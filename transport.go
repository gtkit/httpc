package httpc

import (
	"net"
	"net/http"
	"time"
)

const (
	defaultTimeout             = 10 * time.Second
	defaultMaxIdleConns        = 100
	defaultMaxIdleConnsPerHost = 10
	defaultIdleConnTimeout     = 90 * time.Second
)

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
