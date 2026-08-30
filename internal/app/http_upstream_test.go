package app

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestHTTPUpstreamDecodesBase64EncodedYAMLSubscription(t *testing.T) {
	const document = "proxies:\n  - name: encoded\n    type: ss\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(document))
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = response.Write([]byte(strings.TrimSpace(encoded)))
	}))
	defer server.Close()

	upstream := NewHTTPUpstream(time.Second)
	decoded, _, err := upstream.FetchWithMetadata(context.Background(), UpstreamRequest{Location: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != document {
		t.Fatalf("decoded document = %q, want %q", decoded, document)
	}
}

func TestHTTPUpstreamDecodesUnpaddedBase64WithLineBreaks(t *testing.T) {
	const document = "proxies:\n  - name: wrapped\n    type: trojan\n"
	encoded := base64.RawStdEncoding.EncodeToString([]byte(document))
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = response.Write([]byte(encoded[:len(encoded)/2] + "\n" + encoded[len(encoded)/2:]))
	}))
	defer server.Close()

	upstream := NewHTTPUpstream(time.Second)
	decoded, _, err := upstream.FetchWithMetadata(context.Background(), UpstreamRequest{Location: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != document {
		t.Fatalf("decoded document = %q, want %q", decoded, document)
	}
}

func TestHTTPUpstreamConvertsBase64ProxyURIListToYAML(t *testing.T) {
	const credentials = "aes-256-gcm:secret-password"
	encodedCredentials := base64.RawStdEncoding.EncodeToString([]byte(credentials))
	uriList := "ss://" + encodedCredentials + "@proxy.example:443#Encoded%20Node\n"
	encodedList := base64.StdEncoding.EncodeToString([]byte(uriList))
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = response.Write([]byte(encodedList))
	}))
	defer server.Close()

	upstream := NewHTTPUpstream(time.Second)
	decoded, _, err := upstream.FetchWithMetadata(context.Background(), UpstreamRequest{Location: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(decoded), "name: Encoded Node") || !strings.Contains(string(decoded), "type: ss") || !strings.Contains(string(decoded), "server: proxy.example") {
		t.Fatalf("converted document = %q", decoded)
	}
}
