package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIPSuperProviderParsesObservedPublicQueryPageThroughSuppliedClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" || r.URL.Query().Get("ip") != "1.1.1.1" {
			t.Fatalf("request=%s", r.URL.String())
		}
		_, _ = w.Write([]byte("<main>综合安全分 <strong>83</strong>/100</main>"))
	}))
	defer server.Close()

	provider := IPSuperProvider{Client: server.Client(), Endpoint: server.URL, Timeout: time.Second}
	score, err := provider.ScoreWithClient(context.Background(), "1.1.1.1", server.Client())
	if err != nil || score != 83 {
		t.Fatalf("score=%v err=%v", score, err)
	}
}

func TestIPSuperProviderBoundsChannelClientWithoutItsOwnTimeout(t *testing.T) {
	provider := IPSuperProvider{Endpoint: "https://ipsuper.example", Timeout: 10 * time.Millisecond}
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	_, err := provider.ScoreWithClient(context.Background(), "203.0.113.8", client)
	if err == nil || !errors.Is(err, errScoringProviderUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error=%v, want provider unavailable", err)
	}
}

func TestIPSuperProviderMapsUnstableResponsesToProviderUnavailable(t *testing.T) {
	for _, testCase := range []struct {
		name, body string
		status     int
	}{
		{name: "unauthorized", status: http.StatusUnauthorized},
		{name: "forbidden", status: http.StatusForbidden},
		{name: "challenge", body: "<html>Checking your browser</html>", status: http.StatusOK},
		{name: "missing score", body: "<html>no result</html>", status: http.StatusOK},
		{name: "out of range", body: "综合安全分 101/100", status: http.StatusOK},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(testCase.status)
				_, _ = w.Write([]byte(testCase.body))
			}))
			defer server.Close()
			_, err := (IPSuperProvider{Client: server.Client(), Endpoint: server.URL, Timeout: time.Second}).Score(context.Background(), "203.0.113.8")
			if err == nil || !errors.Is(err, errScoringProviderUnavailable) {
				t.Fatalf("error=%v, want provider unavailable", err)
			}
		})
	}
}

func TestIPSuperProviderMapsExpiredSessionToProviderUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"session expired"}`))
	}))
	defer server.Close()
	_, err := (IPSuperProvider{Client: server.Client(), Endpoint: server.URL, Timeout: time.Second}).Score(context.Background(), "203.0.113.8")
	if err == nil || !errors.Is(err, errScoringProviderUnavailable) || !strings.Contains(err.Error(), "session expired") {
		t.Fatalf("error=%v, want expired session provider unavailable", err)
	}
}

func TestIPSuperProviderIncludesHTTPStatusAndDetail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited by provider"}`))
	}))
	defer server.Close()
	_, err := (IPSuperProvider{Client: server.Client(), Endpoint: server.URL, Timeout: time.Second}).Score(context.Background(), "203.0.113.8")
	if err == nil || !strings.Contains(err.Error(), "HTTP 429") || !strings.Contains(err.Error(), "rate limited by provider") {
		t.Fatalf("error=%v, want availability diagnostic", err)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (roundTripper roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTripper(request)
}
