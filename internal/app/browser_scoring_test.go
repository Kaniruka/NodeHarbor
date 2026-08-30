package app

import (
	"context"
	"strings"
	"testing"
)

type browserRuntimeFixture struct {
	pages       []BrowserPage
	proxy       string
	targets     []string
	closeCalled bool
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

func TestIPSuperBrowserScoringUsesRenderedTextInsteadOfHTMLTemplates(t *testing.T) {
	fixture := &browserRuntimeFixture{pages: []BrowserPage{
		{Status: 200, Text: "203.0.113.8"},
		{Status: 200, Text: "综合安全分 多源加权得到 58 /100", HTML: `<script>const example = "综合安全分 100/100"</script><main>综合安全分 多源加权得到 58 /100</main>`},
	}}
	provider := NewIPSuperProvider(nil)
	provider.Endpoint = "https://ipsuper.test"

	score, err := provider.ScoreWithBrowser(context.Background(), "203.0.113.8", "http://127.0.0.1:19090", fixture)
	if err != nil {
		t.Fatalf("ScoreWithBrowser() error = %v", err)
	}
	if score != 58 {
		t.Fatalf("score = %v, want visible score 58", score)
	}
}

func TestIPSuperBrowserScoringAllowsRenderedCardTextBetweenLabelAndValue(t *testing.T) {
	fixture := &browserRuntimeFixture{pages: []BrowserPage{
		{Status: 200, Text: "203.0.113.8"},
		{Status: 200, Text: "综合安全分\n多源加权得到\n越大越好\n58\n/100"},
	}}
	provider := NewIPSuperProvider(nil)
	provider.Endpoint = "https://ipsuper.test"

	score, err := provider.ScoreWithBrowser(context.Background(), "203.0.113.8", "http://127.0.0.1:19090", fixture)
	if err != nil {
		t.Fatalf("ScoreWithBrowser() error = %v", err)
	}
	if score != 58 {
		t.Fatalf("score = %v, want rendered card score 58", score)
	}
}

func TestIPSuperBrowserScoringIgnoresChallengePlatformScriptMarker(t *testing.T) {
	fixture := &browserRuntimeFixture{pages: []BrowserPage{
		{Status: 200, Text: "203.0.113.8"},
		{Status: 200, Text: "challenge-platform 综合安全分 86 /100"},
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
		{Status: 200, Text: "安全 威胁: 0| 种类: 0 简易检测 综合安全分 86 /100"},
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
