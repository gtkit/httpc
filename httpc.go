package httpc

import (
	"context"
	"fmt"
	"net/http"
)

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
