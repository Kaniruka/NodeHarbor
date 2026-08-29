package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIPLarkProviderParsesJSONScoreWithoutLeakingResponseShape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ip") != "203.0.113.8" {
			t.Fatalf("ip query = %q", r.URL.Query().Get("ip"))
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"ip_score":87.5,"diagnostics":{"vendor":"fixture"}}}`))
	}))
	defer server.Close()
	provider := IPLarkProvider{Client: server.Client(), Endpoint: server.URL + "/ipscore"}
	score, err := provider.Score(context.Background(), "203.0.113.8")
	if err != nil || score != 87.5 {
		t.Fatalf("score=%v err=%v", score, err)
	}
}

func TestIPLarkProviderParsesHTMLAndMapsFailures(t *testing.T) {
	for _, testCase := range []struct {
		name, body string
		status     int
		want       float64
		wantErr    bool
	}{
		{name: "html", body: `<div>IP Score <strong>91</strong></div>`, status: 200, want: 91},
		{name: "forbidden", body: `captcha`, status: http.StatusForbidden, wantErr: true},
		{name: "rate limited", body: `too many requests`, status: http.StatusTooManyRequests, wantErr: true},
		{name: "challenge", body: `<html><title>Just a moment...</title><body>checking your browser</body></html>`, status: 200, wantErr: true},
		{name: "changed response", body: `{"status":"success","data":{"value":"unknown"}}`, status: 200, wantErr: true},
		{name: "error metadata score", body: `{"status":"error","data":{"metadata":{"score":88}}}`, status: 200, wantErr: true},
		{name: "parse failure", body: `not an IP score`, status: 200, wantErr: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(testCase.status)
				_, _ = w.Write([]byte(testCase.body))
			}))
			defer server.Close()
			score, err := (IPLarkProvider{Client: server.Client(), Endpoint: server.URL}).Score(context.Background(), "2001:db8::1")
			if testCase.wantErr {
				if err == nil {
					t.Fatal("expected provider unavailable error")
				}
				if !errors.Is(err, errScoringProviderUnavailable) {
					t.Fatalf("error=%v is not classified as provider unavailable", err)
				}
			}
			if !testCase.wantErr && (err != nil || score != testCase.want) {
				t.Fatalf("score=%v err=%v", score, err)
			}
		})
	}
}

func TestIPLarkProviderMapsTimeoutToProviderUnavailable(t *testing.T) {
	provider := IPLarkProvider{
		Client: &http.Client{Transport: iplarkRoundTripper(func(request *http.Request) (*http.Response, error) {
			return nil, context.DeadlineExceeded
		})},
		Endpoint: "https://iplark.example/ipscore",
	}

	_, err := provider.Score(context.Background(), "203.0.113.8")
	if err == nil || !errors.Is(err, errScoringProviderUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error=%v, want provider unavailable", err)
	}
}

func TestIPLarkProviderBoundsChannelClientWithoutTimeout(t *testing.T) {
	provider := IPLarkProvider{
		Client: &http.Client{Transport: iplarkRoundTripper(func(request *http.Request) (*http.Response, error) {
			<-request.Context().Done()
			return nil, request.Context().Err()
		})},
		Endpoint: "https://iplark.example/ipscore",
		Timeout:  10 * time.Millisecond,
	}
	started := time.Now()
	_, err := provider.ScoreWithClient(context.Background(), "203.0.113.8", provider.Client)
	if err == nil || !errors.Is(err, errScoringProviderUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded timeout error=%v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("provider request was not bounded: %s", elapsed)
	}
}

type iplarkRoundTripper func(*http.Request) (*http.Response, error)

func (roundTripper iplarkRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTripper(request)
}
