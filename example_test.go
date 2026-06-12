package httpc_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/gtkit/httpc"
)

func ExampleNew() {
	client := httpc.New(
		httpc.WithTimeout(5*time.Second),
		httpc.WithBaseURL("https://api.example.com"),
	)

	fmt.Println(client.HTTPClient().Timeout)
	// Output: 5s
}

func ExampleClient_PostJSON() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client := httpc.New(httpc.WithHTTPClient(server.Client()))
	var result struct {
		OK bool `json:"ok"`
	}
	status, err := client.PostJSON(context.Background(), server.URL, map[string]string{"name": "gtkit"}, &result)

	fmt.Println(status, result.OK, err == nil)
	// Output: 200 true true
}
