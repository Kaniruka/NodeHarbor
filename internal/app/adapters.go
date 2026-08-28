package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

func DefaultDependencies(kernel Kernel) Dependencies {
	return Dependencies{
		Upstream:    UnavailableUpstream{},
		Scoring:     UnavailableScoringProvider{},
		Kernel:      kernel,
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

// StructuralYAMLValidator is a lightweight adapter for tests that do not need
// to start Mihomo. Production assembly uses MihomoKernel.
type StructuralYAMLValidator struct{}

func (StructuralYAMLValidator) Validate(_ context.Context, document []byte) error {
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

// MihomoKernel validates a publication with the exact NodeHarbor-owned Mihomo
// executable configured by the platform assembly.
type MihomoKernel struct {
	ExecutablePath string
}

func (kernel MihomoKernel) Validate(ctx context.Context, document []byte) error {
	if kernel.ExecutablePath == "" {
		return errors.New("Mihomo executable path is required")
	}
	workingDirectory, err := os.MkdirTemp("", "nodeharbor-mihomo-validation-")
	if err != nil {
		return fmt.Errorf("create Mihomo validation directory: %w", err)
	}
	defer os.RemoveAll(workingDirectory)
	configPath := filepath.Join(workingDirectory, "clash.yaml")
	if err := os.WriteFile(configPath, document, 0o600); err != nil {
		return fmt.Errorf("write Mihomo validation input: %w", err)
	}
	command := exec.CommandContext(ctx, kernel.ExecutablePath, "-t", "-d", workingDirectory, "-f", configPath)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("Mihomo rejected publication: %w: %s", err, output)
	}
	return nil
}
