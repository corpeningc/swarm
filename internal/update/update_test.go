package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewer(t *testing.T) {
	tests := []struct {
		current, latest string
		want            bool
	}{
		{"0.2.0", "0.2.1", true},
		{"0.2.0", "v0.2.1", true},
		{"0.9.9", "0.10.0", true},
		{"0.2.1", "0.2.1", false},
		{"0.2.2", "0.2.1", false},
		{"1.0.0", "0.9.9", false},
		{"0.2", "0.2.1", true},
		{"0.2.1-rc1", "0.2.1", true},
		{"0.2.1", "0.2.1-rc2", false},
		{DevVersion, "9.9.9", false}, // a local build is usually ahead
		{"", "9.9.9", false},
		{"0.2.0", "not-a-version", false},
	}
	for _, tt := range tests {
		if got := Newer(tt.current, tt.latest); got != tt.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tt.current, tt.latest, got, tt.want)
		}
	}
}

func TestLatest(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v0.3.0","html_url":"https://example.test/releases/v0.3.0"}`))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL}
	rel, err := c.Latest(context.Background(), "owner/repo")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if gotPath != "/repos/owner/repo/releases/latest" {
		t.Errorf("path = %q", gotPath)
	}
	if rel.Version != "0.3.0" {
		t.Errorf("Version = %q, want 0.3.0 (v stripped)", rel.Version)
	}
	if rel.URL != "https://example.test/releases/v0.3.0" {
		t.Errorf("URL = %q", rel.URL)
	}
}

func TestLatestErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"rate limited", http.StatusForbidden, `{}`},
		{"no releases", http.StatusNotFound, `{"message":"Not Found"}`},
		{"draft only", http.StatusOK, `{"tag_name":"v0.3.0","draft":true}`},
		{"garbage", http.StatusOK, `not json`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			if _, err := (&Client{BaseURL: srv.URL}).Latest(context.Background(), "o/r"); err == nil {
				t.Error("expected an error")
			}
		})
	}
}
