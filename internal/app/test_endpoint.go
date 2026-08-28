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
	score, err := application.dependencies.Scoring.Score(request.Context(), probe.ExitIdentity)
	if err != nil {
		writeError(response, http.StatusBadGateway, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"node":         node.Name,
		"exitIdentity": probe.ExitIdentity,
		"score":        score,
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
