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
