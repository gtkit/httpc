// Package httpc provides a production-grade HTTP client for JSON API communication.
//
// Features:
//   - All standard HTTP methods (GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS)
//   - Base URL and client-wide default headers ([WithBaseURL], [WithDefaultHeaders])
//   - JSON encode/decode via [github.com/gtkit/json/v2] (build-tag swappable backend)
//   - Connection pooling, keep-alive, HTTP/2
//   - Bounded body draining for connection reuse
//   - Response body size cap to guard against memory exhaustion
//   - No built-in logging: every outcome (status, headers, error) is returned
//     to the caller, who logs with full business context
//   - Context propagation and cancellation
//
// A nil request body — including a typed nil pointer, map, or slice stored in
// the any parameter — sends no body and no Content-Type, rather than the JSON
// literal "null".
//
// Retries are intentionally left to the caller: only the caller knows which
// requests are idempotent and what backoff policy fits. Request bodies are
// replayable (GetBody is set), so a caller-side retry can resend safely.
//
// Quick start:
//
//	c := httpc.New(httpc.WithTimeout(10 * time.Second))
//	var result MyResponse
//	status, err := c.PostJSON(ctx, url, reqBody, &result)
package httpc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/gtkit/json/v2"
)

const (
	defaultTimeout             = 10 * time.Second
	defaultMaxIdleConns        = 100
	defaultMaxIdleConnsPerHost = 10
	defaultIdleConnTimeout     = 90 * time.Second
	// defaultMaxResponseBytes caps response body reads to guard against memory
	// exhaustion from oversized or malicious responses. Override with
	// [WithMaxResponseBytes].
	defaultMaxResponseBytes = 10 << 20 // 10 MiB
	// drainLimit caps how many leftover body bytes are drained for connection
	// reuse, so an attacker-controlled body cannot force an unbounded read.
	drainLimit = 4 << 10 // 4 KiB
)

// ErrResponseTooLarge is returned (wrapped) when a response body exceeds the
// configured maximum. See [WithMaxResponseBytes]. Detect it with [errors.Is].
var ErrResponseTooLarge = errors.New("httpc: response body exceeds limit")

// ErrEmptyBody is returned by the JSON convenience methods when the server sends
// an empty (or whitespace-only) response body but a decode target was provided.
// It lets callers distinguish "no content" (e.g. 204, an empty 502) from
// malformed JSON. Detect it with [errors.Is]. Use the Raw methods if an empty
// body is expected and should not be treated as an error.
var ErrEmptyBody = errors.New("httpc: empty response body")

// Client is a concurrency-safe HTTP client optimized for JSON APIs.
// All fields are read-only after construction — safe to share across goroutines.
//
// By design httpc does not log: every outcome (status, headers, error) is
// returned to the caller, who should log it from their own layer with full
// business context. For transport-level tracing (DNS, TLS, connection reuse),
// attach a [net/http/httptrace.ClientTrace] to the request context.
type Client struct {
	http             *http.Client
	maxResponseBytes int64
	baseURL          string
	defaultHeaders   map[string]string
}

// Option configures a [Client]. Options are applied in the order given;
// [WithHTTPClient] replaces the underlying client wholesale, so list it before
// mutating options such as [WithTimeout] or [WithCheckRedirect].
type Option func(*Client)

// New creates a new [Client] with production-grade defaults.
func New(opts ...Option) *Client {
	c := &Client{
		http:             newDefaultHTTPClient(defaultTimeout),
		maxResponseBytes: defaultMaxResponseBytes,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// HTTPClient returns the underlying [http.Client] for advanced usage.
func (c *Client) HTTPClient() *http.Client { return c.http }

// --- JSON convenience methods ---

// GetJSON sends GET with custom headers and JSON-decodes the response.
func (c *Client) GetJSON(ctx context.Context, url string, headers map[string]string, result any) (int, error) {
	_, status, err := c.doRequestJSON(ctx, http.MethodGet, url, headers, nil, result)
	return status, err
}

// PostJSON sends POST with a JSON body and JSON-decodes the response.
func (c *Client) PostJSON(ctx context.Context, url string, body any, result any) (int, error) {
	_, status, err := c.doRequestJSON(ctx, http.MethodPost, url, nil, body, result)
	return status, err
}

// PutJSON sends PUT with a JSON body and JSON-decodes the response.
func (c *Client) PutJSON(ctx context.Context, url string, body any, result any) (int, error) {
	_, status, err := c.doRequestJSON(ctx, http.MethodPut, url, nil, body, result)
	return status, err
}

// PatchJSON sends PATCH with a JSON body and JSON-decodes the response.
func (c *Client) PatchJSON(ctx context.Context, url string, body any, result any) (int, error) {
	_, status, err := c.doRequestJSON(ctx, http.MethodPatch, url, nil, body, result)
	return status, err
}

// DeleteJSON sends DELETE with optional JSON body and JSON-decodes the response.
func (c *Client) DeleteJSON(ctx context.Context, url string, body any, result any) (int, error) {
	_, status, err := c.doRequestJSON(ctx, http.MethodDelete, url, nil, body, result)
	return status, err
}

// --- JSON convenience methods returning response headers ---
//
// The *WithHeader variants mirror the methods above but additionally return the
// response [http.Header]. The header is returned whenever a response was
// received — including when JSON decoding fails — so callers can inspect
// metadata such as Content-Type, ETag, or X-Request-Id for diagnostics.
// On transport-level errors (no response) the returned header is nil.

// GetJSONWithHeader sends GET with custom headers, JSON-decodes the response,
// and returns the response headers.
func (c *Client) GetJSONWithHeader(ctx context.Context, url string, headers map[string]string, result any) (http.Header, int, error) {
	return c.doRequestJSON(ctx, http.MethodGet, url, headers, nil, result)
}

// PostJSONWithHeader sends POST with a JSON body, JSON-decodes the response,
// and returns the response headers.
func (c *Client) PostJSONWithHeader(ctx context.Context, url string, body any, result any) (http.Header, int, error) {
	return c.doRequestJSON(ctx, http.MethodPost, url, nil, body, result)
}

// PutJSONWithHeader sends PUT with a JSON body, JSON-decodes the response,
// and returns the response headers.
func (c *Client) PutJSONWithHeader(ctx context.Context, url string, body any, result any) (http.Header, int, error) {
	return c.doRequestJSON(ctx, http.MethodPut, url, nil, body, result)
}

// PatchJSONWithHeader sends PATCH with a JSON body, JSON-decodes the response,
// and returns the response headers.
func (c *Client) PatchJSONWithHeader(ctx context.Context, url string, body any, result any) (http.Header, int, error) {
	return c.doRequestJSON(ctx, http.MethodPatch, url, nil, body, result)
}

// DeleteJSONWithHeader sends DELETE with an optional JSON body, JSON-decodes the
// response, and returns the response headers.
func (c *Client) DeleteJSONWithHeader(ctx context.Context, url string, body any, result any) (http.Header, int, error) {
	return c.doRequestJSON(ctx, http.MethodDelete, url, nil, body, result)
}

// --- Raw response methods ---

// GetRaw sends GET and returns the raw response body bytes.
// Useful when the response needs multiple unmarshal passes.
func (c *Client) GetRaw(ctx context.Context, url string, headers map[string]string) ([]byte, int, error) {
	_, data, status, err := c.doRequestRaw(ctx, http.MethodGet, url, headers, nil)
	return data, status, err
}

// PostRaw sends POST with a JSON body and returns raw response bytes.
func (c *Client) PostRaw(ctx context.Context, url string, body any) ([]byte, int, error) {
	_, data, status, err := c.doRequestRaw(ctx, http.MethodPost, url, nil, body)
	return data, status, err
}

// --- Raw response methods returning response headers ---

// GetRawWithHeader sends GET and returns the raw response body together with the
// response headers. See the *WithHeader JSON methods for header semantics.
func (c *Client) GetRawWithHeader(ctx context.Context, url string, headers map[string]string) (http.Header, []byte, int, error) {
	return c.doRequestRaw(ctx, http.MethodGet, url, headers, nil)
}

// PostRawWithHeader sends POST with a JSON body and returns the raw response body
// together with the response headers.
func (c *Client) PostRawWithHeader(ctx context.Context, url string, body any) (http.Header, []byte, int, error) {
	return c.doRequestRaw(ctx, http.MethodPost, url, nil, body)
}

// --- Advanced: custom headers + body ---

// RequestJSON sends any HTTP method with custom headers, optional JSON body,
// and JSON-decodes the response.
func (c *Client) RequestJSON(ctx context.Context, method, url string, headers map[string]string, body any, result any) (int, error) {
	_, status, err := c.doRequestJSON(ctx, method, url, headers, body, result)
	return status, err
}

// RequestJSONWithHeader sends any HTTP method with custom headers, optional JSON
// body, JSON-decodes the response, and returns the response headers.
//
// This is the most general header-returning method; the *WithHeader convenience
// methods delegate to the same code path. See the *WithHeader documentation for
// the header return semantics.
func (c *Client) RequestJSONWithHeader(ctx context.Context, method, url string, headers map[string]string, body any, result any) (http.Header, int, error) {
	return c.doRequestJSON(ctx, method, url, headers, body, result)
}

// RequestRaw sends any HTTP method with custom headers and optional JSON body,
// returns raw response bytes.
func (c *Client) RequestRaw(ctx context.Context, method, url string, headers map[string]string, body any) ([]byte, int, error) {
	_, data, status, err := c.doRequestRaw(ctx, method, url, headers, body)
	return data, status, err
}

// RequestRawWithHeader sends any HTTP method with custom headers and optional
// JSON body, returning the raw response body together with the response headers.
func (c *Client) RequestRawWithHeader(ctx context.Context, method, url string, headers map[string]string, body any) (http.Header, []byte, int, error) {
	return c.doRequestRaw(ctx, method, url, headers, body)
}

// --- Low-level: Do with raw *http.Request ---

// Do executes an arbitrary [http.Request] and returns the response.
// The caller is responsible for closing the response body.
// Prefer the higher-level methods unless you need full control.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		// Redacted() masks any userinfo password in the URL so credentials do not
		// leak into the error (which the caller may log). Query-string secrets are
		// not redacted — prefer headers for tokens.
		return nil, fmt.Errorf("httpc: %s %s: %w", req.Method, req.URL.Redacted(), err)
	}
	return resp, nil
}

// Head sends a HEAD request and returns the response headers.
//
// The returned response's Body has already been drained and closed — read
// resp.Header and resp.StatusCode only; reading resp.Body returns an error.
func (c *Client) Head(ctx context.Context, url string, headers map[string]string) (*http.Response, error) {
	req, err := c.buildRequest(ctx, http.MethodHead, url, headers, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	drainAndClose(resp.Body)
	return resp, nil
}

// --- Internal: request building + execution ---

// buildRequest creates an [http.Request] with optional JSON body and headers.
func (c *Client) buildRequest(ctx context.Context, method, url string, headers map[string]string, body any) (*http.Request, error) {
	// hasBody treats a typed-nil body (e.g. a nil *Foo stored in the any
	// parameter) the same as no body, instead of sending the literal "null".
	hasBody := !isNilValue(body)

	var bodyReader io.Reader
	if hasBody {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("httpc: marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.resolveURL(url), bodyReader)
	if err != nil {
		return nil, fmt.Errorf("httpc: create request: %w", err)
	}

	if hasBody {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	// Precedence (later wins): standard headers above, then client-wide
	// defaults, then per-call headers.
	for k, v := range c.defaultHeaders {
		req.Header.Set(k, v)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req, nil
}

// resolveURL prefixes a relative request URL with the configured base URL.
// A URL that already carries a scheme is used as-is, so absolute URLs bypass
// the base. Joining is plain concatenation with slash normalization — query
// strings in the path are preserved untouched.
func (c *Client) resolveURL(target string) string {
	if c.baseURL == "" || strings.Contains(target, "://") {
		return target
	}
	if target == "" {
		return c.baseURL
	}
	return c.baseURL + "/" + strings.TrimLeft(target, "/")
}

// doRequestJSON builds, executes, and JSON-decodes the response.
//
// It returns the response [http.Header] whenever a response was received
// (including on decode failure); the header is nil only on transport-level
// errors where no response exists. The returned header is the response's own
// map — safe to read after the body is drained and closed, since headers are
// fully parsed before the body is read.
func (c *Client) doRequestJSON(ctx context.Context, method, url string, headers map[string]string, body any, result any) (http.Header, int, error) {
	req, err := c.buildRequest(ctx, method, url, headers, body)
	if err != nil {
		return nil, 0, err
	}

	resp, err := c.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer drainAndClose(resp.Body)

	// Fire-and-forget: no decode target, so skip reading the body entirely and
	// let the deferred drain reclaim the connection.
	if result == nil {
		return resp.Header, resp.StatusCode, nil
	}

	data, err := c.readBody(resp.Body)
	if err != nil {
		return resp.Header, resp.StatusCode, err
	}
	// Decouple transport from decoding: surface an empty body as a distinct
	// sentinel so callers can tell "no content" apart from malformed JSON.
	if len(bytes.TrimSpace(data)) == 0 {
		return resp.Header, resp.StatusCode, ErrEmptyBody
	}
	if err := json.Unmarshal(data, result); err != nil {
		return resp.Header, resp.StatusCode, fmt.Errorf("httpc: decode response: %w", err)
	}
	return resp.Header, resp.StatusCode, nil
}

// doRequestRaw builds, executes, and returns the raw response body.
func (c *Client) doRequestRaw(ctx context.Context, method, url string, headers map[string]string, body any) (http.Header, []byte, int, error) {
	req, err := c.buildRequest(ctx, method, url, headers, body)
	if err != nil {
		return nil, nil, 0, err
	}

	resp, err := c.Do(req)
	if err != nil {
		return nil, nil, 0, err
	}
	defer drainAndClose(resp.Body)

	data, err := c.readBody(resp.Body)
	if err != nil {
		return resp.Header, nil, resp.StatusCode, err
	}
	return resp.Header, data, resp.StatusCode, nil
}

// readBody reads the response body, honoring the configured size cap. It returns
// an error wrapping [ErrResponseTooLarge] when the body exceeds the limit
// (maxResponseBytes <= 0 disables the cap). The +1 over the limit lets us
// distinguish "exactly at the limit" (allowed) from "over the limit" (rejected).
func (c *Client) readBody(r io.Reader) ([]byte, error) {
	limit := c.maxResponseBytes
	if limit <= 0 {
		data, err := io.ReadAll(r)
		if err != nil {
			return nil, fmt.Errorf("httpc: read body: %w", err)
		}
		return data, nil
	}

	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("httpc: read body: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w (max %d bytes)", ErrResponseTooLarge, limit)
	}
	return data, nil
}

// isNilValue reports whether v is nil, including a typed nil (a nil pointer,
// map, or slice stored in a non-nil interface), which json.Marshal would
// otherwise serialize as the literal "null". Only these three kinds are
// checked: they are the marshalable nilable kinds. A nil func or chan still
// reaches json.Marshal and fails loudly there, surfacing the caller's mistake.
func isNilValue(v any) bool {
	if v == nil {
		return true
	}
	switch rv := reflect.ValueOf(v); rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}

// drainAndClose consumes up to drainLimit leftover body bytes so the connection
// can be reused, then closes it. The cap prevents an oversized or malicious
// body from forcing an unbounded read just to recycle a connection; if more
// than drainLimit remains, the connection is simply not reused.
func drainAndClose(r io.ReadCloser) {
	_, _ = io.CopyN(io.Discard, r, drainLimit)
	_ = r.Close()
}

// --- Options ---

// WithHTTPClient overrides the underlying [http.Client].
//
// The client struct is shallow-copied so that later mutating options
// ([WithTimeout], [WithCheckRedirect]) modify the copy, never the caller's
// client. The Transport is shared by reference — its connection pool is reused.
// Isolation cuts both ways: changes the caller makes to hc after New (Timeout,
// Jar, CheckRedirect) are not observed by this Client either.
//
// Place this option before mutating options: a [WithTimeout] listed earlier
// would be overwritten by the replacement client.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		if hc != nil {
			clone := *hc
			c.http = &clone
		}
	}
}

// WithCheckRedirect sets the redirect policy used by the underlying [http.Client].
// Pass [http.ErrUseLastResponse] from the function to return the latest 3xx response.
func WithCheckRedirect(fn func(req *http.Request, via []*http.Request) error) Option {
	return func(c *Client) {
		if fn != nil {
			c.http.CheckRedirect = fn
		}
	}
}

// WithoutRedirect disables automatic redirect following.
func WithoutRedirect() Option {
	return WithCheckRedirect(func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	})
}

// WithTimeout sets the HTTP client timeout (default 10s).
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		if d > 0 {
			c.http.Timeout = d
		}
	}
}

// WithMaxResponseBytes caps how many response body bytes httpc reads, guarding
// against memory exhaustion from oversized or malicious responses. The default
// is 10 MiB. Pass 0 (or a negative value) to disable the limit. When a body
// exceeds the cap, JSON/Raw methods return an error wrapping [ErrResponseTooLarge].
//
// The limit counts decompressed bytes (Go transparently decompresses gzip
// responses), which is what actually occupies memory — do not size it from
// Content-Length, which reflects the compressed payload.
func WithMaxResponseBytes(n int64) Option {
	return func(c *Client) {
		if n < 0 {
			n = 0
		}
		c.maxResponseBytes = n
	}
}

// WithBaseURL sets a base URL that is prefixed to relative request URLs, so
// call sites pass only the path ("/v1/users") instead of rebuilding the full
// URL. A trailing slash on the base is trimmed. Request URLs that already
// contain a scheme ("https://...") bypass the base and are used as-is.
func WithBaseURL(u string) Option {
	return func(c *Client) {
		c.baseURL = strings.TrimRight(u, "/")
	}
}

// WithDefaultHeaders sets headers applied to every request — typically
// Authorization, User-Agent, or a service-identity header. A per-call header
// with the same name overrides the default. The map is copied; changes the
// caller makes to it after New are not observed. Repeated use merges into the
// existing defaults.
func WithDefaultHeaders(h map[string]string) Option {
	return func(c *Client) {
		if len(h) == 0 {
			return
		}
		if c.defaultHeaders == nil {
			c.defaultHeaders = make(map[string]string, len(h))
		}
		maps.Copy(c.defaultHeaders, h)
	}
}

// WithTransport overrides the HTTP transport.
func WithTransport(t http.RoundTripper) Option {
	return func(c *Client) {
		if t != nil {
			c.http.Transport = t
		}
	}
}

// WithMaxIdleConnsPerHost sets the maximum idle (keep-alive) connections kept
// per host (default 10). Raise this when most traffic targets a single
// upstream, so high concurrency does not churn through new connections.
//
// The transport is cloned before the change, so a transport shared via
// [WithHTTPClient] is never mutated — at the cost that this client gets its
// own connection pool. A nil transport falls back to a clone of
// [http.DefaultTransport]. A custom non-[*http.Transport] RoundTripper is
// left untouched — configure its pool directly instead.
func WithMaxIdleConnsPerHost(n int) Option {
	return func(c *Client) {
		if n <= 0 {
			return
		}
		t, ok := c.http.Transport.(*http.Transport)
		if !ok {
			if c.http.Transport != nil {
				return
			}
			t = http.DefaultTransport.(*http.Transport)
		}
		clone := t.Clone()
		clone.MaxIdleConnsPerHost = n
		c.http.Transport = clone
	}
}

// newDefaultHTTPClient creates an HTTP client with production-grade defaults:
//   - Proxy from environment (HTTP_PROXY / HTTPS_PROXY / NO_PROXY)
//   - Connection pooling (MaxIdleConns=100, MaxIdleConnsPerHost=10)
//   - Keep-alive (30s)
//   - HTTP/2
//   - TLS handshake timeout (5s)
//   - Response header timeout (10s)
func newDefaultHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   5 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:          defaultMaxIdleConns,
			MaxIdleConnsPerHost:   defaultMaxIdleConnsPerHost,
			IdleConnTimeout:       defaultIdleConnTimeout,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ForceAttemptHTTP2:     true,
		},
		Timeout: timeout,
	}
}
