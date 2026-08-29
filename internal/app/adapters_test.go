package app_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Kaniruka/NodeHarbor/internal/app"
)

func TestMihomoKernelRejectsExecutableWithWrongPinnedDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodeharbor-core")
	if err := os.WriteFile(path, []byte("not the pinned core"), 0o700); err != nil {
		t.Fatal(err)
	}
	kernel := app.NewMihomoKernelWithBuild(path, app.KernelSUMihomoBuild)

	err := kernel.Validate(context.Background(), []byte("proxies: []\n"))
	if err == nil || !strings.Contains(err.Error(), "android-arm64-v8") || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("Validate error = %v, want pinned digest mismatch", err)
	}
}

func TestPinnedMihomoBuildsHaveTargetSpecificMetadata(t *testing.T) {
	windows, err := app.MihomoBuildForPlatform("windows")
	if err != nil {
		t.Fatal(err)
	}
	android, err := app.MihomoBuildForPlatform("android")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.MihomoBuildForPlatform("linux"); err == nil {
		t.Fatal("unsupported platform was accepted")
	}
	if windows.Platform != app.WindowsMihomoBuild.Platform || android.Platform != app.KernelSUMihomoBuild.Platform {
		t.Fatalf("platform selection returned wrong metadata: windows=%+v android=%+v", windows, android)
	}
	if app.WindowsMihomoBuild.Platform == app.KernelSUMihomoBuild.Platform || app.WindowsMihomoBuild.ExecutableSHA256 == app.KernelSUMihomoBuild.ExecutableSHA256 {
		t.Fatalf("target metadata is not platform-specific: windows=%+v kernelsu=%+v", app.WindowsMihomoBuild, app.KernelSUMihomoBuild)
	}
	if app.WindowsMihomoBuild.Version == "" || app.KernelSUMihomoBuild.Version == "" || app.WindowsMihomoBuild.LicenseIdentifier == "" || app.KernelSUMihomoBuild.LicenseIdentifier == "" {
		t.Fatalf("pinned metadata is incomplete: windows=%+v kernelsu=%+v", app.WindowsMihomoBuild, app.KernelSUMihomoBuild)
	}
}

func TestDefaultDependenciesUseOwnedMihomoTestChannel(t *testing.T) {
	dependencies := app.DefaultDependencies(app.NewMihomoKernelWithBuild("nodeharbor-core", app.WindowsMihomoBuild))
	if _, ok := dependencies.TestChannel.(*app.MihomoTestChannel); !ok {
		t.Fatalf("default Test Channel=%T, want *app.MihomoTestChannel", dependencies.TestChannel)
	}
}

func TestDefaultDependenciesAlwaysProvideAnIsolationResult(t *testing.T) {
	dependencies := app.DefaultDependencies(app.NewMihomoKernelWithBuild("nodeharbor-core", app.WindowsMihomoBuild))
	if dependencies.Isolation == nil {
		t.Fatal("default production dependencies have no Surfing isolation result")
	}
	status, err := dependencies.Isolation.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Verified || status.Mode != "inactive" {
		t.Fatalf("default Windows isolation status=%+v, want verified inactive", status)
	}
}

func TestDefaultDependenciesUseConfiguredSmokeEndpoints(t *testing.T) {
	dependencies := app.DefaultDependenciesWithTestEndpoints(
		app.NewMihomoKernelWithBuild("nodeharbor-core", app.WindowsMihomoBuild),
		app.TestEndpointConfig{
			IPLarkEndpoint:       "http://127.0.0.1:19001/score",
			IPv4IdentityEndpoint: "http://127.0.0.1:19001/identity",
			IPv6IdentityEndpoint: "http://127.0.0.1:19001/identity-v6",
		},
	)
	provider, ok := dependencies.ScoringProviders["iplark"].(app.IPLarkProvider)
	if !ok {
		t.Fatalf("IPLark provider=%T, want app.IPLarkProvider", dependencies.ScoringProviders["iplark"])
	}
	if provider.Endpoint != "http://127.0.0.1:19001/score" {
		t.Fatalf("IPLark endpoint=%q", provider.Endpoint)
	}
}
