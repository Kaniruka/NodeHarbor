package app_test

import (
	"encoding/json"
	"testing"
	"time"
)

func TestPublicationEndpointReturnsAtomicGroupedSnapshot(t *testing.T) {
	upstream := &recordingUpstream{document: []byte("proxies:\n  - name: tokyo\n    type: vless\n    server: tokyo.example\n    port: 443\n")}
	server := openEvaluationApplication(t, upstream, &recordingKernel{}, &availabilityChannel{verified: true, latencies: []time.Duration{120 * time.Millisecond}})
	created := postJSONResponse(t, server.URL+"/api/upstream-subscriptions", map[string]any{"name": "家庭来源", "kind": "url", "url": "https://source.example/sub"})
	if created.StatusCode != 201 {
		t.Fatalf("create subscription status = %d", created.StatusCode)
	}
	var subscription struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(created.Body).Decode(&subscription); err != nil {
		t.Fatal(err)
	}
	_ = created.Body.Close()
	runEvaluation(t, server.URL, map[string]any{})

	var publication struct {
		Status string `json:"status"`
		Groups []struct {
			SubscriptionName string `json:"subscriptionName"`
			Nodes            []struct {
				Name            string         `json:"name"`
				Config          map[string]any `json:"config"`
				MedianLatencyMS float64        `json:"medianLatencyMs"`
			} `json:"nodes"`
		} `json:"groups"`
	}
	getJSON(t, server.URL+"/api/publication", &publication)
	if publication.Status != "published" || len(publication.Groups) != 1 || publication.Groups[0].SubscriptionName != "家庭来源" || len(publication.Groups[0].Nodes) != 1 {
		t.Fatalf("publication = %+v", publication)
	}
	node := publication.Groups[0].Nodes[0]
	if node.Name != "[家庭来源] tokyo" || node.Config["type"] != "vless" || node.MedianLatencyMS != 120 {
		t.Fatalf("published node = %+v", node)
	}
}
