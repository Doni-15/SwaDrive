package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCheckRequiresHTTP200(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		ok     bool
	}{
		{name: "healthy", status: http.StatusOK, ok: true},
		{name: "not healthy", status: http.StatusServiceUnavailable, ok: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
			}))
			defer server.Close()
			client := &http.Client{Timeout: time.Second}
			err := check(context.Background(), client, server.URL)
			if (err == nil) != test.ok {
				t.Fatalf("check() error = %v, want success=%t", err, test.ok)
			}
		})
	}
}
