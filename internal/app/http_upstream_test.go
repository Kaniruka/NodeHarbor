package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTPUpstreamPreservesProviderReportedQuotaAndExpiry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("User-Agent") != "custom-agent" {
			t.Fatalf("User-Agent = %q, want custom-agent", request.Header.Get("User-Agent"))
		}
		response.Header().Set("subscription-userinfo", "upload=1024; download=2048; total=10000; expire=1798675200")
		_, _ = response.Write([]byte("proxies:\n  - name: one\n    type: ss\n"))
	}))
	defer server.Close()

	upstream := NewHTTPUpstream(time.Second)
	document, metadata, err := upstream.FetchWithMetadata(context.Background(), UpstreamRequest{Location: server.URL, UserAgent: "custom-agent"})
	if err != nil {
		t.Fatal(err)
	}
	if len(document) == 0 || metadata.UploadBytes != 1024 || metadata.DownloadBytes != 2048 || metadata.TotalBytes != 10000 {
		t.Fatalf("document/metadata = %q/%+v", document, metadata)
	}
	if metadata.ExpiresAt != "2026-12-31T00:00:00Z" {
		t.Fatalf("ExpiresAt = %q", metadata.ExpiresAt)
	}
}
