package httpc

import (
	"maps"
	"net/http"
	"strings"
	"time"
)

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
