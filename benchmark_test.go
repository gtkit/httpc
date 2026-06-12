package httpc_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gtkit/httpc"
)

func BenchmarkClient_PostJSON(b *testing.B) {
	server := benchmarkJSONServer()
	defer server.Close()

	client := httpc.New(httpc.WithHTTPClient(server.Client()))
	body := map[string]string{"name": "gtkit"}
	var result struct {
		OK bool `json:"ok"`
	}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		status, err := client.PostJSON(b.Context(), server.URL, body, &result)
		if err != nil {
			b.Fatal(err)
		}
		if status != http.StatusOK || !result.OK {
			b.Fatalf("status=%d ok=%v", status, result.OK)
		}
	}
}

func BenchmarkClient_GetRaw(b *testing.B) {
	server := benchmarkJSONServer()
	defer server.Close()

	client := httpc.New(httpc.WithHTTPClient(server.Client()))

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		body, status, err := client.GetRaw(b.Context(), server.URL, nil)
		if err != nil {
			b.Fatal(err)
		}
		if status != http.StatusOK || len(body) == 0 {
			b.Fatalf("status=%d body=%q", status, body)
		}
	}
}

func BenchmarkClient_PostJSON_Parallel(b *testing.B) {
	server := benchmarkJSONServer()
	defer server.Close()

	client := httpc.New(httpc.WithHTTPClient(server.Client()))
	body := map[string]string{"name": "gtkit"}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			var result struct {
				OK bool `json:"ok"`
			}
			status, err := client.PostJSON(b.Context(), server.URL, body, &result)
			if err != nil {
				b.Fatal(err)
			}
			if status != http.StatusOK || !result.OK {
				b.Fatalf("status=%d ok=%v", status, result.OK)
			}
		}
	})
}

func benchmarkJSONServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
}
