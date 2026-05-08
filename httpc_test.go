package httpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gtkit/json/v2"
)

// --- testLogger verifies Logger is actually called ---

type testLogger struct {
	mu   sync.Mutex
	logs []string
}

func (l *testLogger) Debug(msg string, kv ...any) { l.record("DEBUG", msg) }
func (l *testLogger) Info(msg string, kv ...any)  { l.record("INFO", msg) }
func (l *testLogger) Warn(msg string, kv ...any)  { l.record("WARN", msg) }
func (l *testLogger) Error(msg string, kv ...any) { l.record("ERROR", msg) }

func (l *testLogger) record(level, msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.logs = append(l.logs, level+": "+msg)
}

func (l *testLogger) has(substr string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, log := range l.logs {
		if strings.Contains(log, substr) {
			return true
		}
	}
	return false
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
	logger := &testLogger{}
	c := New(WithTimeout(30*time.Second), WithLogger(logger))
	if c.http.Timeout != 30*time.Second {
		t.Errorf("timeout = %v", c.http.Timeout)
	}
}

func TestNew_NilOptions(t *testing.T) {
	c := New(WithHTTPClient(nil), WithLogger(nil), WithTransport(nil), WithTimeout(0))
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

	logger := &testLogger{}
	c := New(WithHTTPClient(srv.Client()), WithLogger(logger))

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

	// Verify logger was called
	if !logger.has("httpc: request") {
		t.Error("logger.Debug not called for request")
	}
	if !logger.has("httpc: response") {
		t.Error("logger.Info not called for response")
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
	logger := &testLogger{}
	c := New(WithTimeout(time.Second), WithLogger(logger))
	var result map[string]any
	_, err := c.PostJSON(t.Context(), "http://127.0.0.1:1", map[string]string{}, &result)
	if err == nil {
		t.Fatal("expected error")
	}
	if !logger.has("httpc: request failed") {
		t.Error("error not logged")
	}
}

func TestPostJSON_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	logger := &testLogger{}
	c := New(WithHTTPClient(srv.Client()), WithLogger(logger))
	var result map[string]any
	_, err := c.PostJSON(t.Context(), srv.URL, nil, &result)
	if err == nil {
		t.Fatal("expected error")
	}
	if !logger.has("decode response failed") {
		t.Error("decode error not logged")
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

// --- NopLogger ---

func TestNopLogger(t *testing.T) {
	var l NopLogger
	l.Debug("x")
	l.Info("x")
	l.Warn("x")
	l.Error("x")
}
