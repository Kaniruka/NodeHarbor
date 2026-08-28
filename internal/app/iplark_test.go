package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
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
		{name: "changed response", body: `{"status":"success","data":{"value":"unknown"}}`, status: 200, wantErr: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(testCase.status)
				_, _ = w.Write([]byte(testCase.body))
			}))
			defer server.Close()
			score, err := (IPLarkProvider{Client: server.Client(), Endpoint: server.URL}).Score(context.Background(), "2001:db8::1")
			if testCase.wantErr && err == nil {
				t.Fatal("expected provider unavailable error")
			}
			if !testCase.wantErr && (err != nil || score != testCase.want) {
				t.Fatalf("score=%v err=%v", score, err)
			}
		})
	}
}
