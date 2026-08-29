package app_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Kaniruka/NodeHarbor/internal/app"
	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
)

func TestFreshApplicationReportsHealthyAndServesValidEmptyPublishedSubscription(t *testing.T) {
	kernel := &recordingKernel{}
	instance := openTestApplication(t, filepath.Join(t.TempDir(), "nodeharbor.db"), kernel)
	server := httptest.NewServer(instance.Handler())
	t.Cleanup(server.Close)

	var health struct {
		Status                string              `json:"status"`
		Backend               app.HealthComponent `json:"backend"`
		Database              app.HealthComponent `json:"database"`
		PublishedSubscription app.HealthComponent `json:"publishedSubscription"`
	}
	getJSON(t, server.URL+"/api/health", &health)
	if health.Status != "healthy" || health.Backend.Status != "healthy" || health.Database.Status != "healthy" || health.PublishedSubscription.Status != "healthy" {
		t.Fatalf("unexpected health response: %+v", health)
	}

	response, err := http.Get(server.URL + "/sub/clash.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("subscription status = %d", response.StatusCode)
	}
	published, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Proxies []map[string]any `yaml:"proxies"`
		Groups  []struct {
			Name    string   `yaml:"name"`
			Type    string   `yaml:"type"`
			Proxies []string `yaml:"proxies"`
		} `yaml:"proxy-groups"`
	}
	if err := yaml.Unmarshal(published, &document); err != nil {
		t.Fatalf("published subscription is not YAML: %v", err)
	}
	if document.Proxies == nil || len(document.Proxies) != 0 {
		t.Fatalf("expected an explicit empty proxy list, got %#v", document.Proxies)
	}
	if len(document.Groups) != 3 || document.Groups[0].Name != "AUTO" || document.Groups[1].Name != "FALLBACK" || document.Groups[2].Name != "SELECT" {
		t.Fatalf("unexpected generated groups: %#v", document.Groups)
	}
	if !bytes.Equal(kernel.validated, published) {
		t.Fatal("published subscription was not validated through the replaceable kernel seam")
	}
}

func TestSettingsAndSystemStateSurviveApplicationRestart(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "nodeharbor.db")
	first := openTestApplication(t, databasePath, &recordingKernel{})
	firstServer := httptest.NewServer(first.Handler())

	before := readSettings(t, firstServer.URL)
	putJSON(t, firstServer.URL+"/api/settings", map[string]string{"language": "en"})
	firstServer.Close()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second := openTestApplication(t, databasePath, &recordingKernel{})
	secondServer := httptest.NewServer(second.Handler())
	t.Cleanup(secondServer.Close)
	after := readSettings(t, secondServer.URL)
	if after.Language != "en" {
		t.Fatalf("language after restart = %q, want en", after.Language)
	}
	if after.InstallationID == "" || after.InstallationID != before.InstallationID {
		t.Fatalf("installation id did not persist: before=%q after=%q", before.InstallationID, after.InstallationID)
	}
}

func TestManagementPageIsServedByTheRealBackend(t *testing.T) {
	instance := openTestApplication(t, filepath.Join(t.TempDir(), "nodeharbor.db"), &recordingKernel{})
	server := httptest.NewServer(instance.Handler())
	t.Cleanup(server.Close)

	response, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(`<div id="root"></div>`)) {
		t.Fatalf("management page status=%d body=%q", response.StatusCode, body)
	}
}

func TestNonLoopbackCanReadOnlyHealthAndPublication(t *testing.T) {
	instance := openTestApplication(t, filepath.Join(t.TempDir(), "nodeharbor.db"), &recordingKernel{})
	for _, path := range []struct {
		path   string
		status int
	}{{"/api/health", http.StatusOK}, {"/sub/clash.yaml", http.StatusOK}, {"/api/settings", http.StatusForbidden}, {"/", http.StatusForbidden}} {
		request := httptest.NewRequest(http.MethodGet, "http://nodeharbor"+path.path, nil)
		request.RemoteAddr = "192.0.2.10:4000"
		response := httptest.NewRecorder()
		instance.Handler().ServeHTTP(response, request)
		if response.Code != path.status {
			t.Fatalf("%s status=%d want=%d", path.path, response.Code, path.status)
		}
	}
}

func TestBlackBoxEvaluationRequestTraversesReplaceableAdapters(t *testing.T) {
	upstream := &recordingUpstream{document: []byte("proxies:\n  - name: fixture-node\n    type: ss\n    server: example.test\n    port: 443\n")}
	kernel := &recordingKernel{}
	channel := &recordingTestChannel{result: app.ProbeResult{ExitIdentity: "203.0.113.7", Verified: true}}
	scoring := &recordingScoring{score: 84}
	assets := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte(`<div id="root"></div>`)}}
	instance, err := app.Open(context.Background(), app.Config{
		DatabasePath:        filepath.Join(t.TempDir(), "nodeharbor.db"),
		WebAssets:           fs.FS(assets),
		EnableTestEndpoints: true,
	}, app.Dependencies{Upstream: upstream, Scoring: scoring, Kernel: kernel, TestChannel: channel})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	server := httptest.NewServer(instance.Handler())
	t.Cleanup(server.Close)

	response, err := http.Post(server.URL+"/_test/evaluation", "application/json", bytes.NewBufferString(`{"upstream":"fixture://subscription"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("evaluation status=%d body=%q", response.StatusCode, body)
	}
	if upstream.request.Location != "fixture://subscription" || channel.node.Name != "fixture-node" || scoring.exitIdentity != "203.0.113.7" {
		t.Fatalf("adapters were not traversed: upstream=%q node=%q exit=%q", upstream.request.Location, channel.node.Name, scoring.exitIdentity)
	}
	if !bytes.Equal(kernel.validated, upstream.document) {
		t.Fatal("upstream document did not pass through the kernel adapter")
	}
}

func TestNodeValidationKeepsValidNodesWhenAnotherNodeFails(t *testing.T) {
	kernel := &nodeValidationKernel{}
	server := openTestApplicationWithKernel(t, &recordingUpstream{document: []byte("proxies:\n  - name: good\n    type: ss\n    server: good.example\n    port: 443\n  - name: bad\n    type: ss\n    server: bad.example\n    port: 443\n")}, kernel)
	response := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{"name": "My Source", "kind": "url", "url": "https://example.test/sub"})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create status=%d", response.StatusCode)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	var nodes []struct {
		Name, State, Reason string
		Rejection           struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"rejection"`
	}
	getJSON(t, server.URL+"/api/upstream-subscriptions/"+created.ID+"/nodes", &nodes)
	if len(nodes) != 2 || nodes[0].Name != "[My Source] good" || nodes[0].State != "accepted" || nodes[1].State != "rejected" || nodes[1].Rejection.Code != "validation_failed" || nodes[1].Rejection.Message != "unsupported protocol" || !strings.Contains(nodes[1].Reason, "validation_failed") {
		t.Fatalf("nodes=%+v", nodes)
	}
}

func TestProxyNodeIdentitySurvivesReorderedEquivalentRefreshAndCrossSourceNamesStayUnique(t *testing.T) {
	firstDocument := `proxies:
  - name: alpha
    type: vless
    server: alpha.example
    port: 443
    reality-opts:
      public-key: alpha-key
    client-fingerprint: chrome
  - name: beta
    type: trojan
    password: beta-secret
    server: beta.example
    port: 8443
`
	upstream := &configuredUpstream{document: []byte(firstDocument)}
	server := openApplicationServer(t, upstream)
	createdResponse := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{
		"name": "Shared", "kind": "url", "url": "https://first.example/sub",
	})
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	_ = createdResponse.Body.Close()

	firstNodes := readProxyNodes(t, server.URL, created.ID)
	identityByOriginalName := map[string]string{}
	fingerprintByOriginalName := map[string]string{}
	nameByOriginalName := map[string]string{}
	for _, node := range firstNodes {
		identityByOriginalName[node.OriginalName] = node.ID
		fingerprintByOriginalName[node.OriginalName] = node.Fingerprint
		nameByOriginalName[node.OriginalName] = node.Name
	}

	secondDocument := `proxies:
  - server: beta.example
    port: 8443
    password: beta-secret
    type: trojan
    name: beta
  - client-fingerprint: chrome
    reality-opts:
      public-key: alpha-key
    port: 443
    server: alpha.example
    type: vless
    name: alpha
`
	upstream.document = []byte(secondDocument)
	refreshResponse, err := http.Post(server.URL+"/api/upstream-subscriptions/"+created.ID+"/refresh", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if refreshResponse.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(refreshResponse.Body)
		_ = refreshResponse.Body.Close()
		t.Fatalf("refresh status=%d body=%q", refreshResponse.StatusCode, message)
	}
	_ = refreshResponse.Body.Close()

	for _, node := range readProxyNodes(t, server.URL, created.ID) {
		if node.ID != identityByOriginalName[node.OriginalName] || node.Fingerprint != fingerprintByOriginalName[node.OriginalName] || node.Name != nameByOriginalName[node.OriginalName] {
			t.Fatalf("node identity changed after reorder: %+v", node)
		}
		if node.Config["name"] != node.Name || node.Config["reality-opts"] == nil && node.OriginalName == "alpha" {
			t.Fatalf("node fields were not preserved: %+v", node)
		}
	}

	secondResponse := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{
		"name": "Shared", "kind": "paste", "document": "proxies:\n  - name: alpha\n    type: ss\n    server: another.example\n    port: 443\n",
	})
	if secondResponse.StatusCode != http.StatusCreated {
		message, _ := io.ReadAll(secondResponse.Body)
		_ = secondResponse.Body.Close()
		t.Fatalf("second source status=%d body=%q", secondResponse.StatusCode, message)
	}
	var second struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(secondResponse.Body).Decode(&second); err != nil {
		t.Fatal(err)
	}
	_ = secondResponse.Body.Close()

	allNames := map[string]bool{}
	for _, sourceID := range []string{created.ID, second.ID} {
		for _, node := range readProxyNodes(t, server.URL, sourceID) {
			if allNames[node.Name] {
				t.Fatalf("duplicate Proxy Node display name %q", node.Name)
			}
			allNames[node.Name] = true
			if !strings.HasPrefix(node.Name, "[Shared] "+node.OriginalName) {
				t.Fatalf("display name lost source prefix: %q", node.Name)
			}
		}
	}
}

func TestProxyNodeNamesRemainStableWhenAnotherSourceIntroducesACollision(t *testing.T) {
	upstream := &configuredUpstream{document: []byte("proxies:\n  - name: alpha\n    type: ss\n    server: first.example\n    port: 443\n")}
	server := openApplicationServer(t, upstream)
	firstResponse := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{
		"name": "Shared", "kind": "url", "url": "https://first.example/sub",
	})
	var first struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(firstResponse.Body).Decode(&first); err != nil {
		t.Fatal(err)
	}
	_ = firstResponse.Body.Close()
	firstNodes := readProxyNodes(t, server.URL, first.ID)
	if len(firstNodes) != 1 {
		t.Fatalf("first nodes=%+v", firstNodes)
	}
	firstName := firstNodes[0].Name

	upstream.document = []byte("proxies:\n  - name: alpha\n    type: ss\n    server: second.example\n    port: 443\n")
	secondResponse := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{
		"name": "Shared", "kind": "url", "url": "https://second.example/sub",
	})
	var second struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(secondResponse.Body).Decode(&second); err != nil {
		t.Fatal(err)
	}
	_ = secondResponse.Body.Close()

	updatedFirstNodes := readProxyNodes(t, server.URL, first.ID)
	secondNodes := readProxyNodes(t, server.URL, second.ID)
	if len(updatedFirstNodes) != 1 || updatedFirstNodes[0].Name != firstName {
		t.Fatalf("existing Proxy Node name changed from %q: %+v", firstName, updatedFirstNodes)
	}
	if len(secondNodes) != 1 || secondNodes[0].Name == firstName || !strings.HasPrefix(secondNodes[0].Name, "[Shared] alpha") {
		t.Fatalf("colliding Proxy Node name=%+v", secondNodes)
	}
	if len(secondNodes[0].Name) > len("[Shared] alpha (12345678)") {
		t.Fatalf("collision suffix is not short: %q", secondNodes[0].Name)
	}
	deleteRequest, err := http.NewRequest(http.MethodDelete, server.URL+"/api/upstream-subscriptions/"+first.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	deleteResponse, err := http.DefaultClient.Do(deleteRequest)
	if err != nil {
		t.Fatal(err)
	}
	_ = deleteResponse.Body.Close()
	if deleteResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status=%d", deleteResponse.StatusCode)
	}
	remainingNodes := readProxyNodes(t, server.URL, second.ID)
	if len(remainingNodes) != 1 || remainingNodes[0].Name != "[Shared] alpha" {
		t.Fatalf("surviving node kept an unnecessary collision suffix: %+v", remainingNodes)
	}
}

type proxyNodeResponse struct {
	ID           string         `json:"id"`
	Fingerprint  string         `json:"fingerprint"`
	Name         string         `json:"name"`
	OriginalName string         `json:"originalName"`
	Config       map[string]any `json:"config"`
}

func readProxyNodes(t *testing.T, baseURL, subscriptionID string) []proxyNodeResponse {
	t.Helper()
	var nodes []proxyNodeResponse
	getJSON(t, baseURL+"/api/upstream-subscriptions/"+subscriptionID+"/nodes", &nodes)
	return nodes
}

func TestEvaluationRunAppliesAvailabilityRulesAndFailsClosed(t *testing.T) {
	channel := &availabilityChannel{verified: true, latencies: []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 300 * time.Millisecond}}
	server := openEvaluationApplication(t, &recordingUpstream{document: []byte("proxies:\n  - name: available\n    type: ss\n    server: good.example\n    port: 443\n")}, &nodeValidationKernel{}, channel)
	response := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{"name": "Eval", "kind": "url", "url": "https://example.test/sub"})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create status=%d", response.StatusCode)
	}
	var source struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(response.Body).Decode(&source)
	_ = response.Body.Close()
	start := postJSONResponse(t, server.URL+"/api/evaluation-runs", map[string]any{})
	if start.StatusCode != http.StatusAccepted {
		t.Fatalf("start status=%d", start.StatusCode)
	}
	_ = start.Body.Close()
	var run struct {
		Status  string `json:"status"`
		Passed  int    `json:"passed"`
		Results []struct {
			State  string  `json:"state"`
			Median float64 `json:"medianLatencyMs"`
		} `json:"results"`
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		getJSON(t, server.URL+"/api/evaluation-runs/current", &run)
		if run.Status != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if run.Status != "completed" || run.Passed != 1 || len(run.Results) != 1 || run.Results[0].State != "passed" || run.Results[0].Median != 200 {
		t.Fatalf("run=%+v", run)
	}
	_ = source
}

func TestEvaluationPrefersIPv4ExitIdentityFromTestChannel(t *testing.T) {
	channel := &identityChannel{attempt: app.AvailabilityAttempt{
		Success:  true,
		Verified: true,
		Latency:  100 * time.Millisecond,
		ExitIdentities: []app.ExitIdentityCandidate{
			{IP: "2001:db8::1", Verified: true},
			{IP: "198.51.100.7", Verified: true},
		},
	}}
	scoring := &recordingScoring{score: 84}
	server := openEvaluationApplicationWithScoring(t, &recordingUpstream{document: []byte("proxies:\n  - name: dual-stack\n    type: ss\n    server: example.test\n    port: 443\n")}, &nodeValidationKernel{}, channel, scoring)
	putJSON(t, server.URL+"/api/settings", map[string]any{"availabilityAttempts": 1, "availabilityRequiredSuccesses": 1, "availabilityURLs": []string{"https://probe.example/204"}, "evaluationWorkerCount": 1, "scoringJitterMs": 0})
	response := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{"name": "Eval", "kind": "url", "url": "https://example.test/sub"})
	_ = response.Body.Close()
	start := postJSONResponse(t, server.URL+"/api/evaluation-runs", map[string]any{})
	_ = start.Body.Close()
	var run struct {
		Status  string `json:"status"`
		Results []struct {
			State         string `json:"state"`
			ExitIdentity  string `json:"exitIdentity"`
			AddressFamily string `json:"addressFamily"`
		} `json:"results"`
	}
	waitForEvaluationRun(t, server.URL)
	getJSON(t, server.URL+"/api/evaluation-runs/current", &run)
	if run.Status != "completed" || len(run.Results) != 1 || run.Results[0].State != "passed" || run.Results[0].ExitIdentity != "198.51.100.7" || run.Results[0].AddressFamily != "ipv4" || scoring.exitIdentity != "198.51.100.7" {
		t.Fatalf("run=%+v scored=%q", run, scoring.exitIdentity)
	}
}

func TestEvaluationFallsBackToIPv6ExitIdentityWhenIPv4IsUnavailable(t *testing.T) {
	channel := &identityChannel{attempt: app.AvailabilityAttempt{
		Success:  true,
		Verified: true,
		Latency:  100 * time.Millisecond,
		ExitIdentities: []app.ExitIdentityCandidate{
			{IP: "2001:db8::2", Verified: true},
		},
	}}
	scoring := &recordingScoring{score: 84}
	server := openEvaluationApplicationWithScoring(t, &recordingUpstream{document: []byte("proxies:\n  - name: ipv6-only\n    type: ss\n    server: example.test\n    port: 443\n")}, &nodeValidationKernel{}, channel, scoring)
	putJSON(t, server.URL+"/api/settings", map[string]any{"availabilityAttempts": 1, "availabilityRequiredSuccesses": 1, "availabilityURLs": []string{"https://probe.example/204"}, "evaluationWorkerCount": 1, "scoringJitterMs": 0})
	response := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{"name": "Eval", "kind": "url", "url": "https://example.test/sub"})
	_ = response.Body.Close()
	start := postJSONResponse(t, server.URL+"/api/evaluation-runs", map[string]any{})
	_ = start.Body.Close()
	var run struct {
		Status  string `json:"status"`
		Results []struct {
			State         string `json:"state"`
			ExitIdentity  string `json:"exitIdentity"`
			AddressFamily string `json:"addressFamily"`
		} `json:"results"`
	}
	waitForEvaluationRun(t, server.URL)
	getJSON(t, server.URL+"/api/evaluation-runs/current", &run)
	if run.Status != "completed" || len(run.Results) != 1 || run.Results[0].State != "passed" || run.Results[0].ExitIdentity != "2001:db8::2" || run.Results[0].AddressFamily != "ipv6" || scoring.exitIdentity != "2001:db8::2" {
		t.Fatalf("run=%+v scored=%q", run, scoring.exitIdentity)
	}
}

func TestEvaluationRequestsIPv4BeforeFallingBackToIPv6(t *testing.T) {
	channel := &familyIdentityChannel{fallback: "2001:db8::3"}
	scoring := &recordingScoring{score: 84}
	server := openEvaluationApplicationWithScoring(t, &recordingUpstream{document: []byte("proxies:\n  - name: family-order\n    type: ss\n    server: example.test\n    port: 443\n")}, &nodeValidationKernel{}, channel, scoring)
	putJSON(t, server.URL+"/api/settings", map[string]any{"availabilityAttempts": 1, "availabilityRequiredSuccesses": 1, "availabilityURLs": []string{"https://probe.example/204"}, "evaluationWorkerCount": 1, "scoringJitterMs": 0})
	response := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{"name": "Eval", "kind": "url", "url": "https://example.test/sub"})
	_ = response.Body.Close()
	var run struct {
		Status  string `json:"status"`
		Results []struct {
			ExitIdentity  string `json:"exitIdentity"`
			AddressFamily string `json:"addressFamily"`
		} `json:"results"`
	}
	runEvaluation(t, server.URL, map[string]any{})
	getJSON(t, server.URL+"/api/evaluation-runs/current", &run)
	if run.Status != "completed" || len(channel.families) != 2 || channel.families[0] != "ipv4" || channel.families[1] != "ipv6" || len(run.Results) != 1 || run.Results[0].ExitIdentity != channel.fallback || run.Results[0].AddressFamily != "ipv6" {
		t.Fatalf("run=%+v families=%v", run, channel.families)
	}
}

func TestEvaluationRejectsUnboundScoringProvider(t *testing.T) {
	channel := &availabilityChannel{verified: true, latencies: []time.Duration{100 * time.Millisecond}}
	scoring := &unboundScoring{}
	server := openEvaluationApplicationWithScoring(t, &recordingUpstream{document: []byte("proxies:\n  - name: unbound\n    type: ss\n    server: example.test\n    port: 443\n")}, &nodeValidationKernel{}, channel, scoring)
	putJSON(t, server.URL+"/api/settings", map[string]any{"availabilityAttempts": 1, "availabilityRequiredSuccesses": 1, "availabilityURLs": []string{"https://probe.example/204"}, "evaluationWorkerCount": 1, "scoringJitterMs": 0})
	response := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{"name": "Eval", "kind": "url", "url": "https://example.test/sub"})
	_ = response.Body.Close()
	var run struct {
		Status  string `json:"status"`
		Results []struct {
			State  string `json:"state"`
			Reason string `json:"reason"`
		} `json:"results"`
	}
	runEvaluation(t, server.URL, map[string]any{})
	getJSON(t, server.URL+"/api/evaluation-runs/current", &run)
	if run.Status != "completed" || len(run.Results) != 1 || run.Results[0].State != "failed" || !strings.HasPrefix(run.Results[0].Reason, "score_unavailable:") || scoring.calls != 0 {
		t.Fatalf("run=%+v calls=%d", run, scoring.calls)
	}
}

func TestDefaultAssemblyUsesIPLarkThroughTheVerifiedTestChannel(t *testing.T) {
	var directRequests, channelRequests int
	directServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directRequests++
		_, _ = w.Write([]byte(`{"status":"success","data":{"ip_score":12}}`))
	}))
	defer directServer.Close()
	channelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		channelRequests++
		if r.URL.Query().Get("ip") != "198.51.100.77" {
			t.Errorf("channel fixture ip=%q", r.URL.Query().Get("ip"))
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"ip_score":88}}`))
	}))
	defer channelServer.Close()

	channel := &iplarkFixtureChannel{destination: channelServer.URL}
	dependencies := app.DefaultDependencies(&nodeValidationKernel{})
	provider, ok := dependencies.Scoring.(app.IPLarkProvider)
	if !ok {
		t.Fatalf("default scoring provider=%T, want app.IPLarkProvider", dependencies.Scoring)
	}
	provider.Client = directServer.Client()
	provider.Endpoint = directServer.URL + "/ipscore"
	dependencies.Scoring = provider
	dependencies.ScoringProviders["iplark"] = provider
	dependencies.Upstream = &recordingUpstream{document: []byte("proxies:\n  - name: production-assembly\n    type: ss\n    server: example.test\n    port: 443\n")}
	dependencies.TestChannel = channel
	assets := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte(`<!doctype html><div id="root"></div>`)}}
	instance, err := app.Open(context.Background(), app.Config{DatabasePath: filepath.Join(t.TempDir(), "nodeharbor.db"), WebAssets: fs.FS(assets)}, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	server := httptest.NewServer(instance.Handler())
	defer server.Close()

	putJSON(t, server.URL+"/api/settings", map[string]any{"availabilityAttempts": 1, "availabilityRequiredSuccesses": 1, "availabilityURLs": []string{"https://probe.example/204"}, "evaluationWorkerCount": 1, "scoringJitterMs": 0})
	response := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{"name": "Eval", "kind": "url", "url": "https://example.test/sub"})
	_ = response.Body.Close()
	runEvaluation(t, server.URL, map[string]any{})

	var run struct {
		Status  string `json:"status"`
		Results []struct {
			State         string   `json:"state"`
			IPScore       *float64 `json:"ipScore"`
			AddressFamily string   `json:"addressFamily"`
			ScoreSource   string   `json:"scoreSource"`
			Reason        string   `json:"reason"`
		} `json:"results"`
	}
	getJSON(t, server.URL+"/api/evaluation-runs/current", &run)
	if run.Status != "completed" || len(run.Results) != 1 || run.Results[0].State != "passed" || run.Results[0].IPScore == nil || *run.Results[0].IPScore != 88 || run.Results[0].AddressFamily != "ipv4" || run.Results[0].ScoreSource != "provider" || run.Results[0].Reason != "" {
		t.Fatalf("run=%+v", run)
	}
	if directRequests != 0 || channelRequests != 1 || channel.httpClientCalls != 1 {
		t.Fatalf("direct requests=%d channel requests=%d channel clients=%d", directRequests, channelRequests, channel.httpClientCalls)
	}
}

func TestIPCheckProviderUsesTheVerifiedTestChannel(t *testing.T) {
	var requests int
	channelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Query().Get("ip") != "198.51.100.77" {
			t.Errorf("channel fixture ip=%q", r.URL.Query().Get("ip"))
		}
		_, _ = w.Write([]byte(`{"score":88,"provider":"ipcheck-fixture"}`))
	}))
	defer channelServer.Close()

	channel := &iplarkFixtureChannel{destination: channelServer.URL}
	dependencies := app.DefaultDependencies(&nodeValidationKernel{})
	provider, ok := dependencies.ScoringProviders["ipcheck"].(app.IPCheckProvider)
	if !ok {
		t.Fatalf("IPCheck provider=%T", dependencies.ScoringProviders["ipcheck"])
	}
	provider.Endpoint = channelServer.URL + "/score"
	dependencies.ScoringProviders["ipcheck"] = provider
	dependencies.Upstream = &recordingUpstream{document: []byte("proxies:\n  - name: ipcheck-production-assembly\n    type: ss\n    server: example.test\n    port: 443\n")}
	dependencies.TestChannel = channel
	assets := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte(`<!doctype html><div id="root"></div>`)}}
	instance, err := app.Open(context.Background(), app.Config{DatabasePath: filepath.Join(t.TempDir(), "nodeharbor.db"), WebAssets: fs.FS(assets)}, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	server := httptest.NewServer(instance.Handler())
	defer server.Close()

	putJSON(t, server.URL+"/api/settings", map[string]any{"scoringProvider": "ipcheck", "availabilityAttempts": 1, "availabilityRequiredSuccesses": 1, "availabilityURLs": []string{"https://probe.example/204"}, "evaluationWorkerCount": 1, "scoringJitterMs": 0})
	response := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{"name": "Eval", "kind": "url", "url": "https://example.test/sub"})
	_ = response.Body.Close()
	runEvaluation(t, server.URL, map[string]any{})

	var run struct {
		Results []struct {
			State string   `json:"state"`
			Score *float64 `json:"ipScore"`
		} `json:"results"`
	}
	getJSON(t, server.URL+"/api/evaluation-runs/current", &run)
	if len(run.Results) != 1 || run.Results[0].State != "passed" || run.Results[0].Score == nil || *run.Results[0].Score != 88 || requests != 1 || channel.httpClientCalls != 1 {
		t.Fatalf("run=%+v requests=%d channel clients=%d", run, requests, channel.httpClientCalls)
	}
}

func TestEvaluationRejectsUnverifiedExitIdentityCandidate(t *testing.T) {
	channel := &identityChannel{attempt: app.AvailabilityAttempt{
		Success:        true,
		Verified:       true,
		Latency:        100 * time.Millisecond,
		ExitIdentities: []app.ExitIdentityCandidate{{IP: "198.51.100.14", Verified: false}},
	}}
	scoring := &countingScoring{}
	server := openEvaluationApplicationWithScoring(t, &recordingUpstream{document: []byte("proxies:\n  - name: unverified-exit\n    type: ss\n    server: example.test\n    port: 443\n")}, &nodeValidationKernel{}, channel, scoring)
	putJSON(t, server.URL+"/api/settings", map[string]any{"availabilityAttempts": 1, "availabilityRequiredSuccesses": 1, "availabilityURLs": []string{"https://probe.example/204"}, "evaluationWorkerCount": 1, "scoringJitterMs": 0})
	response := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{"name": "Eval", "kind": "url", "url": "https://example.test/sub"})
	_ = response.Body.Close()
	var run struct {
		Results []struct {
			State  string `json:"state"`
			Reason string `json:"reason"`
		} `json:"results"`
	}
	runEvaluation(t, server.URL, map[string]any{})
	getJSON(t, server.URL+"/api/evaluation-runs/current", &run)
	if len(run.Results) != 1 || run.Results[0].State != "failed" || !strings.HasPrefix(run.Results[0].Reason, "test_channel_unverified:") || scoring.callCount() != 0 {
		t.Fatalf("unverified candidate result=%+v calls=%d", run.Results, scoring.callCount())
	}
}

func TestIgnoreCacheForcesNewScoreRequest(t *testing.T) {
	channel := &identityChannel{attempt: app.AvailabilityAttempt{
		Success:        true,
		Verified:       true,
		Latency:        100 * time.Millisecond,
		ExitIdentities: []app.ExitIdentityCandidate{{IP: "198.51.100.8", Verified: true}},
	}}
	scoring := &countingScoring{}
	server := openEvaluationApplicationWithScoring(t, &recordingUpstream{document: []byte("proxies:\n  - name: cacheable\n    type: ss\n    server: example.test\n    port: 443\n")}, &nodeValidationKernel{}, channel, scoring)
	putJSON(t, server.URL+"/api/settings", map[string]any{"availabilityAttempts": 1, "availabilityRequiredSuccesses": 1, "availabilityURLs": []string{"https://probe.example/204"}, "evaluationWorkerCount": 1, "scoringJitterMs": 0})
	response := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{"name": "Eval", "kind": "url", "url": "https://example.test/sub"})
	_ = response.Body.Close()
	runEvaluation(t, server.URL, map[string]any{})
	runEvaluation(t, server.URL, map[string]any{})
	if scoring.callCount() != 1 {
		t.Fatalf("default cache calls=%d", scoring.callCount())
	}
	runEvaluation(t, server.URL, map[string]any{"ignoreCache": true})
	if scoring.callCount() != 2 {
		t.Fatalf("ignore-cache calls=%d", scoring.callCount())
	}
}

func TestExpiredScoreCacheEntryForcesNewScoreRequest(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "nodeharbor.db")
	channel := &identityChannel{attempt: app.AvailabilityAttempt{
		Success:        true,
		Verified:       true,
		Latency:        100 * time.Millisecond,
		ExitIdentities: []app.ExitIdentityCandidate{{IP: "198.51.100.9", Verified: true}},
	}}
	scoring := &countingScoring{}
	server := openEvaluationApplicationWithScoringAtPath(t, databasePath, &recordingUpstream{document: []byte("proxies:\n  - name: expired\n    type: ss\n    server: example.test\n    port: 443\n")}, &nodeValidationKernel{}, channel, scoring)
	putJSON(t, server.URL+"/api/settings", map[string]any{"availabilityAttempts": 1, "availabilityRequiredSuccesses": 1, "availabilityURLs": []string{"https://probe.example/204"}, "evaluationWorkerCount": 1, "scoringJitterMs": 0})
	response := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{"name": "Eval", "kind": "url", "url": "https://example.test/sub"})
	_ = response.Body.Close()
	runEvaluation(t, server.URL, map[string]any{})
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.Exec(`UPDATE score_cache SET scored_at = ?`, time.Now().UTC().Add(-25*time.Hour).Format(time.RFC3339Nano))
	_ = database.Close()
	if err != nil {
		t.Fatal(err)
	}
	runEvaluation(t, server.URL, map[string]any{})
	if scoring.callCount() != 2 {
		t.Fatalf("expired cache calls=%d", scoring.callCount())
	}
}

func TestScoreCacheIsScopedToProviderAndExitIdentity(t *testing.T) {
	channel := &identityChannel{attempt: app.AvailabilityAttempt{
		Success:        true,
		Verified:       true,
		Latency:        100 * time.Millisecond,
		ExitIdentities: []app.ExitIdentityCandidate{{IP: "198.51.100.10", Verified: true}},
	}}
	iplark := &namedScoring{name: "iplark", score: 80}
	ipcheck := &namedScoring{name: "ipcheck", score: 80}
	server := openEvaluationApplicationWithProviders(t, &recordingUpstream{document: []byte("proxies:\n  - name: scoped-cache\n    type: ss\n    server: example.test\n    port: 443\n")}, &nodeValidationKernel{}, channel, iplark, map[string]app.ScoringProvider{"iplark": iplark, "ipcheck": ipcheck})
	putJSON(t, server.URL+"/api/settings", map[string]any{"availabilityAttempts": 1, "availabilityRequiredSuccesses": 1, "availabilityURLs": []string{"https://probe.example/204"}, "evaluationWorkerCount": 1, "scoringJitterMs": 0})
	response := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{"name": "Eval", "kind": "url", "url": "https://example.test/sub"})
	_ = response.Body.Close()
	runEvaluation(t, server.URL, map[string]any{})
	channel.attempt.ExitIdentities[0].IP = "198.51.100.11"
	runEvaluation(t, server.URL, map[string]any{})
	if iplark.callCount() != 2 {
		t.Fatalf("different Exit Identities shared cache: calls=%d", iplark.callCount())
	}
	channel.attempt.ExitIdentities[0].IP = "198.51.100.10"
	putJSON(t, server.URL+"/api/settings", map[string]any{"scoringProvider": "ipcheck"})
	runEvaluation(t, server.URL, map[string]any{})
	if ipcheck.callCount() != 1 {
		t.Fatalf("different Scoring Providers shared cache: calls=%d", ipcheck.callCount())
	}
	putJSON(t, server.URL+"/api/settings", map[string]any{"scoringProvider": "iplark"})
	runEvaluation(t, server.URL, map[string]any{})
	if iplark.callCount() != 2 || ipcheck.callCount() != 1 {
		t.Fatalf("provider cache reuse was not isolated: iplark=%d ipcheck=%d", iplark.callCount(), ipcheck.callCount())
	}
}

func TestScoringProviderRegistryExposesIndependentEnabledAndFailureState(t *testing.T) {
	iplark := &namedScoring{name: "iplark", score: 80}
	ipcheck := &namedScoring{name: "ipcheck", score: 80}
	server := openEvaluationApplicationWithProviders(t, &recordingUpstream{document: []byte("proxies: []\n")}, &nodeValidationKernel{}, &identityChannel{}, iplark, map[string]app.ScoringProvider{"iplark": iplark, "ipcheck": ipcheck})

	var settings struct {
		ScoringProviders []struct {
			Name          string `json:"name"`
			Enabled       bool   `json:"enabled"`
			FailureStatus string `json:"failureStatus"`
		} `json:"scoringProviders"`
	}
	getJSON(t, server.URL+"/api/settings", &settings)
	if len(settings.ScoringProviders) != 2 || settings.ScoringProviders[0].Name != "iplark" || !settings.ScoringProviders[0].Enabled || settings.ScoringProviders[1].Name != "ipcheck" || !settings.ScoringProviders[1].Enabled || settings.ScoringProviders[1].FailureStatus != "" {
		t.Fatalf("initial provider settings=%+v", settings.ScoringProviders)
	}

	putJSON(t, server.URL+"/api/settings", map[string]any{"ipcheckEnabled": false})
	getJSON(t, server.URL+"/api/settings", &settings)
	if settings.ScoringProviders[1].Enabled {
		t.Fatalf("disabled IPCheck.ing was reported enabled: %+v", settings.ScoringProviders)
	}
}

func TestScoringProviderFailureStatusDistinguishesOutageFromLowScore(t *testing.T) {
	channel := &identityChannel{attempt: app.AvailabilityAttempt{Success: true, Verified: true, Latency: 100 * time.Millisecond, ExitIdentities: []app.ExitIdentityCandidate{{IP: "198.51.100.20", Verified: true}}}}
	iplark := &namedScoring{name: "iplark", score: 80}
	ipcheck := &namedScoring{name: "ipcheck", score: 80, err: errors.New("fixture provider outage")}
	server := openEvaluationApplicationWithProviders(t, &recordingUpstream{document: []byte("proxies:\n  - name: provider-status\n    type: ss\n    server: example.test\n    port: 443\n")}, &nodeValidationKernel{}, channel, iplark, map[string]app.ScoringProvider{"iplark": iplark, "ipcheck": ipcheck})
	putJSON(t, server.URL+"/api/settings", map[string]any{"scoringProvider": "ipcheck", "availabilityAttempts": 1, "availabilityRequiredSuccesses": 1, "availabilityURLs": []string{"https://probe.example/204"}, "evaluationWorkerCount": 1, "scoringJitterMs": 0})
	response := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{"name": "Eval", "kind": "url", "url": "https://example.test/sub"})
	_ = response.Body.Close()
	runEvaluation(t, server.URL, map[string]any{})

	var run struct {
		Results []struct {
			Reason string `json:"reason"`
		} `json:"results"`
	}
	getJSON(t, server.URL+"/api/evaluation-runs/current", &run)
	if len(run.Results) != 1 || !strings.HasPrefix(run.Results[0].Reason, "score_unavailable:") {
		t.Fatalf("provider failure result=%+v", run.Results)
	}
	var settings struct {
		ScoringProviders []struct {
			Name          string `json:"name"`
			FailureStatus string `json:"failureStatus"`
		} `json:"scoringProviders"`
	}
	getJSON(t, server.URL+"/api/settings", &settings)
	if len(settings.ScoringProviders) != 2 || settings.ScoringProviders[1].Name != "ipcheck" || settings.ScoringProviders[1].FailureStatus != "fixture provider outage" {
		t.Fatalf("provider diagnostics=%+v", settings.ScoringProviders)
	}

}

func TestSelectedScoringProviderUsesItsOwnThreshold(t *testing.T) {
	channel := &identityChannel{attempt: app.AvailabilityAttempt{Success: true, Verified: true, Latency: 100 * time.Millisecond, ExitIdentities: []app.ExitIdentityCandidate{{IP: "198.51.100.21", Verified: true}}}}
	iplark := &namedScoring{name: "iplark", score: 80}
	ipcheck := &namedScoring{name: "ipcheck", score: 80}
	server := openEvaluationApplicationWithProviders(t, &recordingUpstream{document: []byte("proxies:\n  - name: provider-threshold\n    type: ss\n    server: example.test\n    port: 443\n")}, &nodeValidationKernel{}, channel, iplark, map[string]app.ScoringProvider{"iplark": iplark, "ipcheck": ipcheck})
	putJSON(t, server.URL+"/api/settings", map[string]any{"scoringProvider": "ipcheck", "iplarkThreshold": 10, "ipcheckThreshold": 90, "availabilityAttempts": 1, "availabilityRequiredSuccesses": 1, "availabilityURLs": []string{"https://probe.example/204"}, "evaluationWorkerCount": 1, "scoringJitterMs": 0})
	response := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{"name": "Eval", "kind": "url", "url": "https://example.test/sub"})
	_ = response.Body.Close()
	runEvaluation(t, server.URL, map[string]any{})
	var run struct {
		Results []struct {
			State  string `json:"state"`
			Reason string `json:"reason"`
		} `json:"results"`
	}
	getJSON(t, server.URL+"/api/evaluation-runs/current", &run)
	if len(run.Results) != 1 || run.Results[0].State != "failed" || !strings.Contains(run.Results[0].Reason, "threshold 90") {
		t.Fatalf("IPCheck.ing threshold result=%+v", run.Results)
	}

	putJSON(t, server.URL+"/api/settings", map[string]any{"scoringProvider": "iplark"})
	runEvaluation(t, server.URL, map[string]any{})
	getJSON(t, server.URL+"/api/evaluation-runs/current", &run)
	if len(run.Results) != 1 || run.Results[0].State != "passed" {
		t.Fatalf("IPLark threshold result=%+v", run.Results)
	}
}

func TestSharedExitIdentityReusesScoreAndKeepsAllQualifiedProxyNodes(t *testing.T) {
	channel := &identityChannel{attempt: app.AvailabilityAttempt{
		Success:        true,
		Verified:       true,
		Latency:        100 * time.Millisecond,
		ExitIdentities: []app.ExitIdentityCandidate{{IP: "198.51.100.12", Verified: true}},
	}}
	scoring := &countingScoring{}
	server := openEvaluationApplicationWithScoring(t, &recordingUpstream{document: []byte("proxies:\n  - name: shared-one\n    type: ss\n    server: one.example\n    port: 443\n  - name: shared-two\n    type: ss\n    server: two.example\n    port: 443\n")}, &nodeValidationKernel{}, channel, scoring)
	putJSON(t, server.URL+"/api/settings", map[string]any{"availabilityAttempts": 1, "availabilityRequiredSuccesses": 1, "availabilityURLs": []string{"https://probe.example/204"}, "evaluationWorkerCount": 2, "scoringJitterMs": 0})
	response := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{"name": "Eval", "kind": "url", "url": "https://example.test/sub"})
	_ = response.Body.Close()
	runEvaluation(t, server.URL, map[string]any{"ignoreCache": true})
	if scoring.callCount() != 1 {
		t.Fatalf("shared Exit Identity was scored %d times", scoring.callCount())
	}
	publicationResponse, err := http.Get(server.URL + "/sub/clash.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer publicationResponse.Body.Close()
	var publication struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.NewDecoder(publicationResponse.Body).Decode(&publication); err != nil {
		t.Fatal(err)
	}
	if len(publication.Proxies) != 2 {
		t.Fatalf("shared Exit Identity removed qualified nodes: %+v", publication.Proxies)
	}
}

func TestLowScoreIsDistinctFromScoreUnavailable(t *testing.T) {
	channel := &identityChannel{attempt: app.AvailabilityAttempt{
		Success:        true,
		Verified:       true,
		Latency:        100 * time.Millisecond,
		ExitIdentities: []app.ExitIdentityCandidate{{IP: "198.51.100.13", Verified: true}},
	}}
	scoring := &recordingScoring{score: 69}
	server := openEvaluationApplicationWithScoring(t, &recordingUpstream{document: []byte("proxies:\n  - name: low-score\n    type: ss\n    server: example.test\n    port: 443\n")}, &nodeValidationKernel{}, channel, scoring)
	putJSON(t, server.URL+"/api/settings", map[string]any{"availabilityAttempts": 1, "availabilityRequiredSuccesses": 1, "availabilityURLs": []string{"https://probe.example/204"}, "evaluationWorkerCount": 1, "scoringJitterMs": 0})
	response := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{"name": "Eval", "kind": "url", "url": "https://example.test/sub"})
	_ = response.Body.Close()
	var run struct {
		Results []struct {
			State  string   `json:"state"`
			Score  *float64 `json:"ipScore"`
			Reason string   `json:"reason"`
		} `json:"results"`
	}
	runEvaluation(t, server.URL, map[string]any{})
	getJSON(t, server.URL+"/api/evaluation-runs/current", &run)
	if len(run.Results) != 1 || run.Results[0].State != "failed" || run.Results[0].Score == nil || !strings.HasPrefix(run.Results[0].Reason, "low_score:") {
		t.Fatalf("low-score result=%+v", run.Results)
	}
}

func TestEvaluationRunRejectsPartialFailureTimeoutHighLatencyAndUnknownOwnership(t *testing.T) {
	for _, outcome := range []string{"partial", "timeout", "slow", "unverified"} {
		t.Run(outcome, func(t *testing.T) {
			channel := &availabilityChannel{outcome: outcome, verified: outcome != "unverified", latencies: []time.Duration{100 * time.Millisecond}}
			server := openEvaluationApplication(t, &recordingUpstream{document: []byte("proxies:\n  - name: candidate\n    type: ss\n    server: example.test\n    port: 443\n")}, &nodeValidationKernel{}, channel)
			response := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{"name": "Eval", "kind": "url", "url": "https://example.test/sub"})
			_ = response.Body.Close()
			if response.StatusCode != http.StatusCreated {
				t.Fatalf("create status=%d", response.StatusCode)
			}
			start := postJSONResponse(t, server.URL+"/api/evaluation-runs", map[string]any{})
			_ = start.Body.Close()
			var run struct {
				Status  string `json:"status"`
				Results []struct {
					State  string `json:"state"`
					Reason string `json:"reason"`
				} `json:"results"`
			}
			for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
				getJSON(t, server.URL+"/api/evaluation-runs/current", &run)
				if run.Status != "running" {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if run.Status != "completed" || len(run.Results) != 1 || run.Results[0].State != "failed" {
				t.Fatalf("run=%+v", run)
			}
		})
	}
}

func TestAvailabilityFailureDoesNotCallScoringProvider(t *testing.T) {
	scoring := &countingScoring{}
	server := openEvaluationApplicationWithScoring(t, &recordingUpstream{document: []byte("proxies:\n  - name: unavailable\n    type: ss\n    server: example.test\n    port: 443\n")}, &nodeValidationKernel{}, &availabilityChannel{outcome: "partial", verified: true, latencies: []time.Duration{100 * time.Millisecond}}, scoring)
	response := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{"name": "Eval", "kind": "url", "url": "https://example.test/sub"})
	_ = response.Body.Close()
	start := postJSONResponse(t, server.URL+"/api/evaluation-runs", map[string]any{})
	_ = start.Body.Close()
	var run struct {
		Status string `json:"status"`
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		getJSON(t, server.URL+"/api/evaluation-runs/current", &run)
		if run.Status != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if run.Status != "completed" || scoring.calls != 0 {
		t.Fatalf("run=%+v scoring calls=%d", run, scoring.calls)
	}
}

func TestAvailabilitySettingsArePersistedAndAppliedToTheNextRun(t *testing.T) {
	channel := &availabilityChannel{verified: true, latencies: []time.Duration{100 * time.Millisecond, 300 * time.Millisecond}}
	server := openEvaluationApplication(t, &recordingUpstream{document: []byte("proxies:\n  - name: configured\n    type: ss\n    server: example.test\n    port: 443\n")}, &nodeValidationKernel{}, channel)
	putJSON(t, server.URL+"/api/settings", map[string]any{
		"availabilityAttempts":          2,
		"availabilityRequiredSuccesses": 2,
		"availabilityTimeoutSeconds":    1,
		"availabilityMaxLatencyMs":      500,
		"availabilityURLs":              []string{"https://probe.example/204"},
		"evaluationWorkerCount":         1,
		"scoringJitterMs":               0,
	})
	var settings struct {
		AvailabilityAttempts          int      `json:"availabilityAttempts"`
		AvailabilityRequiredSuccesses int      `json:"availabilityRequiredSuccesses"`
		AvailabilityTimeoutSeconds    int      `json:"availabilityTimeoutSeconds"`
		AvailabilityMaxLatencyMS      int      `json:"availabilityMaxLatencyMs"`
		AvailabilityURLs              []string `json:"availabilityURLs"`
		EvaluationWorkerCount         int      `json:"evaluationWorkerCount"`
		ScoringJitterMS               int      `json:"scoringJitterMs"`
	}
	getJSON(t, server.URL+"/api/settings", &settings)
	if settings.AvailabilityAttempts != 2 || settings.AvailabilityRequiredSuccesses != 2 || settings.AvailabilityTimeoutSeconds != 1 || settings.AvailabilityMaxLatencyMS != 500 || settings.EvaluationWorkerCount != 1 || settings.ScoringJitterMS != 0 || len(settings.AvailabilityURLs) != 1 || settings.AvailabilityURLs[0] != "https://probe.example/204" {
		t.Fatalf("settings=%+v", settings)
	}
	response := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{"name": "Eval", "kind": "url", "url": "https://example.test/sub"})
	_ = response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create status=%d", response.StatusCode)
	}
	start := postJSONResponse(t, server.URL+"/api/evaluation-runs", map[string]any{})
	_ = start.Body.Close()
	var run struct {
		Status  string `json:"status"`
		Passed  int    `json:"passed"`
		Results []struct {
			Attempts int     `json:"attempts"`
			Median   float64 `json:"medianLatencyMs"`
		} `json:"results"`
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		getJSON(t, server.URL+"/api/evaluation-runs/current", &run)
		if run.Status != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if run.Status != "completed" || run.Passed != 1 || len(run.Results) != 1 || run.Results[0].Attempts != 2 || run.Results[0].Median != 200 || channel.calls != 2 {
		t.Fatalf("run=%+v calls=%d", run, channel.calls)
	}
}

func TestAvailabilityAttemptSharesOneTimeoutBudgetAcrossTargets(t *testing.T) {
	channel := &timeoutBudgetChannel{}
	server := openEvaluationApplication(t, &recordingUpstream{document: []byte("proxies:\n  - name: configured\n    type: ss\n    server: example.test\n    port: 443\n")}, &nodeValidationKernel{}, channel)
	putJSON(t, server.URL+"/api/settings", map[string]any{
		"availabilityAttempts":          1,
		"availabilityRequiredSuccesses": 1,
		"availabilityTimeoutSeconds":    1,
		"availabilityURLs":              []string{"https://first.example/204", "https://second.example/204"},
		"evaluationWorkerCount":         1,
		"scoringJitterMs":               0,
	})
	response := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{"name": "Eval", "kind": "url", "url": "https://example.test/sub"})
	_ = response.Body.Close()
	start := postJSONResponse(t, server.URL+"/api/evaluation-runs", map[string]any{})
	_ = start.Body.Close()
	var run struct {
		Status string `json:"status"`
		Passed int    `json:"passed"`
		Reason string `json:"reason"`
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		getJSON(t, server.URL+"/api/evaluation-runs/current", &run)
		if run.Status != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if run.Status != "completed" || run.Passed != 1 || run.Reason != "" || len(channel.deadlines) != 2 || !channel.deadlines[0].Equal(channel.deadlines[1]) {
		t.Fatalf("run=%+v deadlines=%v", run, channel.deadlines)
	}
}

func TestAvailabilityUsesTheTrueMedianForAnEvenNumberOfSamples(t *testing.T) {
	channel := &availabilityChannel{verified: true, latencies: []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 300 * time.Millisecond, 400 * time.Millisecond}}
	server := openEvaluationApplication(t, &recordingUpstream{document: []byte("proxies:\n  - name: configured\n    type: ss\n    server: example.test\n    port: 443\n")}, &nodeValidationKernel{}, channel)
	putJSON(t, server.URL+"/api/settings", map[string]any{"availabilityAttempts": 4, "availabilityRequiredSuccesses": 4, "availabilityURLs": []string{"https://probe.example/204"}, "evaluationWorkerCount": 1, "scoringJitterMs": 0})
	response := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{"name": "Eval", "kind": "url", "url": "https://example.test/sub"})
	_ = response.Body.Close()
	start := postJSONResponse(t, server.URL+"/api/evaluation-runs", map[string]any{})
	_ = start.Body.Close()
	var run struct {
		Status  string `json:"status"`
		Results []struct {
			Median float64 `json:"medianLatencyMs"`
		} `json:"results"`
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		getJSON(t, server.URL+"/api/evaluation-runs/current", &run)
		if run.Status != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if run.Status != "completed" || len(run.Results) != 1 || run.Results[0].Median != 250 {
		t.Fatalf("run=%+v", run)
	}
}

func TestEvaluationUsesAtMostThreeConcurrentProxyNodeWorkers(t *testing.T) {
	channel := &concurrencyChannel{}
	scoring := &countingScoring{}
	document := []byte("proxies:\n  - name: one\n    type: ss\n    server: one.example\n    port: 443\n  - name: two\n    type: ss\n    server: two.example\n    port: 443\n  - name: three\n    type: ss\n    server: three.example\n    port: 443\n  - name: four\n    type: ss\n    server: four.example\n    port: 443\n")
	server := openEvaluationApplicationWithScoring(t, &recordingUpstream{document: document}, &nodeValidationKernel{}, channel, scoring)
	putJSON(t, server.URL+"/api/settings", map[string]any{"availabilityAttempts": 1, "availabilityRequiredSuccesses": 1, "availabilityURLs": []string{"https://probe.example/204"}, "evaluationWorkerCount": 3, "scoringJitterMs": 0})
	response := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{"name": "Eval", "kind": "url", "url": "https://example.test/sub"})
	_ = response.Body.Close()
	start := postJSONResponse(t, server.URL+"/api/evaluation-runs", map[string]any{})
	_ = start.Body.Close()
	var run struct {
		Status string `json:"status"`
	}
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		getJSON(t, server.URL+"/api/evaluation-runs/current", &run)
		if run.Status != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if run.Status != "completed" || channel.maxActive < 2 || channel.maxActive > 3 || scoring.calls != 1 {
		t.Fatalf("run=%+v max concurrent workers=%d scoring calls=%d", run, channel.maxActive, scoring.calls)
	}
}

func TestSuccessfulEvaluationPublishesOnlyQualifiedNodes(t *testing.T) {
	server := openEvaluationApplication(t, &recordingUpstream{document: []byte("proxies:\n  - name: published\n    type: ss\n    server: example.test\n    port: 443\nrules:\n  - MATCH,DIRECT\ndns:\n  enable: true\n")}, &nodeValidationKernel{}, &availabilityChannel{verified: true, latencies: []time.Duration{100 * time.Millisecond}})
	response := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{"name": "Publish", "kind": "url", "url": "https://example.test/sub"})
	_ = response.Body.Close()
	start := postJSONResponse(t, server.URL+"/api/evaluation-runs", map[string]any{})
	_ = start.Body.Close()
	var run struct {
		Status string `json:"status"`
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		getJSON(t, server.URL+"/api/evaluation-runs/current", &run)
		if run.Status != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if run.Status != "completed" {
		t.Fatalf("run status=%s", run.Status)
	}
	publicationResponse, err := http.Get(server.URL + "/sub/clash.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer publicationResponse.Body.Close()
	var publication struct {
		Proxies []map[string]any `yaml:"proxies"`
		Groups  []map[string]any `yaml:"proxy-groups"`
		Rules   []string         `yaml:"rules"`
		DNS     map[string]any   `yaml:"dns"`
	}
	if err := yaml.NewDecoder(publicationResponse.Body).Decode(&publication); err != nil {
		t.Fatal(err)
	}
	if len(publication.Proxies) != 1 || publication.Proxies[0]["name"] != "[Publish] published" || len(publication.Groups) != 3 || publication.Rules != nil || publication.DNS != nil {
		t.Fatalf("publication=%+v", publication)
	}
}

func TestPublicationValidationFailurePreservesSnapshotAndReportsReason(t *testing.T) {
	kernel := &rejectingPublicationKernel{}
	server := openEvaluationApplication(t, &recordingUpstream{document: []byte("proxies:\n  - name: candidate\n    type: ss\n    server: example.test\n    port: 443\n")}, kernel, &availabilityChannel{verified: true, latencies: []time.Duration{100 * time.Millisecond}})
	response := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{"name": "Publish", "kind": "url", "url": "https://example.test/sub"})
	_ = response.Body.Close()
	runEvaluation(t, server.URL, map[string]any{})
	beforeResponse, err := http.Get(server.URL + "/sub/clash.yaml")
	if err != nil {
		t.Fatal(err)
	}
	before, err := io.ReadAll(beforeResponse.Body)
	_ = beforeResponse.Body.Close()
	if err != nil {
		t.Fatal(err)
	}

	kernel.reject = true
	runEvaluation(t, server.URL, map[string]any{})

	var run struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	getJSON(t, server.URL+"/api/evaluation-runs/current", &run)
	if run.Status != "failed" || !strings.Contains(run.Reason, "publication validation failed") {
		t.Fatalf("run=%+v", run)
	}
	afterResponse, err := http.Get(server.URL + "/sub/clash.yaml")
	if err != nil {
		t.Fatal(err)
	}
	after, err := io.ReadAll(afterResponse.Body)
	_ = afterResponse.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("failed publication replaced the previous snapshot: before=%q after=%q", before, after)
	}
}

func TestPublishedSubscriptionEndpointServesOnlyCompleteSnapshotsDuringReplacement(t *testing.T) {
	upstream := &recordingUpstream{document: []byte("proxies:\n  - name: first\n    type: ss\n    server: first.example\n    port: 443\n")}
	channel := &concurrencyChannel{}
	server := openEvaluationApplication(t, upstream, &nodeValidationKernel{}, channel)
	putJSON(t, server.URL+"/api/settings", map[string]any{"availabilityAttempts": 1, "availabilityRequiredSuccesses": 1, "evaluationWorkerCount": 1, "scoringJitterMs": 0})
	response := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{"name": "Publish", "kind": "url", "url": "https://example.test/sub"})
	var subscription struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&subscription); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	runEvaluation(t, server.URL, map[string]any{})

	readPublication := func() []byte {
		response, err := http.Get(server.URL + "/sub/clash.yaml")
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		document, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return document
	}
	oldSnapshot := readPublication()
	upstream.document = []byte("proxies:\n  - name: second\n    type: ss\n    server: second.example\n    port: 443\n")
	refreshResponse, err := http.Post(server.URL+"/api/upstream-subscriptions/"+subscription.ID+"/refresh", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = refreshResponse.Body.Close()
	if refreshResponse.StatusCode != http.StatusOK {
		t.Fatalf("refresh status=%d", refreshResponse.StatusCode)
	}

	var observed [][]byte
	var observedMu sync.Mutex
	var readerErrors []error
	var readerWG sync.WaitGroup
	readerReady := make(chan struct{}, 4)
	channel.probeStarted = make(chan struct{})
	channel.releaseProbe = make(chan struct{})
	for reader := 0; reader < 4; reader++ {
		readerWG.Add(1)
		go func() {
			defer readerWG.Done()
			readerReady <- struct{}{}
			for attempt := 0; attempt < 30; attempt++ {
				response, err := http.Get(server.URL + "/sub/clash.yaml")
				if err != nil {
					observedMu.Lock()
					readerErrors = append(readerErrors, err)
					observedMu.Unlock()
					continue
				}
				document, err := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if err != nil {
					observedMu.Lock()
					readerErrors = append(readerErrors, err)
					observedMu.Unlock()
					continue
				}
				observedMu.Lock()
				observed = append(observed, document)
				observedMu.Unlock()
			}
		}()
	}
	for reader := 0; reader < 4; reader++ {
		<-readerReady
	}
	runResponse := postJSONResponse(t, server.URL+"/api/evaluation-runs", map[string]any{})
	_ = runResponse.Body.Close()
	if runResponse.StatusCode != http.StatusAccepted {
		t.Fatalf("start evaluation status=%d", runResponse.StatusCode)
	}
	<-channel.probeStarted
	close(channel.releaseProbe)
	waitForEvaluationRun(t, server.URL)
	readerWG.Wait()
	if len(readerErrors) > 0 {
		t.Fatalf("publication readers failed: %v", readerErrors[0])
	}
	newSnapshot := readPublication()
	if bytes.Equal(oldSnapshot, newSnapshot) {
		t.Fatal("second evaluation did not produce a new snapshot")
	}
	for index, document := range observed {
		if !bytes.Equal(document, oldSnapshot) && !bytes.Equal(document, newSnapshot) {
			t.Fatalf("reader %d observed a partial or unknown snapshot: %q", index, document)
		}
	}
}

func TestSurfingTUNPausesEvaluationBeforeProbe(t *testing.T) {
	channel := &availabilityChannel{verified: true, latencies: []time.Duration{100 * time.Millisecond}}
	server := openGuardedEvaluationApplication(t, &recordingUpstream{document: []byte("proxies:\n  - name: candidate\n    type: ss\n    server: example.test\n    port: 443\n")}, &nodeValidationKernel{}, channel, surfingGuard{status: app.SurfingIsolationStatus{Mode: "tun", Reason: "Surfing TUN is active"}})
	response := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{"name": "Guarded", "kind": "url", "url": "https://example.test/sub"})
	_ = response.Body.Close()
	start := postJSONResponse(t, server.URL+"/api/evaluation-runs", map[string]any{})
	_ = start.Body.Close()
	var run struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		getJSON(t, server.URL+"/api/evaluation-runs/current", &run)
		if run.Status != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if run.Status != "paused" || run.Reason != "Surfing TUN is active" || channel.calls != 0 {
		t.Fatalf("run=%+v calls=%d", run, channel.calls)
	}
}

func openTestApplicationWithKernel(t *testing.T, upstream app.Upstream, kernel app.Kernel) *httptest.Server {
	t.Helper()
	assets := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte(`<!doctype html><div id="root"></div>`)}}
	instance, err := app.Open(context.Background(), app.Config{DatabasePath: filepath.Join(t.TempDir(), "nodeharbor.db"), WebAssets: fs.FS(assets)}, app.Dependencies{Upstream: upstream, Scoring: unavailableScoring{}, Kernel: kernel, TestChannel: unavailableTestChannel{}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(instance.Handler())
	t.Cleanup(server.Close)
	t.Cleanup(func() { _ = instance.Close() })
	return server
}

func openEvaluationApplication(t *testing.T, upstream app.Upstream, kernel app.Kernel, channel app.TestChannel) *httptest.Server {
	return openEvaluationApplicationWithScoring(t, upstream, kernel, channel, &recordingScoring{score: 80})
}

func openEvaluationApplicationWithScoring(t *testing.T, upstream app.Upstream, kernel app.Kernel, channel app.TestChannel, scoring app.ScoringProvider) *httptest.Server {
	return openEvaluationApplicationWithScoringAtPath(t, filepath.Join(t.TempDir(), "nodeharbor.db"), upstream, kernel, channel, scoring)
}

func openEvaluationApplicationWithScoringAtPath(t *testing.T, databasePath string, upstream app.Upstream, kernel app.Kernel, channel app.TestChannel, scoring app.ScoringProvider) *httptest.Server {
	t.Helper()
	assets := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte(`<!doctype html><div id="root"></div>`)}}
	instance, err := app.Open(context.Background(), app.Config{DatabasePath: databasePath, WebAssets: fs.FS(assets)}, app.Dependencies{Upstream: upstream, Scoring: scoring, Kernel: kernel, TestChannel: channel})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(instance.Handler())
	t.Cleanup(server.Close)
	t.Cleanup(func() { _ = instance.Close() })
	return server
}

func openEvaluationApplicationWithProviders(t *testing.T, upstream app.Upstream, kernel app.Kernel, channel app.TestChannel, scoring app.ScoringProvider, providers map[string]app.ScoringProvider) *httptest.Server {
	t.Helper()
	assets := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte(`<!doctype html><div id="root"></div>`)}}
	instance, err := app.Open(context.Background(), app.Config{DatabasePath: filepath.Join(t.TempDir(), "nodeharbor.db"), WebAssets: fs.FS(assets)}, app.Dependencies{Upstream: upstream, Scoring: scoring, ScoringProviders: providers, Kernel: kernel, TestChannel: channel})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(instance.Handler())
	t.Cleanup(server.Close)
	t.Cleanup(func() { _ = instance.Close() })
	return server
}

func openGuardedEvaluationApplication(t *testing.T, upstream app.Upstream, kernel app.Kernel, channel app.TestChannel, guard app.SurfingIsolationGuard) *httptest.Server {
	t.Helper()
	assets := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte(`<!doctype html><div id="root"></div>`)}}
	instance, err := app.Open(context.Background(), app.Config{DatabasePath: filepath.Join(t.TempDir(), "nodeharbor.db"), WebAssets: fs.FS(assets)}, app.Dependencies{Upstream: upstream, Scoring: &recordingScoring{score: 80}, Kernel: kernel, TestChannel: channel, Isolation: guard})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(instance.Handler())
	t.Cleanup(server.Close)
	t.Cleanup(func() { _ = instance.Close() })
	return server
}

func TestInitialPublishedSubscriptionPassesMihomoValidation(t *testing.T) {
	mihomoPath := os.Getenv("NODEHARBOR_TEST_MIHOMO")
	if mihomoPath == "" {
		t.Skip("NODEHARBOR_TEST_MIHOMO is not set")
	}
	instance := openTestApplication(t, filepath.Join(t.TempDir(), "nodeharbor.db"), app.NewMihomoKernel(mihomoPath))
	server := httptest.NewServer(instance.Handler())
	t.Cleanup(server.Close)
	response, err := http.Get(server.URL + "/sub/clash.yaml")
	if err != nil {
		t.Fatal(err)
	}
	document, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(document) == 0 {
		t.Fatal("Mihomo-validated publication is empty")
	}
}

type settingsResponse struct {
	Language       string `json:"language"`
	InstallationID string `json:"installationId"`
}

func readSettings(t *testing.T, baseURL string) settingsResponse {
	t.Helper()
	var settings settingsResponse
	getJSON(t, baseURL+"/api/settings", &settings)
	return settings
}

func getJSON(t *testing.T, url string, target any) {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d", url, response.StatusCode)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}

func putJSON(t *testing.T, url string, value any) {
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
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		message, _ := io.ReadAll(response.Body)
		t.Fatalf("PUT %s status=%d body=%q", url, response.StatusCode, message)
	}
}

func openTestApplication(t *testing.T, databasePath string, kernel app.Kernel) *app.Application {
	t.Helper()
	assets := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(`<!doctype html><div id="root"></div>`)},
	}
	instance, err := app.Open(context.Background(), app.Config{
		DatabasePath: databasePath,
		WebAssets:    fs.FS(assets),
	}, app.Dependencies{
		Upstream:    unavailableUpstream{},
		Scoring:     unavailableScoring{},
		Kernel:      kernel,
		TestChannel: unavailableTestChannel{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	return instance
}

type recordingKernel struct{ validated []byte }

func (kernel *recordingKernel) Validate(_ context.Context, document []byte) error {
	kernel.validated = append([]byte(nil), document...)
	return nil
}

type nodeValidationKernel struct{}

func (*nodeValidationKernel) Validate(context.Context, []byte) error { return nil }
func (*nodeValidationKernel) ValidateNode(_ context.Context, node app.ProxyNode) error {
	if strings.Contains(node.Name, "bad") {
		return errors.New("unsupported protocol")
	}
	return nil
}

type rejectingPublicationKernel struct {
	reject bool
}

func (kernel *rejectingPublicationKernel) Validate(_ context.Context, document []byte) error {
	if kernel.reject && bytes.Contains(document, []byte("[Publish] candidate")) {
		return errors.New("publication validation failed")
	}
	return nil
}

func (*rejectingPublicationKernel) ValidateNode(context.Context, app.ProxyNode) error { return nil }

type availabilityChannel struct {
	verified  bool
	latencies []time.Duration
	calls     int
	outcome   string
}

type timeoutBudgetChannel struct {
	mu        sync.Mutex
	deadlines []time.Time
}

type concurrencyChannel struct {
	mu           sync.Mutex
	active       int
	maxActive    int
	probeStarted chan struct{}
	releaseProbe chan struct{}
	probeOnce    sync.Once
}

func (channel *concurrencyChannel) Probe(context.Context, app.ProxyNode) (app.ProbeResult, error) {
	return app.ProbeResult{}, nil
}

func (channel *concurrencyChannel) ProbeAttempt(context.Context, app.ProxyNode, string) (app.AvailabilityAttempt, error) {
	channel.mu.Lock()
	channel.active++
	if channel.active > channel.maxActive {
		channel.maxActive = channel.active
	}
	channel.mu.Unlock()
	if channel.probeStarted != nil {
		channel.probeOnce.Do(func() { close(channel.probeStarted) })
		<-channel.releaseProbe
	}
	time.Sleep(40 * time.Millisecond)
	channel.mu.Lock()
	channel.active--
	channel.mu.Unlock()
	return app.AvailabilityAttempt{Success: true, Verified: true, Latency: 10 * time.Millisecond, ExitIdentity: "203.0.113.8"}, nil
}

func (channel *concurrencyChannel) HTTPClient(context.Context, app.ProxyNode) (*http.Client, error) {
	return boundTestChannelClient(), nil
}

func (channel *concurrencyChannel) DiscoverExitIdentities(_ context.Context, _ app.ProxyNode, family string) ([]app.ExitIdentityCandidate, error) {
	if family != "ipv4" {
		return nil, app.ErrUnavailable
	}
	return []app.ExitIdentityCandidate{{IP: "203.0.113.8", Verified: true}}, nil
}

func (channel *timeoutBudgetChannel) Probe(context.Context, app.ProxyNode) (app.ProbeResult, error) {
	return app.ProbeResult{}, nil
}

func (channel *timeoutBudgetChannel) ProbeAttempt(ctx context.Context, _ app.ProxyNode, target string) (app.AvailabilityAttempt, error) {
	deadline, _ := ctx.Deadline()
	channel.mu.Lock()
	channel.deadlines = append(channel.deadlines, deadline)
	call := len(channel.deadlines)
	channel.mu.Unlock()
	if call == 1 && target != "https://second.example/204" {
		return app.AvailabilityAttempt{}, errors.New("target unavailable")
	}
	return app.AvailabilityAttempt{Success: true, Verified: true, Latency: 100 * time.Millisecond, ExitIdentity: "203.0.113.8"}, nil
}

func (channel *timeoutBudgetChannel) HTTPClient(context.Context, app.ProxyNode) (*http.Client, error) {
	return boundTestChannelClient(), nil
}

func (channel *timeoutBudgetChannel) DiscoverExitIdentities(_ context.Context, _ app.ProxyNode, family string) ([]app.ExitIdentityCandidate, error) {
	if family != "ipv4" {
		return nil, app.ErrUnavailable
	}
	return []app.ExitIdentityCandidate{{IP: "203.0.113.8", Verified: true}}, nil
}

type surfingGuard struct{ status app.SurfingIsolationStatus }

func (guard surfingGuard) Check(context.Context) (app.SurfingIsolationStatus, error) {
	return guard.status, nil
}

func (channel *availabilityChannel) Probe(context.Context, app.ProxyNode) (app.ProbeResult, error) {
	return app.ProbeResult{}, nil
}

func (channel *availabilityChannel) ProbeAttempt(_ context.Context, _ app.ProxyNode, _ string) (app.AvailabilityAttempt, error) {
	if channel.outcome == "timeout" {
		return app.AvailabilityAttempt{}, errors.New("context deadline exceeded")
	}
	latency := channel.latencies[channel.calls%len(channel.latencies)]
	channel.calls++
	if channel.outcome == "slow" {
		latency = 2 * time.Second
	}
	if channel.outcome == "partial" && channel.calls > 1 {
		return app.AvailabilityAttempt{Success: false, Verified: true}, nil
	}
	return app.AvailabilityAttempt{Success: true, Verified: channel.verified, Latency: latency, ExitIdentity: "203.0.113.8"}, nil
}

func (channel *availabilityChannel) HTTPClient(context.Context, app.ProxyNode) (*http.Client, error) {
	return boundTestChannelClient(), nil
}

func (channel *availabilityChannel) DiscoverExitIdentities(_ context.Context, _ app.ProxyNode, family string) ([]app.ExitIdentityCandidate, error) {
	if family != "ipv4" {
		return nil, app.ErrUnavailable
	}
	return []app.ExitIdentityCandidate{{IP: "203.0.113.8", Verified: channel.verified}}, nil
}

type unavailableUpstream struct{}

func (unavailableUpstream) Fetch(context.Context, app.UpstreamRequest) ([]byte, error) {
	return nil, app.ErrUnavailable
}

type unavailableScoring struct{}

func (unavailableScoring) Score(context.Context, string) (float64, error) {
	return 0, app.ErrUnavailable
}

type unavailableTestChannel struct{}

func (unavailableTestChannel) Probe(context.Context, app.ProxyNode) (app.ProbeResult, error) {
	return app.ProbeResult{}, app.ErrUnavailable
}

type recordingUpstream struct {
	document []byte
	request  app.UpstreamRequest
}

func (upstream *recordingUpstream) Fetch(_ context.Context, request app.UpstreamRequest) ([]byte, error) {
	upstream.request = request
	return upstream.document, nil
}

type recordingTestChannel struct {
	node   app.ProxyNode
	result app.ProbeResult
}

type iplarkFixtureChannel struct {
	destination     string
	httpClientCalls int
}

func (channel *iplarkFixtureChannel) Probe(context.Context, app.ProxyNode) (app.ProbeResult, error) {
	return app.ProbeResult{}, nil
}

func (channel *iplarkFixtureChannel) ProbeAttempt(context.Context, app.ProxyNode, string) (app.AvailabilityAttempt, error) {
	return app.AvailabilityAttempt{Success: true, Verified: true, Latency: 100 * time.Millisecond}, nil
}

func (channel *iplarkFixtureChannel) DiscoverExitIdentities(_ context.Context, _ app.ProxyNode, family string) ([]app.ExitIdentityCandidate, error) {
	if family != "ipv4" {
		return nil, app.ErrUnavailable
	}
	return []app.ExitIdentityCandidate{{IP: "198.51.100.77", Verified: true}}, nil
}

func (channel *iplarkFixtureChannel) HTTPClient(context.Context, app.ProxyNode) (*http.Client, error) {
	channel.httpClientCalls++
	destination, err := url.Parse(channel.destination)
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		redirected := request.Clone(request.Context())
		redirected.URL.Scheme = destination.Scheme
		redirected.URL.Host = destination.Host
		return http.DefaultTransport.RoundTrip(redirected)
	})}, nil
}

func (channel *recordingTestChannel) Probe(_ context.Context, node app.ProxyNode) (app.ProbeResult, error) {
	channel.node = node
	return channel.result, nil
}

func (channel *recordingTestChannel) HTTPClient(context.Context, app.ProxyNode) (*http.Client, error) {
	return boundTestChannelClient(), nil
}

type identityChannel struct {
	attempt app.AvailabilityAttempt
}

func (channel *identityChannel) Probe(context.Context, app.ProxyNode) (app.ProbeResult, error) {
	return app.ProbeResult{}, nil
}

func (channel *identityChannel) ProbeAttempt(context.Context, app.ProxyNode, string) (app.AvailabilityAttempt, error) {
	return channel.attempt, nil
}

func (channel *identityChannel) DiscoverExitIdentities(_ context.Context, _ app.ProxyNode, family string) ([]app.ExitIdentityCandidate, error) {
	candidates := channel.attempt.ExitIdentities
	if len(candidates) == 0 && channel.attempt.ExitIdentity != "" {
		candidates = []app.ExitIdentityCandidate{{IP: channel.attempt.ExitIdentity, Verified: channel.attempt.Verified}}
	}
	result := make([]app.ExitIdentityCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if addressFamilyForTest(candidate.IP) == family {
			result = append(result, candidate)
		}
	}
	if len(result) == 0 {
		return nil, app.ErrUnavailable
	}
	return result, nil
}

func (channel *identityChannel) HTTPClient(context.Context, app.ProxyNode) (*http.Client, error) {
	return boundTestChannelClient(), nil
}

func addressFamilyForTest(ip string) string {
	if strings.Contains(ip, ":") {
		return "ipv6"
	}
	return "ipv4"
}

type familyIdentityChannel struct {
	families []string
	fallback string
}

func (channel *familyIdentityChannel) Probe(context.Context, app.ProxyNode) (app.ProbeResult, error) {
	return app.ProbeResult{}, nil
}

func (channel *familyIdentityChannel) ProbeAttempt(context.Context, app.ProxyNode, string) (app.AvailabilityAttempt, error) {
	return app.AvailabilityAttempt{Success: true, Verified: true, Latency: 100 * time.Millisecond}, nil
}

func (channel *familyIdentityChannel) DiscoverExitIdentities(_ context.Context, _ app.ProxyNode, family string) ([]app.ExitIdentityCandidate, error) {
	channel.families = append(channel.families, family)
	if family == "ipv4" {
		return nil, app.ErrUnavailable
	}
	return []app.ExitIdentityCandidate{{IP: channel.fallback, Verified: true}}, nil
}

func (channel *familyIdentityChannel) HTTPClient(context.Context, app.ProxyNode) (*http.Client, error) {
	return boundTestChannelClient(), nil
}

func waitForEvaluationRun(t *testing.T, baseURL string) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		var current struct {
			Status string `json:"status"`
		}
		getJSON(t, baseURL+"/api/evaluation-runs/current", &current)
		if current.Status != "running" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("evaluation run did not finish")
}

func runEvaluation(t *testing.T, baseURL string, input any) {
	t.Helper()
	response := postJSONResponse(t, baseURL+"/api/evaluation-runs", input)
	if response.StatusCode != http.StatusAccepted {
		message, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		t.Fatalf("start evaluation status=%d body=%q", response.StatusCode, message)
	}
	_ = response.Body.Close()
	waitForEvaluationRun(t, baseURL)
}

type recordingScoring struct {
	mu           sync.Mutex
	exitIdentity string
	score        float64
}

type countingScoring struct {
	mu    sync.Mutex
	calls int
}

type unboundScoring struct {
	calls int
}

type namedScoring struct {
	mu    sync.Mutex
	name  string
	score float64
	calls int
	err   error
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (roundTripper roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTripper(request)
}

func boundTestChannelClient() *http.Client {
	return &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	})}
}

func useBoundTestChannelClient(client *http.Client) error {
	if client == nil {
		return errors.New("missing Test Channel HTTP client")
	}
	request, err := http.NewRequest(http.MethodGet, "https://scoring-through-channel.invalid/", nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	return response.Body.Close()
}

func (scoring *namedScoring) Name() string { return scoring.name }

func (scoring *namedScoring) Score(context.Context, string) (float64, error) {
	scoring.mu.Lock()
	defer scoring.mu.Unlock()
	scoring.calls++
	if scoring.err != nil {
		return 0, scoring.err
	}
	return scoring.score, nil
}

func (scoring *namedScoring) ScoreWithClient(ctx context.Context, exitIdentity string, client *http.Client) (float64, error) {
	if err := useBoundTestChannelClient(client); err != nil {
		return 0, err
	}
	return scoring.Score(ctx, exitIdentity)
}

func (scoring *namedScoring) callCount() int {
	scoring.mu.Lock()
	defer scoring.mu.Unlock()
	return scoring.calls
}

func (scoring *unboundScoring) Score(context.Context, string) (float64, error) {
	scoring.calls++
	return 80, nil
}

func (scoring *countingScoring) callCount() int {
	scoring.mu.Lock()
	defer scoring.mu.Unlock()
	return scoring.calls
}

func (scoring *countingScoring) Score(context.Context, string) (float64, error) {
	scoring.mu.Lock()
	defer scoring.mu.Unlock()
	scoring.calls++
	return 80, nil
}

func (scoring *countingScoring) ScoreWithClient(ctx context.Context, exitIdentity string, client *http.Client) (float64, error) {
	if err := useBoundTestChannelClient(client); err != nil {
		return 0, err
	}
	return scoring.Score(ctx, exitIdentity)
}

func (scoring *recordingScoring) Score(_ context.Context, exitIdentity string) (float64, error) {
	scoring.mu.Lock()
	defer scoring.mu.Unlock()
	scoring.exitIdentity = exitIdentity
	return scoring.score, nil
}

func (scoring *recordingScoring) ScoreWithClient(ctx context.Context, exitIdentity string, client *http.Client) (float64, error) {
	if err := useBoundTestChannelClient(client); err != nil {
		return 0, err
	}
	return scoring.Score(ctx, exitIdentity)
}
