package httpc

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"

	"github.com/gtkit/json/v2"
)

const (
	// defaultMaxResponseBytes caps response body reads to guard against memory
	// exhaustion from oversized or malicious responses. Override with
	// [WithMaxResponseBytes].
	defaultMaxResponseBytes = 10 << 20 // 10 MiB
	// drainLimit caps how many leftover body bytes are drained for connection
	// reuse, so an attacker-controlled body cannot force an unbounded read.
	drainLimit = 4 << 10 // 4 KiB
)

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
