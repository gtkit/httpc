package httpc

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gtkit/json/v2"
)

// sizedServer returns a server whose body is exactly n bytes.
func sizedServer(n int) *httptest.Server {
	body := strings.Repeat("x", n)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "req-sz")
		_, _ = w.Write([]byte(body))
	}))
}

// --- Helper: echo server that returns request info ---

func echoServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"method":       r.Method,
			"content_type": r.Header.Get("Content-Type"),
			"auth":         r.Header.Get("Authorization"),
		}
		data, _ := json.Marshal(resp)
		w.Write(data)
	}))
}

// --- New ---

func TestNew_Defaults(t *testing.T) {
	c := New()
	if c.http == nil || c.http.Timeout != defaultTimeout {
		t.Error("bad defaults")
	}
}

func TestNew_WithOptions(t *testing.T) {
	c := New(WithTimeout(30 * time.Second))
	if c.http.Timeout != 30*time.Second {
		t.Errorf("timeout = %v", c.http.Timeout)
	}
}

func TestNew_NilOptions(t *testing.T) {
	c := New(WithHTTPClient(nil), WithTransport(nil), WithTimeout(0))
	if c.http == nil {
		t.Error("nil")
	}
}

func TestWithoutRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("target"))
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	c := New(WithHTTPClient(redirector.Client()), WithoutRedirect())
	body, status, err := c.GetRaw(t.Context(), redirector.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusFound {
		t.Errorf("status = %d, want %d", status, http.StatusFound)
	}
	if string(body) == "target" {
		t.Error("unexpectedly followed redirect")
	}
}

func TestWithCheckRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	var seenVia int
	c := New(
		WithHTTPClient(redirector.Client()),
		WithCheckRedirect(func(req *http.Request, via []*http.Request) error {
			seenVia = len(via)
			return nil
		}),
	)

	var result map[string]bool
	status, err := c.GetJSON(t.Context(), redirector.URL, nil, &result)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusOK {
		t.Errorf("status = %d, want %d", status, http.StatusOK)
	}
	if !result["ok"] {
		t.Error("missing redirected response body")
	}
	if seenVia != 1 {
		t.Errorf("seenVia = %d, want 1", seenVia)
	}
}

func TestHTTPClient(t *testing.T) {
	if New().HTTPClient() == nil {
		t.Error("nil")
	}
}

// --- GetJSON ---

func TestGetJSON(t *testing.T) {
	srv := echoServer()
	defer srv.Close()

	c := New(WithHTTPClient(srv.Client()))

	var result map[string]any
	status, err := c.GetJSON(t.Context(), srv.URL, map[string]string{"Authorization": "Bearer tok"}, &result)
	if err != nil || status != 200 {
		t.Fatalf("err=%v status=%d", err, status)
	}
	if result["method"] != "GET" {
		t.Errorf("method = %v", result["method"])
	}
	if result["auth"] != "Bearer tok" {
		t.Errorf("auth = %v", result["auth"])
	}
}

// --- PostJSON ---

func TestPostJSON(t *testing.T) {
	srv := echoServer()
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()))

	var result map[string]any
	status, err := c.PostJSON(t.Context(), srv.URL, map[string]string{"key": "val"}, &result)
	if err != nil || status != 200 {
		t.Fatalf("err=%v status=%d", err, status)
	}
	if result["method"] != "POST" {
		t.Errorf("method = %v", result["method"])
	}
	if result["content_type"] != "application/json" {
		t.Errorf("content_type = %v", result["content_type"])
	}
}

// --- PutJSON ---

func TestPutJSON(t *testing.T) {
	srv := echoServer()
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()))

	var result map[string]any
	status, err := c.PutJSON(t.Context(), srv.URL, map[string]string{"a": "b"}, &result)
	if err != nil || status != 200 || result["method"] != "PUT" {
		t.Errorf("err=%v status=%d method=%v", err, status, result["method"])
	}
}

// --- PatchJSON ---

func TestPatchJSON(t *testing.T) {
	srv := echoServer()
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()))

	var result map[string]any
	status, err := c.PatchJSON(t.Context(), srv.URL, map[string]string{}, &result)
	if err != nil || status != 200 || result["method"] != "PATCH" {
		t.Errorf("err=%v status=%d method=%v", err, status, result["method"])
	}
}

// --- DeleteJSON ---

func TestDeleteJSON(t *testing.T) {
	srv := echoServer()
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()))

	var result map[string]any
	status, err := c.DeleteJSON(t.Context(), srv.URL, nil, &result)
	if err != nil || status != 200 || result["method"] != "DELETE" {
		t.Errorf("err=%v status=%d method=%v", err, status, result["method"])
	}
}

// --- GetRaw ---

func TestGetRaw(t *testing.T) {
	srv := echoServer()
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()))

	body, status, err := c.GetRaw(t.Context(), srv.URL, nil)
	if err != nil || status != 200 || len(body) == 0 {
		t.Fatalf("err=%v status=%d len=%d", err, status, len(body))
	}
}

// --- PostRaw ---

func TestPostRaw(t *testing.T) {
	srv := echoServer()
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()))

	body, status, err := c.PostRaw(t.Context(), srv.URL, map[string]string{"x": "y"})
	if err != nil || status != 200 || len(body) == 0 {
		t.Errorf("err=%v status=%d len=%d", err, status, len(body))
	}
}

// --- RequestJSON (generic method) ---

func TestRequestJSON(t *testing.T) {
	srv := echoServer()
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()))

	var result map[string]any
	status, err := c.RequestJSON(t.Context(), http.MethodOptions, srv.URL,
		map[string]string{"X-Custom": "test"}, nil, &result)
	if err != nil || status != 200 {
		t.Fatalf("err=%v status=%d", err, status)
	}
}

// --- Head ---

func TestHead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("method = %s", r.Method)
		}
		w.Header().Set("X-Test", "ok")
	}))
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()))

	resp, err := c.Head(t.Context(), srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("X-Test") != "ok" {
		t.Error("missing X-Test header")
	}
}

// --- Do (low-level) ---

func TestDo(t *testing.T) {
	srv := echoServer()
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()))

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

// --- Error cases ---

func TestPostJSON_NetworkError(t *testing.T) {
	c := New(WithTimeout(time.Second))
	var result map[string]any
	_, err := c.PostJSON(t.Context(), "http://127.0.0.1:1", map[string]string{}, &result)
	if err == nil {
		t.Fatal("expected error")
	}
	// The error identifies the method+URL for the caller to log.
	if !strings.Contains(err.Error(), "POST") {
		t.Errorf("error lacks method context: %v", err)
	}
}

func TestPostJSON_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	c := New(WithHTTPClient(srv.Client()))
	var result map[string]any
	_, err := c.PostJSON(t.Context(), srv.URL, nil, &result)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "decode") {
		t.Errorf("expected decode error, got: %v", err)
	}
}

func TestPostJSON_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
	}))
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var result map[string]any
	_, err := c.PostJSON(ctx, srv.URL, nil, &result)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestPostJSON_Non2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"error":"bad"}`))
	}))
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()))

	var result map[string]any
	status, err := c.PostJSON(t.Context(), srv.URL, nil, &result)
	if err != nil {
		t.Fatal(err)
	}
	if status != 500 {
		t.Errorf("status = %d", status)
	}
}

func TestGetRaw_LargeResponse(t *testing.T) {
	big := strings.Repeat("x", 200000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(big))
	}))
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()))

	body, _, err := c.GetRaw(t.Context(), srv.URL, nil)
	if err != nil || len(body) != 200000 {
		t.Errorf("err=%v len=%d", err, len(body))
	}
}

func TestGetRaw_EmptyBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
	}))
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()))

	body, status, err := c.GetRaw(t.Context(), srv.URL, nil)
	if err != nil || status != 204 || len(body) != 0 {
		t.Errorf("err=%v status=%d len=%d", err, status, len(body))
	}
}

// --- PostJSON with nil result (fire-and-forget) ---

func TestPostJSON_NilResult(t *testing.T) {
	srv := echoServer()
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()))

	status, err := c.PostJSON(t.Context(), srv.URL, map[string]string{"a": "b"}, nil)
	if err != nil || status != 200 {
		t.Errorf("err=%v status=%d", err, status)
	}
}

// --- Concurrent safety ---

func TestConcurrent_PostJSON(t *testing.T) {
	srv := echoServer()
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()))

	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for range 100 {
		wg.Go(func() {
			var r map[string]any
			if _, err := c.PostJSON(t.Context(), srv.URL, map[string]string{}, &r); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestConcurrent_GetRaw(t *testing.T) {
	srv := echoServer()
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()))

	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for range 100 {
		wg.Go(func() {
			if _, _, err := c.GetRaw(t.Context(), srv.URL, nil); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// --- *WithHeader methods ---

// headerServer echoes the request method and sets a few response headers.
func headerServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "req-123")
		w.Header().Add("X-Multi", "a")
		w.Header().Add("X-Multi", "b")
		data, _ := json.Marshal(map[string]any{"method": r.Method})
		w.Write(data)
	}))
}

func TestRequestJSONWithHeader(t *testing.T) {
	srv := headerServer()
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()))

	var result map[string]any
	header, status, err := c.RequestJSONWithHeader(t.Context(), http.MethodGet, srv.URL, nil, nil, &result)
	if err != nil || status != 200 {
		t.Fatalf("err=%v status=%d", err, status)
	}
	if header == nil {
		t.Fatal("header is nil")
	}
	if header.Get("X-Request-Id") != "req-123" {
		t.Errorf("X-Request-Id = %q", header.Get("X-Request-Id"))
	}
	if got := header.Values("X-Multi"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("X-Multi = %v", got)
	}
	if result["method"] != "GET" {
		t.Errorf("method = %v", result["method"])
	}
}

func TestGetJSONWithHeader(t *testing.T) {
	srv := headerServer()
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()))

	var result map[string]any
	header, status, err := c.GetJSONWithHeader(t.Context(), srv.URL,
		map[string]string{"Authorization": "Bearer tok"}, &result)
	if err != nil || status != 200 || header.Get("X-Request-Id") != "req-123" || result["method"] != "GET" {
		t.Fatalf("err=%v status=%d xrid=%q method=%v", err, status, header.Get("X-Request-Id"), result["method"])
	}
}

func TestPostJSONWithHeader(t *testing.T) {
	srv := headerServer()
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()))

	var result map[string]any
	header, status, err := c.PostJSONWithHeader(t.Context(), srv.URL, map[string]string{"k": "v"}, &result)
	if err != nil || status != 200 || header.Get("Content-Type") != "application/json" || result["method"] != "POST" {
		t.Fatalf("err=%v status=%d ct=%q method=%v", err, status, header.Get("Content-Type"), result["method"])
	}
}

func TestPutPatchDeleteJSONWithHeader(t *testing.T) {
	srv := headerServer()
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()))

	cases := []struct {
		name   string
		call   func(result any) (http.Header, int, error)
		method string
	}{
		{"PUT", func(r any) (http.Header, int, error) { return c.PutJSONWithHeader(t.Context(), srv.URL, nil, r) }, "PUT"},
		{"PATCH", func(r any) (http.Header, int, error) { return c.PatchJSONWithHeader(t.Context(), srv.URL, nil, r) }, "PATCH"},
		{"DELETE", func(r any) (http.Header, int, error) { return c.DeleteJSONWithHeader(t.Context(), srv.URL, nil, r) }, "DELETE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var result map[string]any
			header, status, err := tc.call(&result)
			if err != nil || status != 200 {
				t.Fatalf("err=%v status=%d", err, status)
			}
			if header.Get("X-Request-Id") != "req-123" {
				t.Errorf("X-Request-Id = %q", header.Get("X-Request-Id"))
			}
			if result["method"] != tc.method {
				t.Errorf("method = %v, want %s", result["method"], tc.method)
			}
		})
	}
}

// Header must be returned even when JSON decoding fails, so callers can
// inspect Content-Type / request-id for diagnostics.
func TestWithHeader_DecodeError_StillReturnsHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "req-err")
		w.WriteHeader(500)
		w.Write([]byte("not json"))
	}))
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()))

	var result map[string]any
	header, status, err := c.GetJSONWithHeader(t.Context(), srv.URL, nil, &result)
	if err == nil {
		t.Fatal("expected decode error")
	}
	if status != 500 {
		t.Errorf("status = %d, want 500", status)
	}
	if header == nil || header.Get("X-Request-Id") != "req-err" {
		t.Errorf("header lost on decode error: %v", header)
	}
}

// On transport-level errors there is no response, so header must be nil.
func TestWithHeader_TransportError_NilHeader(t *testing.T) {
	c := New(WithTimeout(time.Second))
	var result map[string]any
	header, status, err := c.PostJSONWithHeader(t.Context(), "http://127.0.0.1:1", nil, &result)
	if err == nil {
		t.Fatal("expected transport error")
	}
	if header != nil {
		t.Errorf("header = %v, want nil", header)
	}
	if status != 0 {
		t.Errorf("status = %d, want 0", status)
	}
}

// nil result (fire-and-forget) must still drain the body and return the header.
func TestWithHeader_NilResult(t *testing.T) {
	srv := headerServer()
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()))

	header, status, err := c.PostJSONWithHeader(t.Context(), srv.URL, map[string]string{"a": "b"}, nil)
	if err != nil || status != 200 {
		t.Fatalf("err=%v status=%d", err, status)
	}
	if header.Get("X-Request-Id") != "req-123" {
		t.Errorf("X-Request-Id = %q", header.Get("X-Request-Id"))
	}
}

// Cross-verification: the WithHeader variant must yield the same status/decode
// result as the existing GetJSON for an identical request.
func TestWithHeader_ConsistentWithPlain(t *testing.T) {
	srv := headerServer()
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()))

	var plain map[string]any
	plainStatus, plainErr := c.GetJSON(t.Context(), srv.URL, nil, &plain)

	var withHdr map[string]any
	header, hdrStatus, hdrErr := c.GetJSONWithHeader(t.Context(), srv.URL, nil, &withHdr)

	if plainErr != nil || hdrErr != nil {
		t.Fatalf("plainErr=%v hdrErr=%v", plainErr, hdrErr)
	}
	if plainStatus != hdrStatus {
		t.Errorf("status mismatch: plain=%d withHeader=%d", plainStatus, hdrStatus)
	}
	if plain["method"] != withHdr["method"] {
		t.Errorf("body mismatch: plain=%v withHeader=%v", plain["method"], withHdr["method"])
	}
	if header == nil {
		t.Error("WithHeader returned nil header on success")
	}
}

// Header must remain readable after the body has been drained/closed.
func TestWithHeader_ReadableAfterBodyDrain(t *testing.T) {
	srv := headerServer()
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()))

	var result map[string]any
	header, _, err := c.GetJSONWithHeader(t.Context(), srv.URL, nil, &result)
	if err != nil {
		t.Fatal(err)
	}
	// doRequestJSON has already drained+closed the body via defer by now.
	if header.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type unreadable after drain: %q", header.Get("Content-Type"))
	}
}

func TestConcurrent_WithHeader(t *testing.T) {
	srv := headerServer()
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()))

	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for range 100 {
		wg.Go(func() {
			var r map[string]any
			header, _, err := c.PostJSONWithHeader(t.Context(), srv.URL, map[string]string{}, &r)
			if err != nil {
				errs <- err
				return
			}
			if header.Get("X-Request-Id") != "req-123" {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// --- Raw *WithHeader methods ---

func TestGetRawWithHeader(t *testing.T) {
	srv := headerServer()
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()))

	header, body, status, err := c.GetRawWithHeader(t.Context(), srv.URL, nil)
	if err != nil || status != 200 || len(body) == 0 {
		t.Fatalf("err=%v status=%d len=%d", err, status, len(body))
	}
	if header.Get("X-Request-Id") != "req-123" {
		t.Errorf("X-Request-Id = %q", header.Get("X-Request-Id"))
	}
}

func TestPostRawWithHeader(t *testing.T) {
	srv := headerServer()
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()))

	header, body, status, err := c.PostRawWithHeader(t.Context(), srv.URL, map[string]string{"x": "y"})
	if err != nil || status != 200 || len(body) == 0 || header.Get("X-Request-Id") != "req-123" {
		t.Fatalf("err=%v status=%d len=%d xrid=%q", err, status, len(body), header.Get("X-Request-Id"))
	}
}

func TestRequestRawWithHeader(t *testing.T) {
	srv := headerServer()
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()))

	header, body, status, err := c.RequestRawWithHeader(t.Context(), http.MethodGet, srv.URL,
		map[string]string{"X-Custom": "test"}, nil)
	if err != nil || status != 200 || len(body) == 0 || header == nil {
		t.Fatalf("err=%v status=%d len=%d header=%v", err, status, len(body), header)
	}
}

// Cross-verification: Raw WithHeader must match plain GetRaw for the same request.
func TestRawWithHeader_ConsistentWithPlain(t *testing.T) {
	srv := headerServer()
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()))

	plainBody, plainStatus, plainErr := c.GetRaw(t.Context(), srv.URL, nil)
	header, hdrBody, hdrStatus, hdrErr := c.GetRawWithHeader(t.Context(), srv.URL, nil)

	if plainErr != nil || hdrErr != nil {
		t.Fatalf("plainErr=%v hdrErr=%v", plainErr, hdrErr)
	}
	if plainStatus != hdrStatus || string(plainBody) != string(hdrBody) {
		t.Errorf("mismatch: plain(%d,%q) withHeader(%d,%q)", plainStatus, plainBody, hdrStatus, hdrBody)
	}
	if header == nil {
		t.Error("nil header")
	}
}

// --- WithMaxResponseBytes ---

func TestMaxResponseBytes_UnderLimit(t *testing.T) {
	srv := sizedServer(50)
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()), WithMaxResponseBytes(100))

	body, status, err := c.GetRaw(t.Context(), srv.URL, nil)
	if err != nil || status != 200 || len(body) != 50 {
		t.Fatalf("err=%v status=%d len=%d", err, status, len(body))
	}
}

// A body exactly at the limit must be accepted (boundary correctness).
func TestMaxResponseBytes_AtLimit(t *testing.T) {
	srv := sizedServer(100)
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()), WithMaxResponseBytes(100))

	body, status, err := c.GetRaw(t.Context(), srv.URL, nil)
	if err != nil {
		t.Fatalf("body at exact limit rejected: %v", err)
	}
	if status != 200 || len(body) != 100 {
		t.Errorf("status=%d len=%d", status, len(body))
	}
}

func TestMaxResponseBytes_OverLimit_Raw(t *testing.T) {
	srv := sizedServer(101)
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()), WithMaxResponseBytes(100))

	_, _, err := c.GetRaw(t.Context(), srv.URL, nil)
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err = %v, want ErrResponseTooLarge", err)
	}
}

// JSON path must enforce the same cap, and still return the header + status so
// callers can diagnose the oversized response.
func TestMaxResponseBytes_OverLimit_JSON(t *testing.T) {
	payload, _ := json.Marshal(strings.Repeat("x", 1000))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "big")
		_, _ = w.Write(payload)
	}))
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()), WithMaxResponseBytes(100))

	var s string
	header, status, err := c.GetJSONWithHeader(t.Context(), srv.URL, nil, &s)
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err = %v, want ErrResponseTooLarge", err)
	}
	if status != 200 {
		t.Errorf("status = %d, want 200", status)
	}
	if header == nil || header.Get("X-Request-Id") != "big" {
		t.Errorf("header lost on too-large error: %v", header)
	}
}

// Limit 0 disables the cap entirely.
func TestMaxResponseBytes_Unlimited(t *testing.T) {
	srv := sizedServer(300000)
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()), WithMaxResponseBytes(0))

	body, _, err := c.GetRaw(t.Context(), srv.URL, nil)
	if err != nil || len(body) != 300000 {
		t.Fatalf("err=%v len=%d", err, len(body))
	}
}

// Negative values are normalized to "unlimited", never a tiny/zero accidental cap.
func TestMaxResponseBytes_NegativeMeansUnlimited(t *testing.T) {
	srv := sizedServer(5000)
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()), WithMaxResponseBytes(-1))

	body, _, err := c.GetRaw(t.Context(), srv.URL, nil)
	if err != nil || len(body) != 5000 {
		t.Fatalf("err=%v len=%d", err, len(body))
	}
}

// Default cap (10 MiB) must be in effect without any option.
func TestMaxResponseBytes_DefaultApplied(t *testing.T) {
	c := New()
	if c.maxResponseBytes != defaultMaxResponseBytes {
		t.Errorf("default = %d, want %d", c.maxResponseBytes, defaultMaxResponseBytes)
	}
}

// --- ErrEmptyBody ---

func TestGetJSON_EmptyBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200) // 200 with no body
	}))
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()))

	var result map[string]any
	status, err := c.GetJSON(t.Context(), srv.URL, nil, &result)
	if !errors.Is(err, ErrEmptyBody) {
		t.Fatalf("err = %v, want ErrEmptyBody", err)
	}
	if status != 200 {
		t.Errorf("status = %d, want 200", status)
	}
}

func TestGetJSON_WhitespaceOnlyBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("  \n\t  "))
	}))
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()))

	var result map[string]any
	_, err := c.GetJSON(t.Context(), srv.URL, nil, &result)
	if !errors.Is(err, ErrEmptyBody) {
		t.Fatalf("err = %v, want ErrEmptyBody", err)
	}
}

// An empty body and malformed JSON must be distinguishable.
func TestEmptyBody_DistinctFromDecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()))

	var result map[string]any
	_, err := c.GetJSON(t.Context(), srv.URL, nil, &result)
	if err == nil {
		t.Fatal("expected decode error")
	}
	if errors.Is(err, ErrEmptyBody) {
		t.Error("malformed JSON misclassified as ErrEmptyBody")
	}
}

// On an empty body the header and status must still be returned for diagnostics.
func TestGetJSONWithHeader_EmptyBody_ReturnsHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "empty-1")
		w.WriteHeader(502)
	}))
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()))

	var result map[string]any
	header, status, err := c.GetJSONWithHeader(t.Context(), srv.URL, nil, &result)
	if !errors.Is(err, ErrEmptyBody) {
		t.Fatalf("err = %v, want ErrEmptyBody", err)
	}
	if status != 502 {
		t.Errorf("status = %d, want 502", status)
	}
	if header == nil || header.Get("X-Request-Id") != "empty-1" {
		t.Errorf("header lost on empty body: %v", header)
	}
}

// Fire-and-forget (result == nil) must not raise ErrEmptyBody.
func TestEmptyBody_NilResult_NoError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
	}))
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()))

	status, err := c.PostJSON(t.Context(), srv.URL, map[string]string{"a": "b"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != 204 {
		t.Errorf("status = %d, want 204", status)
	}
}

// Raw methods must NOT treat an empty body as an error (no decoding involved).
func TestGetRaw_EmptyBody_NoError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
	}))
	defer srv.Close()
	c := New(WithHTTPClient(srv.Client()))

	body, status, err := c.GetRaw(t.Context(), srv.URL, nil)
	if err != nil {
		t.Fatalf("raw empty body should not error: %v", err)
	}
	if status != 204 || len(body) != 0 {
		t.Errorf("status=%d len=%d", status, len(body))
	}
}

// --- Error redaction ---

// The transport error wraps the URL with userinfo password masked, so a caller
// that logs the returned error does not leak credentials embedded in the URL.
func TestDo_ErrorRedactsURLUserinfo(t *testing.T) {
	c := New(WithTimeout(time.Second))

	u := url.URL{
		Scheme: "http",
		User:   url.UserPassword("alice", "s3cr3t-pass"),
		Host:   "127.0.0.1:1", // unreachable → transport error
	}

	var r map[string]any
	_, err := c.GetJSON(t.Context(), u.String(), nil, &r)
	if err == nil {
		t.Fatal("expected transport error")
	}
	if strings.Contains(err.Error(), "s3cr3t-pass") {
		t.Errorf("password leaked into error: %v", err)
	}
	if !strings.Contains(err.Error(), "xxxxx") {
		t.Errorf("expected redaction marker xxxxx, got: %v", err)
	}
}

// --- Bounded drain must not break connection reuse for normal-sized bodies ---

func TestConnectionReuse(t *testing.T) {
	var newConns int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			atomic.AddInt32(&newConns, 1)
		}
	}
	srv.Start()
	defer srv.Close()

	c := New(WithHTTPClient(srv.Client()))
	for range 5 {
		var r map[string]bool
		if _, err := c.GetJSON(t.Context(), srv.URL, nil, &r); err != nil {
			t.Fatal(err)
		}
	}
	if n := atomic.LoadInt32(&newConns); n != 1 {
		t.Errorf("opened %d connections, want 1 (connection reuse broken)", n)
	}
}
