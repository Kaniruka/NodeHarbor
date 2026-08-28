package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Kaniruka/NodeHarbor/internal/app"
	"gopkg.in/yaml.v3"
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

func TestBlackBoxEvaluationRequestTraversesReplaceableAdapters(t *testing.T) {
	upstream := &recordingUpstream{document: []byte("proxies:\n  - name: fixture-node\n    type: ss\n    server: example.test\n    port: 443\n")}
	kernel := &recordingKernel{}
	channel := &recordingTestChannel{result: app.ProbeResult{ExitIdentity: "203.0.113.7"}}
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
	var nodes []struct{ Name, State, Reason string }
	getJSON(t, server.URL+"/api/upstream-subscriptions/"+created.ID+"/nodes", &nodes)
	if len(nodes) != 2 || nodes[0].Name != "[My Source] good" || nodes[0].State != "accepted" || nodes[1].State != "rejected" || !strings.Contains(nodes[1].Reason, "validation_failed") {
		t.Fatalf("nodes=%+v", nodes)
	}
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
	t.Helper()
	assets := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte(`<!doctype html><div id="root"></div>`)}}
	instance, err := app.Open(context.Background(), app.Config{DatabasePath: filepath.Join(t.TempDir(), "nodeharbor.db"), WebAssets: fs.FS(assets)}, app.Dependencies{Upstream: upstream, Scoring: unavailableScoring{}, Kernel: kernel, TestChannel: channel})
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

type availabilityChannel struct {
	verified  bool
	latencies []time.Duration
	calls     int
	outcome   string
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

func (channel *recordingTestChannel) Probe(_ context.Context, node app.ProxyNode) (app.ProbeResult, error) {
	channel.node = node
	return channel.result, nil
}

type recordingScoring struct {
	exitIdentity string
	score        float64
}

func (scoring *recordingScoring) Score(_ context.Context, exitIdentity string) (float64, error) {
	scoring.exitIdentity = exitIdentity
	return scoring.score, nil
}
