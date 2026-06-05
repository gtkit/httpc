// Package httpc provides a production-grade HTTP client for JSON API communication.
//
// Features:
//   - All standard HTTP methods (GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS)
//   - JSON encode/decode via [github.com/gtkit/json/v2] (build-tag swappable backend)
//   - Connection pooling, keep-alive, HTTP/2
//   - Bounded body draining for connection reuse
//   - Response body size cap to guard against memory exhaustion
//   - Optional debug logging (requests) and error logging (transport failures)
//   - Context propagation and cancellation
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
	"net"
	"net/http"
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

// Logger defines a minimal structured logging interface.
//
// httpc keeps internal logging deliberately sparse to avoid duplicating what
// callers already log from return values: it emits Debug for each outgoing
// request (and for read/decode/empty-body diagnostics) and Error only on
// transport failures. The Info and Warn methods are part of the interface for
// adapter completeness but are not currently called by httpc.
//
// Implement this to integrate with zap, slog, zerolog, or gtkit/logger:
//
//	type zapAdapter struct{ l *zap.SugaredLogger }
//	func (z *zapAdapter) Debug(msg string, kv ...any) { z.l.Debugw(msg, kv...) }
//	func (z *zapAdapter) Info(msg string, kv ...any)  { z.l.Infow(msg, kv...)  }
//	func (z *zapAdapter) Warn(msg string, kv ...any)  { z.l.Warnw(msg, kv...)  }
//	func (z *zapAdapter) Error(msg string, kv ...any) { z.l.Errorw(msg, kv...) }
type Logger interface {
	Debug(msg string, keysAndValues ...any)
	Info(msg string, keysAndValues ...any)
	Warn(msg string, keysAndValues ...any)
	Error(msg string, keysAndValues ...any)
}

// NopLogger discards all log output. Used as the default.
type NopLogger struct{}

func (NopLogger) Debug(string, ...any) {}
func (NopLogger) Info(string, ...any)  {}
func (NopLogger) Warn(string, ...any)  {}
func (NopLogger) Error(string, ...any) {}

// Client is a concurrency-safe HTTP client optimized for JSON APIs.
// All fields are read-only after construction — safe to share across goroutines.
type Client struct {
	http             *http.Client
	logger           Logger
	maxResponseBytes int64
}

// Option configures a [Client].
type Option func(*Client)

// New creates a new [Client] with production-grade defaults.
func New(opts ...Option) *Client {
	c := &Client{
		http:             newDefaultHTTPClient(defaultTimeout),
		logger:           NopLogger{},
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
	// Redacted() masks any userinfo password embedded in the URL so credentials
	// do not leak into logs. NOTE: secrets passed as query parameters are still
	// logged — prefer headers for tokens.
	safeURL := req.URL.Redacted()
	c.logger.Debug("httpc: request",
		"method", req.Method, "url", safeURL,
	)
	resp, err := c.http.Do(req)
	if err != nil {
		c.logger.Error("httpc: request failed",
			"method", req.Method, "url", safeURL, "error", err.Error(),
		)
		return nil, fmt.Errorf("httpc: %s %s: %w", req.Method, safeURL, err)
	}
	return resp, nil
}

// Head sends a HEAD request and returns the response headers.
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
	var bodyReader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("httpc: marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("httpc: create request: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req, nil
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
		c.logger.Debug("httpc: read response failed",
			"method", method, "url", url,
			"status", resp.StatusCode, "error", err.Error(),
		)
		return resp.Header, resp.StatusCode, err
	}
	// Decouple transport from decoding: surface an empty body as a distinct
	// sentinel so callers can tell "no content" apart from malformed JSON.
	if len(bytes.TrimSpace(data)) == 0 {
		c.logger.Debug("httpc: empty response body",
			"method", method, "url", url, "status", resp.StatusCode,
		)
		return resp.Header, resp.StatusCode, ErrEmptyBody
	}
	if err := json.Unmarshal(data, result); err != nil {
		c.logger.Debug("httpc: decode response failed",
			"method", method, "url", url,
			"status", resp.StatusCode, "error", err.Error(),
		)
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
		c.logger.Debug("httpc: read response failed",
			"method", method, "url", url,
			"status", resp.StatusCode, "error", err.Error(),
		)
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
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		if hc != nil {
			c.http = hc
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
func WithMaxResponseBytes(n int64) Option {
	return func(c *Client) {
		if n < 0 {
			n = 0
		}
		c.maxResponseBytes = n
	}
}

// WithLogger sets a structured logger for request/response logging.
func WithLogger(l Logger) Option {
	return func(c *Client) {
		if l != nil {
			c.logger = l
		}
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

// newDefaultHTTPClient creates an HTTP client with production-grade defaults:
//   - Connection pooling (MaxIdleConns=100, MaxIdleConnsPerHost=10)
//   - Keep-alive (30s)
//   - HTTP/2
//   - TLS handshake timeout (5s)
//   - Response header timeout (10s)
func newDefaultHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
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
