// Package httpc provides a production-grade HTTP client for JSON API communication.
//
// Features:
//   - All standard HTTP methods (GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS)
//   - JSON encode/decode via [github.com/gtkit/json/v2] (build-tag swappable backend)
//   - Connection pooling, keep-alive, HTTP/2
//   - Body draining for connection reuse
//   - Structured logging at every request lifecycle stage
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
)

// Logger defines a minimal structured logging interface.
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
	http   *http.Client
	logger Logger
}

// Option configures a [Client].
type Option func(*Client)

// New creates a new [Client] with production-grade defaults.
func New(opts ...Option) *Client {
	c := &Client{
		http:   newDefaultHTTPClient(defaultTimeout),
		logger: NopLogger{},
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
	return c.doRequestJSON(ctx, http.MethodGet, url, headers, nil, result)
}

// PostJSON sends POST with a JSON body and JSON-decodes the response.
func (c *Client) PostJSON(ctx context.Context, url string, body any, result any) (int, error) {
	return c.doRequestJSON(ctx, http.MethodPost, url, nil, body, result)
}

// PutJSON sends PUT with a JSON body and JSON-decodes the response.
func (c *Client) PutJSON(ctx context.Context, url string, body any, result any) (int, error) {
	return c.doRequestJSON(ctx, http.MethodPut, url, nil, body, result)
}

// PatchJSON sends PATCH with a JSON body and JSON-decodes the response.
func (c *Client) PatchJSON(ctx context.Context, url string, body any, result any) (int, error) {
	return c.doRequestJSON(ctx, http.MethodPatch, url, nil, body, result)
}

// DeleteJSON sends DELETE with optional JSON body and JSON-decodes the response.
func (c *Client) DeleteJSON(ctx context.Context, url string, body any, result any) (int, error) {
	return c.doRequestJSON(ctx, http.MethodDelete, url, nil, body, result)
}

// --- Raw response methods ---

// GetRaw sends GET and returns the raw response body bytes.
// Useful when the response needs multiple unmarshal passes.
func (c *Client) GetRaw(ctx context.Context, url string, headers map[string]string) ([]byte, int, error) {
	return c.doRequestRaw(ctx, http.MethodGet, url, headers, nil)
}

// PostRaw sends POST with a JSON body and returns raw response bytes.
func (c *Client) PostRaw(ctx context.Context, url string, body any) ([]byte, int, error) {
	return c.doRequestRaw(ctx, http.MethodPost, url, nil, body)
}

// --- Advanced: custom headers + body ---

// RequestJSON sends any HTTP method with custom headers, optional JSON body,
// and JSON-decodes the response.
func (c *Client) RequestJSON(ctx context.Context, method, url string, headers map[string]string, body any, result any) (int, error) {
	return c.doRequestJSON(ctx, method, url, headers, body, result)
}

// RequestRaw sends any HTTP method with custom headers and optional JSON body,
// returns raw response bytes.
func (c *Client) RequestRaw(ctx context.Context, method, url string, headers map[string]string, body any) ([]byte, int, error) {
	return c.doRequestRaw(ctx, method, url, headers, body)
}

// --- Low-level: Do with raw *http.Request ---

// Do executes an arbitrary [http.Request] and returns the response.
// The caller is responsible for closing the response body.
// Prefer the higher-level methods unless you need full control.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	c.logger.Debug("httpc: request",
		"method", req.Method, "url", req.URL.String(),
	)
	resp, err := c.http.Do(req)
	if err != nil {
		c.logger.Error("httpc: request failed",
			"method", req.Method, "url", req.URL.String(), "error", err.Error(),
		)
		return nil, fmt.Errorf("httpc: %s %s: %w", req.Method, req.URL.String(), err)
	}
	c.logger.Info("httpc: response",
		"method", req.Method, "url", req.URL.String(),
		"status", resp.StatusCode,
	)
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
func (c *Client) doRequestJSON(ctx context.Context, method, url string, headers map[string]string, body any, result any) (int, error) {
	req, err := c.buildRequest(ctx, method, url, headers, body)
	if err != nil {
		return 0, err
	}

	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer drainAndClose(resp.Body)

	if result != nil {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			c.logger.Warn("httpc: decode response failed",
				"method", method, "url", url,
				"status", resp.StatusCode, "error", err.Error(),
			)
			return resp.StatusCode, fmt.Errorf("httpc: decode response: %w", err)
		}
	}
	return resp.StatusCode, nil
}

// doRequestRaw builds, executes, and returns the raw response body.
func (c *Client) doRequestRaw(ctx context.Context, method, url string, headers map[string]string, body any) ([]byte, int, error) {
	req, err := c.buildRequest(ctx, method, url, headers, body)
	if err != nil {
		return nil, 0, err
	}

	resp, err := c.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer drainAndClose(resp.Body)

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("httpc: read body: %w", err)
	}
	return data, resp.StatusCode, nil
}

// drainAndClose fully consumes and closes the body for connection reuse.
func drainAndClose(r io.ReadCloser) {
	_, _ = io.Copy(io.Discard, r)
	r.Close()
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

// WithTimeout sets the HTTP client timeout (default 10s).
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		if d > 0 {
			c.http.Timeout = d
		}
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
