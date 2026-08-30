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
	kernel := app.NewMihomoKernelWithBuild(path, app.WindowsMihomoBuild)

	err := kernel.Validate(context.Background(), []byte("proxies: []\n"))
	if err == nil || !strings.Contains(err.Error(), "windows-amd64") || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("Validate error = %v, want pinned digest mismatch", err)
	}
}

func TestPinnedMihomoBuildsHaveTargetSpecificMetadata(t *testing.T) {
	windows, err := app.MihomoBuildForPlatform("windows")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.MihomoBuildForPlatform("linux"); err == nil {
		t.Fatal("unsupported platform was accepted")
	}
	if windows.Platform != app.WindowsMihomoBuild.Platform {
		t.Fatalf("platform selection returned wrong metadata: windows=%+v", windows)
	}
	if windows.Version == "" || windows.LicenseIdentifier == "" {
		t.Fatalf("pinned metadata is incomplete: windows=%+v", windows)
	}
}

func TestDefaultDependenciesUseOwnedMihomoTestChannel(t *testing.T) {
	dependencies := app.DefaultDependencies(app.NewMihomoKernelWithBuild("nodeharbor-core", app.WindowsMihomoBuild))
	if _, ok := dependencies.TestChannel.(*app.MihomoTestChannel); !ok {
		t.Fatalf("default Test Channel=%T, want *app.MihomoTestChannel", dependencies.TestChannel)
	}
}

func TestDefaultDependenciesUseConfiguredSmokeEndpoints(t *testing.T) {
	dependencies := app.DefaultDependenciesWithTestEndpoints(
		app.NewMihomoKernelWithBuild("nodeharbor-core", app.WindowsMihomoBuild),
		app.TestEndpointConfig{
			IPSuperEndpoint:      "http://127.0.0.1:19001/score",
			IPv4IdentityEndpoint: "http://127.0.0.1:19001/identity",
			IPv6IdentityEndpoint: "http://127.0.0.1:19001/identity-v6",
		},
	)
	provider, ok := dependencies.ScoringProviders["ipsuper"].(app.IPSuperProvider)
	if !ok {
		t.Fatalf("IPSuper provider=%T, want app.IPSuperProvider", dependencies.ScoringProviders["ipsuper"])
	}
	if provider.Endpoint != "http://127.0.0.1:19001/score" {
		t.Fatalf("IPSuper endpoint=%q", provider.Endpoint)
	}
}
