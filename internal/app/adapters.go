package app

import (
	"context"
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"
)

func DefaultDependencies() Dependencies {
	return Dependencies{
		Upstream:    UnavailableUpstream{},
		Scoring:     UnavailableScoringProvider{},
		Kernel:      YAMLKernel{},
		TestChannel: UnavailableTestChannel{},
	}
}

type UnavailableUpstream struct{}

func (UnavailableUpstream) Fetch(context.Context, string) ([]byte, error) {
	return nil, ErrUnavailable
}

type UnavailableScoringProvider struct{}

func (UnavailableScoringProvider) Score(context.Context, string) (float64, error) {
	return 0, ErrUnavailable
}

type UnavailableTestChannel struct{}

func (UnavailableTestChannel) Probe(context.Context, ProxyNode) (ProbeResult, error) {
	return ProbeResult{}, ErrUnavailable
}

// YAMLKernel is the development adapter used before a bundled Mihomo executable
// is supplied by platform packaging. It checks the structural invariants needed
// by NodeHarbor's initial publication; release adapters can replace it with an
// isolated Mihomo process without changing the application interface.
type YAMLKernel struct{}

func (YAMLKernel) Validate(_ context.Context, document []byte) error {
	var subscription struct {
		Proxies []map[string]any `yaml:"proxies"`
		Groups  []struct {
			Name    string   `yaml:"name"`
			Type    string   `yaml:"type"`
			Proxies []string `yaml:"proxies"`
		} `yaml:"proxy-groups"`
	}
	if err := yaml.Unmarshal(document, &subscription); err != nil {
		return fmt.Errorf("parse Clash/Mihomo YAML: %w", err)
	}
	if subscription.Proxies == nil {
		return errors.New("published subscription must contain proxies")
	}
	if len(subscription.Groups) != 3 {
		return errors.New("published subscription must contain AUTO, FALLBACK, and SELECT groups")
	}
	want := []struct{ name, kind string }{{"AUTO", "url-test"}, {"FALLBACK", "fallback"}, {"SELECT", "select"}}
	for index, expected := range want {
		group := subscription.Groups[index]
		if group.Name != expected.name || group.Type != expected.kind || len(group.Proxies) == 0 {
			return fmt.Errorf("invalid %s proxy group", expected.name)
		}
	}
	return nil
}
