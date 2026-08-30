package app

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

type browserRuntimeFixture struct {
	pages       []BrowserPage
	proxy       string
	targets     []string
	closeCalled bool
}

type browserSearchRuntimeFixture struct {
	browserRuntimeFixture
	searchTarget string
}

func (fixture *browserSearchRuntimeFixture) FetchWithInput(_ context.Context, proxy, identityTarget, searchTarget, input string) ([]BrowserPage, error) {
	fixture.proxy = proxy
	fixture.searchTarget = searchTarget
	fixture.targets = []string{identityTarget, searchTarget, input}
	return fixture.pages, nil
}

func (fixture *browserRuntimeFixture) Fetch(_ context.Context, proxy string, targets []string) ([]BrowserPage, error) {
	fixture.proxy = proxy
	fixture.targets = append([]string(nil), targets...)
	return fixture.pages, nil
}

func (fixture *browserRuntimeFixture) Close() error {
	fixture.closeCalled = true
	return nil
}

func TestIPLarkBrowserScoringUsesOneProxyBoundSession(t *testing.T) {
	fixture := &browserRuntimeFixture{pages: []BrowserPage{
		{URL: "http://identity.test", Status: 200, Text: "203.0.113.8"},
		{URL: "http://iplark.test/ipscore?ip=203.0.113.8", Status: 200, Text: `<div>IP Score <strong>87</strong></div>`},
	}}
	provider := NewIPLarkProvider(nil)
	provider.Endpoint = "https://iplark.test/ipscore"
	provider.IPv4IdentityEndpoint = "https://identity.test/ip"

	score, err := provider.ScoreWithBrowser(context.Background(), "203.0.113.8", "http://127.0.0.1:19090", fixture)
	if err != nil {
		t.Fatal(err)
	}
	if score != 87 {
		t.Fatalf("score = %v, want 87", score)
	}
	if fixture.proxy != "http://127.0.0.1:19090" || len(fixture.targets) != 2 {
		t.Fatalf("browser session = proxy %q targets %v", fixture.proxy, fixture.targets)
	}
	if !strings.Contains(fixture.targets[1], "ip=203.0.113.8") {
		t.Fatalf("score target did not preserve IP query: %q", fixture.targets[1])
	}
}

func TestIPLarkBrowserScoringUsesRenderedSearchResultPath(t *testing.T) {
	fixture := &browserRuntimeFixture{pages: []BrowserPage{
		{URL: "https://api4.ipify.org", Status: 200, Text: "203.0.113.8"},
		{URL: "https://iplark.com/203.0.113.8", Status: 200, Text: "IP评分 99/100"},
	}}
	provider := NewIPLarkProvider(nil)

	score, err := provider.ScoreWithBrowser(context.Background(), "203.0.113.8", "http://127.0.0.1:19090", fixture)
	if err != nil {
		t.Fatal(err)
	}
	if score != 99 {
		t.Fatalf("score = %v, want 99", score)
	}
	if len(fixture.targets) != 2 || fixture.targets[1] != "https://iplark.com/203.0.113.8" {
		t.Fatalf("IPLark targets = %v, want rendered search result path", fixture.targets)
	}
}

func TestIPLarkBrowserScoringUsesRenderedSearchFlowWhenAvailable(t *testing.T) {
	fixture := &browserSearchRuntimeFixture{browserRuntimeFixture: browserRuntimeFixture{pages: []BrowserPage{
		{URL: "https://api4.ipify.org", Status: 200, Text: "203.0.113.8"},
		{URL: "https://iplark.com/203.0.113.8", Status: 200, Text: "IP评分 88/100"},
	}}}
	provider := NewIPLarkProvider(nil)

	score, err := provider.ScoreWithBrowser(context.Background(), "203.0.113.8", "http://127.0.0.1:19090", fixture)
	if err != nil {
		t.Fatal(err)
	}
	if score != 88 || fixture.searchTarget != "https://iplark.com/search" {
		t.Fatalf("score = %v, search target = %q", score, fixture.searchTarget)
	}
}

func TestIPLarkBrowserScoringAcceptsRenderedScoreFromNonOKDocument(t *testing.T) {
	runtime := &browserRuntimeFixture{pages: []BrowserPage{
		{URL: "https://api4.ipify.org", Status: 200, Text: "203.0.113.8"},
		{URL: "https://iplark.com/203.0.113.8", Status: 404, Text: "IP评分 49"},
	}}
	provider := NewIPLarkProvider(http.DefaultClient)
	provider.Endpoint = "https://iplark.com"

	score, err := provider.ScoreWithBrowser(context.Background(), "203.0.113.8", "http://127.0.0.1:19090", runtime)
	if err != nil {
		t.Fatalf("ScoreWithBrowser() error = %v", err)
	}
	if score != 49 {
		t.Fatalf("score = %v, want 49", score)
	}
}

func TestIPLarkBrowserScoringReportsChallengeResponse(t *testing.T) {
	fixture := &browserRuntimeFixture{pages: []BrowserPage{
		{Status: 200, Text: "203.0.113.8"},
		{Status: 200, Text: "setTimeout(() => location.reload(),5000);"},
	}}
	provider := NewIPLarkProvider(nil)
	provider.Endpoint = "https://iplark.com"

	_, err := provider.ScoreWithBrowser(context.Background(), "203.0.113.8", "http://127.0.0.1:19090", fixture)
	if err == nil || !strings.Contains(err.Error(), "IPLark provider unavailable: challenge response") {
		t.Fatalf("error = %v, want challenge response", err)
	}
}

func TestIPSuperBrowserScoringRejectsIdentityMismatch(t *testing.T) {
	fixture := &browserRuntimeFixture{pages: []BrowserPage{
		{Status: 200, Text: "198.51.100.4"},
		{Status: 200, Text: "aggregate security score 91/100"},
	}}
	provider := NewIPSuperProvider(nil)
	provider.Endpoint = "https://ipsuper.test"
	provider.IPv4IdentityEndpoint = "https://identity.test/ip"

	_, err := provider.ScoreWithBrowser(context.Background(), "203.0.113.8", "http://127.0.0.1:19091", fixture)
	if err == nil || !strings.Contains(err.Error(), "Exit Identity verification failed") {
		t.Fatalf("error = %v, want identity verification failure", err)
	}
}

func TestIPSuperBrowserScoringIgnoresChallengePlatformScriptMarker(t *testing.T) {
	fixture := &browserRuntimeFixture{pages: []BrowserPage{
		{Status: 200, Text: "203.0.113.8"},
		{Status: 200, Text: "challenge-platform 综合安全分 多源加权得到 86 /100"},
	}}
	provider := NewIPSuperProvider(nil)
	provider.Endpoint = "https://ipsuper.test"

	score, err := provider.ScoreWithBrowser(context.Background(), "203.0.113.8", "http://127.0.0.1:19090", fixture)
	if err != nil {
		t.Fatalf("ScoreWithBrowser() error = %v", err)
	}
	if score != 86 {
		t.Fatalf("score = %v, want 86", score)
	}
}

func TestIPSuperBrowserScoringReadsAggregateScoreAfterThreatCounters(t *testing.T) {
	fixture := &browserRuntimeFixture{pages: []BrowserPage{
		{Status: 200, Text: "203.0.113.8"},
		{Status: 200, Text: "综合安全分 安全 威胁: 0| 种类: 0 简易检测 综合安全分 多源加权得到 越大越好 86 /100"},
	}}
	provider := NewIPSuperProvider(nil)
	provider.Endpoint = "https://ipsuper.test"

	score, err := provider.ScoreWithBrowser(context.Background(), "203.0.113.8", "http://127.0.0.1:19090", fixture)
	if err != nil {
		t.Fatalf("ScoreWithBrowser() error = %v", err)
	}
	if score != 86 {
		t.Fatalf("score = %v, want 86", score)
	}
}

func TestBrowserScoringRejectsChallengeResponse(t *testing.T) {
	fixture := &browserRuntimeFixture{pages: []BrowserPage{
		{Status: 200, Text: "203.0.113.8"},
		{Status: 403, Text: "checking your browser"},
	}}
	provider := NewIPSuperProvider(nil)
	provider.Endpoint = "https://ipsuper.test"
	provider.IPv4IdentityEndpoint = "https://identity.test/ip"

	_, err := provider.ScoreWithBrowser(context.Background(), "203.0.113.8", "http://127.0.0.1:19092", fixture)
	if err == nil || !strings.Contains(err.Error(), "IPSuper provider unavailable: HTTP 403") {
		t.Fatalf("error = %v, want HTTP 403 provider failure", err)
	}
}
