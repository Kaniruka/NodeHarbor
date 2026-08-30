package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Kaniruka/NodeHarbor/internal/app"
	"gopkg.in/yaml.v3"
)

// TestProductionAssemblyPublishesThroughRealMihomo proves the release-level
// seam: only the external subscription and scoring websites are faked; the
// application, HTTP boundary, bundled-kernel validator, Test Channel, and
// publication path are the production implementations.
func TestProductionAssemblyPublishesThroughRealMihomo(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the deterministic production assembly gate runs with the pinned Windows Mihomo in CI")
	}
	mihomoPath := os.Getenv("NODEHARBOR_TEST_MIHOMO")
	if mihomoPath == "" {
		t.Skip("NODEHARBOR_TEST_MIHOMO is not set")
	}

	var subscriptionCalls, probeCalls, identityCalls, scoreCalls atomic.Int32
	externalServices := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/subscription":
			subscriptionCalls.Add(1)
			response.Header().Set("Content-Type", "application/yaml")
			_, _ = response.Write([]byte("proxies:\n  - name: Release Gate Proxy\n    type: direct\n"))
		case "/probe":
			probeCalls.Add(1)
			response.WriteHeader(http.StatusNoContent)
		case "/identity":
			identityCalls.Add(1)
			_, _ = response.Write([]byte("203.0.113.16\n"))
		case "/score":
			scoreCalls.Add(1)
			response.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = response.Write([]byte(`<html><body><div>综合安全分 99/100</div></body></html>`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer externalServices.Close()

	dependencies := app.DefaultDependenciesWithTestEndpoints(
		app.NewMihomoKernelWithBuild(mihomoPath, app.WindowsMihomoBuild),
		app.TestEndpointConfig{
			IPSuperEndpoint:      externalServices.URL + "/score",
			IPv4IdentityEndpoint: externalServices.URL + "/identity",
			IPv6IdentityEndpoint: externalServices.URL + "/identity-v6",
		},
	)
	assets := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte(`<!doctype html><div id="root"></div>`)}}
	instance, err := app.Open(context.Background(), app.Config{
		DatabasePath: filepath.Join(t.TempDir(), "nodeharbor.db"),
		WebAssets:    fs.FS(assets),
	}, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	server := httptest.NewServer(instance.Handler())
	defer server.Close()

	putJSON(t, server.URL+"/api/settings", map[string]any{
		"availabilityAttempts":          1,
		"availabilityRequiredSuccesses": 1,
		"availabilityTimeoutSeconds":    5,
		"availabilityMaxLatencyMs":      1500,
		"availabilityURLs":              []string{externalServices.URL + "/probe"},
		"evaluationWorkerCount":         1,
		"scoringJitterMs":               0,
	})
	created := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{
		"name": "Release Gate",
		"kind": "url",
		"url":  externalServices.URL + "/subscription",
	})
	if created.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(created.Body)
		_ = created.Body.Close()
		t.Fatalf("create URL Upstream Subscription status=%d body=%q", created.StatusCode, body)
	}
	_ = created.Body.Close()

	started := postJSONResponse(t, server.URL+"/api/evaluation-runs", map[string]any{})
	if started.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(started.Body)
		_ = started.Body.Close()
		t.Fatalf("start Evaluation Run status=%d body=%q", started.StatusCode, body)
	}
	var runID struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(started.Body).Decode(&runID); err != nil {
		_ = started.Body.Close()
		t.Fatal(err)
	}
	_ = started.Body.Close()

	var run struct {
		Status            string `json:"status"`
		Passed            int    `json:"passed"`
		PublicationResult string `json:"publicationResult"`
		Reason            string `json:"reason"`
		Results           []struct {
			State         string  `json:"state"`
			Attempts      int     `json:"attempts"`
			Successful    int     `json:"successful"`
			ExitIdentity  string  `json:"exitIdentity"`
			AddressFamily string  `json:"addressFamily"`
			IPScore       float64 `json:"ipScore"`
			ScoreSource   string  `json:"scoreSource"`
		} `json:"results"`
		Sources []struct {
			RefreshStatus  string `json:"refreshStatus"`
			ProxyNodeCount int    `json:"proxyNodeCount"`
		} `json:"sources"`
		Phases []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"phases"`
	}
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		getJSON(t, server.URL+"/api/evaluation-runs/"+runID.ID, &run)
		if run.Status != "running" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if run.Status != "completed" || run.Passed != 1 || run.PublicationResult != "published" || run.Reason != "" {
		t.Fatalf("production Evaluation Run=%+v", run)
	}
	if len(run.Sources) != 1 || run.Sources[0].RefreshStatus != "success" || run.Sources[0].ProxyNodeCount != 1 {
		t.Fatalf("source refresh outcome=%+v", run.Sources)
	}
	if len(run.Results) != 1 || run.Results[0].State != "passed" || run.Results[0].Attempts != 1 || run.Results[0].Successful != 1 || run.Results[0].ExitIdentity != "203.0.113.16" || run.Results[0].AddressFamily != "ipv4" || run.Results[0].IPScore != 99 || run.Results[0].ScoreSource != "provider" {
		t.Fatalf("production node outcome=%+v", run.Results)
	}
	wantPhases := []string{"refresh", "availability-and-scoring", "publication"}
	if len(run.Phases) != len(wantPhases) {
		t.Fatalf("production phases=%+v", run.Phases)
	}
	for index, want := range wantPhases {
		if run.Phases[index].Name != want || run.Phases[index].Status != "completed" {
			t.Fatalf("production phases=%+v", run.Phases)
		}
	}
	if subscriptionCalls.Load() == 0 || probeCalls.Load() == 0 || identityCalls.Load() == 0 || scoreCalls.Load() == 0 {
		t.Fatalf("production fake service calls: subscription=%d probe=%d identity=%d score=%d", subscriptionCalls.Load(), probeCalls.Load(), identityCalls.Load(), scoreCalls.Load())
	}

	response, err := http.Get(server.URL + "/sub/clash.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	document, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var publication struct {
		Proxies []map[string]any `yaml:"proxies"`
		Groups  []map[string]any `yaml:"proxy-groups"`
	}
	if err := yaml.Unmarshal(document, &publication); err != nil {
		t.Fatal(err)
	}
	if len(publication.Proxies) != 1 || publication.Proxies[0]["name"] != "[Release Gate] Release Gate Proxy" || len(publication.Groups) != 3 || !bytes.Contains(document, []byte("Release Gate Proxy")) {
		t.Fatalf("published production snapshot=%s", document)
	}
	if strings.Contains(string(document), "rules:") {
		t.Fatal("published production snapshot inherited upstream rules")
	}
}
