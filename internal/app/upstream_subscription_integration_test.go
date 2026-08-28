package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Kaniruka/NodeHarbor/internal/app"
)

func TestUserImportsURLUpstreamSubscriptionWithUserAgentAndAllProxyNodeFields(t *testing.T) {
	document := []byte("proxies:\n  - name: edge-hk\n    type: vless\n    server: example.test\n    port: 443\n    reality-opts:\n      public-key: preserved-key\n    client-fingerprint: chrome\n")
	upstream := &configuredUpstream{document: document}
	server := openApplicationServer(t, upstream)

	response := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{
		"name":      "Primary",
		"kind":      "url",
		"url":       "https://user:secret@example.test/subscription?token=visible",
		"userAgent": "Clash.Meta/1.19",
	})
	if response.StatusCode != http.StatusCreated {
		defer response.Body.Close()
		t.Fatalf("create status=%d", response.StatusCode)
	}
	_ = response.Body.Close()
	if upstream.request.Location != "https://user:secret@example.test/subscription?token=visible" || upstream.request.UserAgent != "Clash.Meta/1.19" {
		t.Fatalf("fetch request = %+v", upstream.request)
	}

	var subscriptions []struct {
		Name                   string `json:"name"`
		Kind                   string `json:"kind"`
		URL                    string `json:"url"`
		UserAgent              string `json:"userAgent"`
		LastSuccessfulDocument string `json:"lastSuccessfulDocument"`
		ProxyNodeCount         int    `json:"proxyNodeCount"`
		RefreshStatus          string `json:"refreshStatus"`
	}
	getJSON(t, server.URL+"/api/upstream-subscriptions", &subscriptions)
	if len(subscriptions) != 1 {
		t.Fatalf("subscription count=%d", len(subscriptions))
	}
	got := subscriptions[0]
	if got.URL != "https://user:secret@example.test/subscription?token=visible" || got.UserAgent != "Clash.Meta/1.19" {
		t.Fatalf("stored URL source = %+v", got)
	}
	if got.LastSuccessfulDocument != string(document) || got.ProxyNodeCount != 1 || got.RefreshStatus != "success" {
		t.Fatalf("stored successful document = %+v", got)
	}
	if !bytes.Contains([]byte(got.LastSuccessfulDocument), []byte("reality-opts")) || !bytes.Contains([]byte(got.LastSuccessfulDocument), []byte("client-fingerprint")) {
		t.Fatal("Proxy Node extension fields were not preserved")
	}
}

func TestUserImportsPastedUpstreamSubscriptionWithoutNetworkFetch(t *testing.T) {
	document := "proxies:\n  - name: pasted-node\n    type: ss\n    server: pasted.example\n    port: 443\n    plugin-opts:\n      mode: websocket\n"
	upstream := &configuredUpstream{err: errors.New("network adapter must not be called")}
	server := openApplicationServer(t, upstream)

	response := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{
		"name":     "Pasted",
		"kind":     "paste",
		"document": document,
	})
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("create pasted status=%d body=%q", response.StatusCode, body)
	}
	if upstream.request.Location != "" {
		t.Fatalf("paste unexpectedly fetched %q", upstream.request.Location)
	}
	var created struct {
		Kind                   string `json:"kind"`
		ConfiguredDocument     string `json:"configuredDocument"`
		LastSuccessfulDocument string `json:"lastSuccessfulDocument"`
	}
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Kind != "paste" || created.ConfiguredDocument != document || created.LastSuccessfulDocument != document {
		t.Fatalf("created pasted source = %+v", created)
	}
}

func TestUserImportsUploadedUpstreamSubscription(t *testing.T) {
	document := []byte("proxies:\n  - name: uploaded-node\n    type: trojan\n    server: upload.example\n    port: 443\n    password: visible-secret\n")
	server := openApplicationServer(t, &configuredUpstream{err: errors.New("network adapter must not be called")})
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err := form.WriteField("name", "Uploaded"); err != nil {
		t.Fatal(err)
	}
	file, err := form.CreateFormFile("file", "subscription.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(document); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}

	response, err := http.Post(server.URL+"/api/upstream-subscriptions", form.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		message, _ := io.ReadAll(response.Body)
		t.Fatalf("create upload status=%d body=%q", response.StatusCode, message)
	}
	var created struct {
		Kind                   string `json:"kind"`
		ConfiguredDocument     string `json:"configuredDocument"`
		LastSuccessfulDocument string `json:"lastSuccessfulDocument"`
	}
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Kind != "upload" || created.ConfiguredDocument != string(document) || created.LastSuccessfulDocument != string(document) {
		t.Fatalf("created uploaded source = %+v", created)
	}
}

func TestFailedURLRefreshPreservesLastSuccessfulDocumentAndRecordsReason(t *testing.T) {
	firstDocument := []byte("proxies:\n  - name: stable-node\n    type: ss\n    server: stable.example\n    port: 443\n")
	upstream := &configuredUpstream{document: firstDocument}
	server := openApplicationServer(t, upstream)
	createdResponse := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{
		"name": "Stable", "kind": "url", "url": "https://example.test/sub",
	})
	if createdResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create status=%d", createdResponse.StatusCode)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	_ = createdResponse.Body.Close()

	upstream.err = errors.New("connection timed out")
	refreshResponse, err := http.Post(server.URL+"/api/upstream-subscriptions/"+created.ID+"/refresh", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = refreshResponse.Body.Close()
	if refreshResponse.StatusCode != http.StatusBadGateway {
		t.Fatalf("refresh status=%d", refreshResponse.StatusCode)
	}

	var subscriptions []struct {
		LastSuccessfulDocument string `json:"lastSuccessfulDocument"`
		RefreshStatus          string `json:"refreshStatus"`
		LastError              string `json:"lastError"`
	}
	getJSON(t, server.URL+"/api/upstream-subscriptions", &subscriptions)
	got := subscriptions[0]
	if got.LastSuccessfulDocument != string(firstDocument) || got.RefreshStatus != "failed" || !strings.Contains(got.LastError, "connection timed out") {
		t.Fatalf("source after failed refresh = %+v", got)
	}
}

func TestInvalidOrEmptyRefreshPreservesLastSuccessfulDocument(t *testing.T) {
	cases := []struct {
		name     string
		document []byte
		error    string
	}{
		{name: "invalid YAML", document: []byte("proxies: ["), error: "invalid Clash/Mihomo YAML"},
		{name: "empty subscription", document: []byte("proxies: []\n"), error: "contains no Proxy Nodes"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			firstDocument := []byte("proxies:\n  - name: retained-node\n    type: ss\n    server: retained.example\n    port: 443\n")
			upstream := &configuredUpstream{document: firstDocument}
			server := openApplicationServer(t, upstream)
			createdResponse := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{
				"name": "Retained", "kind": "url", "url": "https://example.test/sub",
			})
			var created struct {
				ID string `json:"id"`
			}
			if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
				t.Fatal(err)
			}
			_ = createdResponse.Body.Close()

			upstream.document = testCase.document
			refreshResponse, err := http.Post(server.URL+"/api/upstream-subscriptions/"+created.ID+"/refresh", "application/json", nil)
			if err != nil {
				t.Fatal(err)
			}
			_ = refreshResponse.Body.Close()
			if refreshResponse.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("refresh status=%d", refreshResponse.StatusCode)
			}
			var subscriptions []struct {
				LastSuccessfulDocument string `json:"lastSuccessfulDocument"`
				LastError              string `json:"lastError"`
			}
			getJSON(t, server.URL+"/api/upstream-subscriptions", &subscriptions)
			if subscriptions[0].LastSuccessfulDocument != string(firstDocument) || !strings.Contains(subscriptions[0].LastError, testCase.error) {
				t.Fatalf("source after rejected refresh = %+v", subscriptions[0])
			}
		})
	}
}

func TestUserEditsPastedUpstreamSubscriptionThroughHTTP(t *testing.T) {
	server := openApplicationServer(t, &configuredUpstream{})
	createdResponse := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{
		"name": "Before", "kind": "paste", "document": "proxies:\n  - name: before\n    type: ss\n    server: before.example\n    port: 443\n",
	})
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	_ = createdResponse.Body.Close()
	afterDocument := "proxies:\n  - name: after\n    type: vless\n    server: after.example\n    port: 8443\n    custom-field: still-here\n"

	response := putJSONResponse(t, server.URL+"/api/upstream-subscriptions/"+created.ID, map[string]any{
		"name": "After", "document": afterDocument,
	})
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("edit status=%d body=%q", response.StatusCode, body)
	}
	var edited struct {
		Name                   string `json:"name"`
		ConfiguredDocument     string `json:"configuredDocument"`
		LastSuccessfulDocument string `json:"lastSuccessfulDocument"`
	}
	if err := json.NewDecoder(response.Body).Decode(&edited); err != nil {
		t.Fatal(err)
	}
	if edited.Name != "After" || edited.ConfiguredDocument != afterDocument || edited.LastSuccessfulDocument != afterDocument {
		t.Fatalf("edited source = %+v", edited)
	}
}

func TestUserDisablesUpstreamSubscription(t *testing.T) {
	server := openApplicationServer(t, &configuredUpstream{})
	createdResponse := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{
		"name": "Toggle", "kind": "paste", "document": "proxies:\n  - name: toggle\n    type: ss\n    server: toggle.example\n    port: 443\n",
	})
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	_ = createdResponse.Body.Close()

	body := bytes.NewBufferString(`{"enabled":false}`)
	request, err := http.NewRequest(http.MethodPatch, server.URL+"/api/upstream-subscriptions/"+created.ID, body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("disable status=%d", response.StatusCode)
	}
	var disabled struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(response.Body).Decode(&disabled); err != nil {
		t.Fatal(err)
	}
	if disabled.Enabled {
		t.Fatal("Upstream Subscription remains enabled")
	}
}

func TestUserDeletesUpstreamSubscription(t *testing.T) {
	server := openApplicationServer(t, &configuredUpstream{})
	createdResponse := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{
		"name": "Delete", "kind": "paste", "document": "proxies:\n  - name: delete\n    type: ss\n    server: delete.example\n    port: 443\n",
	})
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	_ = createdResponse.Body.Close()
	request, err := http.NewRequest(http.MethodDelete, server.URL+"/api/upstream-subscriptions/"+created.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status=%d", response.StatusCode)
	}
	var subscriptions []any
	getJSON(t, server.URL+"/api/upstream-subscriptions", &subscriptions)
	if len(subscriptions) != 0 {
		t.Fatalf("subscriptions after delete=%d", len(subscriptions))
	}
}

func TestEleventhUpstreamSubscriptionIsRejectedWithClearLimitMessage(t *testing.T) {
	server := openApplicationServer(t, &configuredUpstream{})
	for index := 0; index < 10; index++ {
		response := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{
			"name":     fmt.Sprintf("Source %d", index+1),
			"kind":     "paste",
			"document": fmt.Sprintf("proxies:\n  - name: node-%d\n    type: ss\n    server: node.example\n    port: 443\n", index+1),
		})
		_ = response.Body.Close()
		if response.StatusCode != http.StatusCreated {
			t.Fatalf("create %d status=%d", index+1, response.StatusCode)
		}
	}
	response := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{
		"name": "Source 11", "kind": "paste", "document": "proxies:\n  - name: node-11\n    type: ss\n    server: node.example\n    port: 443\n",
	})
	defer response.Body.Close()
	message, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusConflict || !bytes.Contains(message, []byte("10")) {
		t.Fatalf("eleventh create status=%d body=%q", response.StatusCode, message)
	}
}

func TestUpstreamSubscriptionAndRefreshStateSurviveApplicationRestart(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "nodeharbor.db")
	document := []byte("proxies:\n  - name: persistent-node\n    type: ss\n    server: persistent.example\n    port: 443\n")
	upstream := &configuredUpstream{document: document}
	firstApplication, firstServer := startApplication(t, databasePath, upstream)
	createdResponse := postJSONResponse(t, firstServer.URL+"/api/upstream-subscriptions", map[string]any{
		"name": "Persistent", "kind": "url", "url": "https://user:secret@example.test/sub", "userAgent": "Custom/1.0",
	})
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	_ = createdResponse.Body.Close()
	upstream.err = errors.New("temporary network failure")
	failedResponse, err := http.Post(firstServer.URL+"/api/upstream-subscriptions/"+created.ID+"/refresh", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = failedResponse.Body.Close()
	firstServer.Close()
	if err := firstApplication.Close(); err != nil {
		t.Fatal(err)
	}

	secondApplication, secondServer := startApplication(t, databasePath, &configuredUpstream{})
	defer secondServer.Close()
	defer secondApplication.Close()
	var subscriptions []struct {
		Name                   string `json:"name"`
		URL                    string `json:"url"`
		UserAgent              string `json:"userAgent"`
		LastSuccessfulDocument string `json:"lastSuccessfulDocument"`
		RefreshStatus          string `json:"refreshStatus"`
		LastError              string `json:"lastError"`
	}
	getJSON(t, secondServer.URL+"/api/upstream-subscriptions", &subscriptions)
	got := subscriptions[0]
	if got.Name != "Persistent" || got.URL != "https://user:secret@example.test/sub" || got.UserAgent != "Custom/1.0" ||
		got.LastSuccessfulDocument != string(document) || got.RefreshStatus != "failed" || !strings.Contains(got.LastError, "temporary network failure") {
		t.Fatalf("persisted source = %+v", got)
	}
}

func TestURLImportUsesRealHTTPAdapter(t *testing.T) {
	var receivedUserAgent string
	remote := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		receivedUserAgent = request.Header.Get("User-Agent")
		_, _ = response.Write([]byte("proxies:\n  - name: remote-node\n    type: ss\n    server: remote.example\n    port: 443\n"))
	}))
	defer remote.Close()
	server := openApplicationServer(t, app.NewHTTPUpstream(5*time.Second))
	response := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{
		"name": "Remote", "kind": "url", "url": remote.URL, "userAgent": "NodeHarbor-Test/1.0",
	})
	_ = response.Body.Close()
	if response.StatusCode != http.StatusCreated || receivedUserAgent != "NodeHarbor-Test/1.0" {
		t.Fatalf("create status=%d received User-Agent=%q", response.StatusCode, receivedUserAgent)
	}
}

type configuredUpstream struct {
	document []byte
	err      error
	request  app.UpstreamRequest
}

func (upstream *configuredUpstream) Fetch(_ context.Context, request app.UpstreamRequest) ([]byte, error) {
	upstream.request = request
	return upstream.document, upstream.err
}

func openApplicationServer(t *testing.T, upstream app.Upstream) *httptest.Server {
	t.Helper()
	instance, server := startApplication(t, filepath.Join(t.TempDir(), "nodeharbor.db"), upstream)
	t.Cleanup(server.Close)
	t.Cleanup(func() { _ = instance.Close() })
	return server
}

func startApplication(t *testing.T, databasePath string, upstream app.Upstream) (*app.Application, *httptest.Server) {
	t.Helper()
	assets := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte(`<div id="root"></div>`)}}
	instance, err := app.Open(context.Background(), app.Config{
		DatabasePath: databasePath,
		WebAssets:    fs.FS(assets),
	}, app.Dependencies{
		Upstream:    upstream,
		Scoring:     unavailableScoring{},
		Kernel:      &recordingKernel{},
		TestChannel: unavailableTestChannel{},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(instance.Handler())
	return instance, server
}

func postJSONResponse(t *testing.T, url string, value any) *http.Response {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func putJSONResponse(t *testing.T, url string, value any) *http.Response {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
