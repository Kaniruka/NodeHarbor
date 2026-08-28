package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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
	executablePath string
}

const MihomoVersion = "v1.19.30"
const MihomoExecutableSHA256 = "F55B3028D9160BEB9044F21B05DD7405B46524614A19642D6291492F5F985761"

func NewMihomoKernel(executablePath string) MihomoKernel {
	return MihomoKernel{executablePath: executablePath}
}

func (kernel MihomoKernel) Validate(ctx context.Context, document []byte) error {
	if kernel.executablePath == "" {
		return errors.New("Mihomo executable path is required")
	}
	if err := verifyMihomoExecutable(kernel.executablePath); err != nil {
		return err
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
	command := exec.CommandContext(ctx, kernel.executablePath, "-t", "-d", workingDirectory, "-f", configPath)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("Mihomo rejected publication: %w: %s", err, output)
	}
	return nil
}

func verifyMihomoExecutable(path string) error {
	executable, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open pinned Mihomo %s executable: %w", MihomoVersion, err)
	}
	defer executable.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, executable); err != nil {
		return fmt.Errorf("hash pinned Mihomo executable: %w", err)
	}
	actual := strings.ToUpper(fmt.Sprintf("%x", hash.Sum(nil)))
	if actual != MihomoExecutableSHA256 {
		return fmt.Errorf("Mihomo executable digest mismatch: got %s, want %s", actual, MihomoExecutableSHA256)
	}
	return nil
}
