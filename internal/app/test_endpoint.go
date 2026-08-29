package app

import (
	"encoding/json"
	"errors"
	"net/http"

	"gopkg.in/yaml.v3"
)

// handleTestEvaluation is registered only by integration-test assembly. It is
// the shared black-box entry point for exercising replaceable evaluation adapters.
func (application *Application) handleTestEvaluation(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Upstream string `json:"upstream"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 4096)).Decode(&input); err != nil || input.Upstream == "" {
		writeError(response, http.StatusBadRequest, errors.New("upstream is required"))
		return
	}
	document, err := application.dependencies.Upstream.Fetch(request.Context(), UpstreamRequest{Location: input.Upstream})
	if err != nil {
		writeError(response, http.StatusBadGateway, err)
		return
	}
	if err := application.dependencies.Kernel.Validate(request.Context(), document); err != nil {
		writeError(response, http.StatusUnprocessableEntity, err)
		return
	}
	node, err := firstProxyNode(document)
	if err != nil {
		writeError(response, http.StatusUnprocessableEntity, err)
		return
	}
	probe, err := application.dependencies.TestChannel.Probe(request.Context(), node)
	if err != nil {
		writeError(response, http.StatusBadGateway, err)
		return
	}
	candidates := probe.ExitIdentities
	if len(candidates) == 0 && probe.ExitIdentity != "" {
		// Probe predates the candidate contract; legacy adapters must explicitly
		// mark the single observed identity as verified.
		candidates = []ExitIdentityCandidate{{IP: probe.ExitIdentity, Verified: probe.Verified}}
	}
	exitIdentity, family, err := selectExitIdentity(candidates)
	if err != nil {
		writeError(response, http.StatusBadGateway, err)
		return
	}
	if exitIdentity == "" {
		writeError(response, http.StatusBadGateway, errors.New("no_exit_identity: Test Channel returned no exit identity"))
		return
	}
	provider, ok := application.dependencies.Scoring.(ChannelScoringProvider)
	if !ok {
		writeError(response, http.StatusBadGateway, errors.New("Scoring Provider cannot bind requests to the verified Test Channel"))
		return
	}
	transport, ok := application.dependencies.TestChannel.(TestChannelHTTPClient)
	if !ok {
		writeError(response, http.StatusBadGateway, errors.New("Test Channel cannot provide scoring transport"))
		return
	}
	client, err := transport.HTTPClient(request.Context(), node)
	if err != nil {
		writeError(response, http.StatusBadGateway, err)
		return
	}
	score, err := provider.ScoreWithClient(request.Context(), exitIdentity, client)
	if err != nil {
		writeError(response, http.StatusBadGateway, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"node":          node.Name,
		"exitIdentity":  exitIdentity,
		"addressFamily": family,
		"score":         score,
	})
}

func firstProxyNode(document []byte) (ProxyNode, error) {
	var subscription struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(document, &subscription); err != nil {
		return ProxyNode{}, err
	}
	if len(subscription.Proxies) == 0 {
		return ProxyNode{}, errors.New("upstream subscription contains no Proxy Nodes")
	}
	name, _ := subscription.Proxies[0]["name"].(string)
	if name == "" {
		return ProxyNode{}, errors.New("Proxy Node name is required")
	}
	return ProxyNode{Name: name, Config: subscription.Proxies[0]}, nil
}
