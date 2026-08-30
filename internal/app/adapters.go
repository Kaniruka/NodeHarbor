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
	"time"

	"gopkg.in/yaml.v3"
)

func DefaultDependencies(kernel Kernel) Dependencies {
	ipsuper := NewIPSuperProvider(nil)
	testChannel := TestChannel(UnavailableTestChannel{})
	if mihomo, ok := kernel.(MihomoKernel); ok {
		testChannel = NewMihomoTestChannel(mihomo.executablePath, mihomo.build)
	}
	return Dependencies{
		Upstream:         NewHTTPUpstream(30 * time.Second),
		Scoring:          ipsuper,
		ScoringProviders: map[string]ScoringProvider{"ipsuper": ipsuper},
		Kernel:           kernel,
		TestChannel:      testChannel,
	}
}

// TestEndpointConfig is an explicit opt-in configuration for deterministic
// package smoke tests. Normal application assembly always uses the real
// provider and Exit Identity endpoints.
type TestEndpointConfig struct {
	IPSuperEndpoint      string
	IPv4IdentityEndpoint string
	IPv6IdentityEndpoint string
}

func DefaultDependenciesWithTestEndpoints(kernel Kernel, config TestEndpointConfig) Dependencies {
	dependencies := DefaultDependencies(kernel)
	if config.IPSuperEndpoint != "" {
		if provider, ok := dependencies.ScoringProviders["ipsuper"].(IPSuperProvider); ok {
			provider.Endpoint = config.IPSuperEndpoint
			dependencies.ScoringProviders["ipsuper"] = provider
		}
	}
	if provider, ok := dependencies.ScoringProviders["ipsuper"].(IPSuperProvider); ok {
		provider.IPv4IdentityEndpoint = endpointOrDefault(config.IPv4IdentityEndpoint, ipv4IdentityEndpoint)
		provider.IPv6IdentityEndpoint = endpointOrDefault(config.IPv6IdentityEndpoint, ipv6IdentityEndpoint)
		dependencies.ScoringProviders["ipsuper"] = provider
	}
	if channel, ok := dependencies.TestChannel.(*MihomoTestChannel); ok {
		channel.ipv4IdentityEndpoint = endpointOrDefault(config.IPv4IdentityEndpoint, ipv4IdentityEndpoint)
		channel.ipv6IdentityEndpoint = endpointOrDefault(config.IPv6IdentityEndpoint, ipv6IdentityEndpoint)
	}
	return dependencies
}

func endpointOrDefault(endpoint, fallback string) string {
	if endpoint == "" {
		return fallback
	}
	return endpoint
}

type UnavailableUpstream struct{}

func (UnavailableUpstream) Fetch(context.Context, UpstreamRequest) ([]byte, error) {
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
	build          MihomoBuild
}

type MihomoBuild struct {
	Platform          string
	Version           string
	Asset             string
	ArchiveSHA256     string
	ExecutableSHA256  string
	LicenseIdentifier string
}

var WindowsMihomoBuild = MihomoBuild{
	Platform:          "windows-amd64",
	Version:           "v1.19.30",
	Asset:             "mihomo-windows-amd64-v1.19.30.zip",
	ArchiveSHA256:     "22C09FD67673895EF7CD6B1820563918275C3D316F2462B306208675118DB3C0",
	ExecutableSHA256:  "F55B3028D9160BEB9044F21B05DD7405B46524614A19642D6291492F5F985761",
	LicenseIdentifier: "GPL-3.0-or-later",
}

const MihomoVersion = "v1.19.30"

func MihomoBuildForPlatform(platform string) (MihomoBuild, error) {
	switch platform {
	case "windows":
		return WindowsMihomoBuild, nil
	default:
		return MihomoBuild{}, fmt.Errorf("unsupported Mihomo platform %q", platform)
	}
}

func NewMihomoKernel(executablePath string) MihomoKernel {
	return NewMihomoKernelWithBuild(executablePath, WindowsMihomoBuild)
}

func NewMihomoKernelWithBuild(executablePath string, build MihomoBuild) MihomoKernel {
	return MihomoKernel{executablePath: executablePath, build: build}
}

func (kernel MihomoKernel) Validate(ctx context.Context, document []byte) error {
	return kernel.validateDocument(ctx, document, "publication")
}

func (kernel MihomoKernel) ValidateNode(ctx context.Context, node ProxyNode) error {
	document, err := yaml.Marshal(struct {
		Proxies []map[string]any `yaml:"proxies"`
	}{Proxies: []map[string]any{node.Config}})
	if err != nil {
		return fmt.Errorf("serialize Proxy Node %q: %w", node.Name, err)
	}
	return kernel.validateDocument(ctx, document, fmt.Sprintf("Proxy Node %q", node.Name))
}

func (kernel MihomoKernel) validateDocument(ctx context.Context, document []byte, subject string) error {
	if kernel.executablePath == "" {
		return errors.New("Mihomo executable path is required")
	}
	if err := verifyMihomoExecutable(kernel.executablePath, kernel.build); err != nil {
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
		return fmt.Errorf("Mihomo rejected %s: %w: %s", subject, err, output)
	}
	return nil
}

func verifyMihomoExecutable(path string, build MihomoBuild) error {
	executable, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open pinned Mihomo %s executable for %s: %w", build.Version, build.Platform, err)
	}
	defer executable.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, executable); err != nil {
		return fmt.Errorf("hash pinned Mihomo executable: %w", err)
	}
	actual := strings.ToUpper(fmt.Sprintf("%x", hash.Sum(nil)))
	if actual != strings.ToUpper(build.ExecutableSHA256) {
		return fmt.Errorf("Mihomo executable digest mismatch for %s: got %s, want %s", build.Platform, actual, strings.ToUpper(build.ExecutableSHA256))
	}
	return nil
}
