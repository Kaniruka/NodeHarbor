package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIPCheckProviderParsesFixedJSONScoreThroughTheSuppliedClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("ip"); got != "203.0.113.8" {
			t.Fatalf("ip query = %q", got)
		}
		_, _ = w.Write([]byte(`{"score":87.5,"provider":"fixture"}`))
	}))
	defer server.Close()

	provider := IPCheckProvider{Client: server.Client(), Endpoint: server.URL + "/score", Timeout: time.Second}
	score, err := provider.ScoreWithClient(context.Background(), "203.0.113.8", server.Client())
	if err != nil || score != 87.5 {
		t.Fatalf("score=%v err=%v", score, err)
	}
}

func TestIPCheckProviderBoundsAChannelClientWithoutItsOwnTimeout(t *testing.T) {
	provider := IPCheckProvider{
		Endpoint: "https://ipcheck.example/score",
		Timeout:  10 * time.Millisecond,
	}
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}

	started := time.Now()
	_, err := provider.ScoreWithClient(context.Background(), "203.0.113.8", client)
	if err == nil || !errors.Is(err, errScoringProviderUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error=%v, want provider unavailable", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("provider request was not bounded: %s", elapsed)
	}
}

func TestIPCheckProviderMapsUnstableResponsesToProviderUnavailable(t *testing.T) {
	for _, testCase := range []struct {
		name, body string
		status     int
	}{
		{name: "non-success response", body: `{"score":91}`, status: http.StatusServiceUnavailable},
		{name: "challenge", body: `<html><body>Checking your browser before accessing</body></html>`, status: http.StatusOK},
		{name: "rejection", body: `{"status":"rejected","score":91}`, status: http.StatusOK},
		{name: "parse failure", body: `{"message":"temporarily unavailable"}`, status: http.StatusOK},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(testCase.status)
				_, _ = w.Write([]byte(testCase.body))
			}))
			defer server.Close()

			_, err := (IPCheckProvider{Client: server.Client(), Endpoint: server.URL, Timeout: time.Second}).Score(context.Background(), "203.0.113.8")
			if err == nil || !errors.Is(err, errScoringProviderUnavailable) {
				t.Fatalf("error=%v, want provider unavailable", err)
			}
		})
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (roundTripper roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTripper(request)
}
