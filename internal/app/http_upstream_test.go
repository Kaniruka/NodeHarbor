package app

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
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

func TestHTTPUpstreamConvertsBase64Hysteria2URIToYAML(t *testing.T) {
	uriList := "hysteria2://secret-password@proxy.example:443/?sni=cdn.example&insecure=1#Fast%20Node\n"
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
	if !strings.Contains(string(decoded), "name: Fast Node") || !strings.Contains(string(decoded), "type: hysteria2") || !strings.Contains(string(decoded), "password: secret-password") || !strings.Contains(string(decoded), "skip-cert-verify: true") {
		t.Fatalf("converted document = %q", decoded)
	}
	var document struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(decoded, &document); err != nil {
		t.Fatalf("converted document is not valid YAML: %v", err)
	}
	if len(document.Proxies) != 1 {
		t.Fatalf("converted proxy count = %d, want 1", len(document.Proxies))
	}
}

func TestHTTPUpstreamConvertsBase64HysteriaURIToYAML(t *testing.T) {
	uriList := "hysteria://proxy.example:443?auth=legacy-secret&peer=cdn.example&insecure=1&upmbps=10&downmbps=50#Legacy%20Node\n"
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
	if !strings.Contains(string(decoded), "name: Legacy Node") || !strings.Contains(string(decoded), "type: hysteria") || !strings.Contains(string(decoded), "auth-str: legacy-secret") || !strings.Contains(string(decoded), "skip-cert-verify: true") {
		t.Fatalf("converted document = %q", decoded)
	}
}

func TestHTTPUpstreamConvertsTrojanURIWithPasswordOnlyUserinfo(t *testing.T) {
	uriList := "trojan://secret-password@proxy.example:8080?allowInsecure=1&sni=cdn.example&type=tcp#Trojan%20Node\n"
	encodedList := base64.StdEncoding.EncodeToString([]byte(uriList))
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = response.Write([]byte(encodedList))
	}))
	defer server.Close()

	decoded, _, err := NewHTTPUpstream(time.Second).FetchWithMetadata(context.Background(), UpstreamRequest{Location: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(decoded, &document); err != nil {
		t.Fatalf("converted document is not valid YAML: %v", err)
	}
	if len(document.Proxies) != 1 || document.Proxies[0]["password"] != "secret-password" || document.Proxies[0]["skip-cert-verify"] != true {
		t.Fatalf("converted proxy = %#v", document.Proxies)
	}
}

func TestHTTPUpstreamConvertsAnyTLSURIToYAML(t *testing.T) {
	uriList := "anytls://secret-password@proxy.example/?sni=cdn.example&insecure=1#AnyTLS%20Node\n"
	encodedList := base64.StdEncoding.EncodeToString([]byte(uriList))
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = response.Write([]byte(encodedList))
	}))
	defer server.Close()

	decoded, _, err := NewHTTPUpstream(time.Second).FetchWithMetadata(context.Background(), UpstreamRequest{Location: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(decoded, &document); err != nil {
		t.Fatalf("converted document is not valid YAML: %v", err)
	}
	if len(document.Proxies) != 1 || document.Proxies[0]["type"] != "anytls" || document.Proxies[0]["server"] != "proxy.example" || document.Proxies[0]["port"] != 443 || document.Proxies[0]["password"] != "secret-password" || document.Proxies[0]["sni"] != "cdn.example" || document.Proxies[0]["skip-cert-verify"] != true {
		t.Fatalf("converted proxy = %#v", document.Proxies)
	}
}
