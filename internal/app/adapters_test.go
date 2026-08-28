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
	kernel := app.NewMihomoKernelWithBuild(path, app.MihomoBuild{
		Platform:          "test",
		Version:           "v-test",
		ExecutableSHA256:  strings.Repeat("0", 64),
		ArchiveSHA256:     strings.Repeat("1", 64),
		LicenseIdentifier: "GPL-3.0-or-later",
	})

	err := kernel.Validate(context.Background(), []byte("proxies: []\n"))
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("Validate error = %v, want pinned digest mismatch", err)
	}
}

func TestPinnedMihomoBuildsHaveTargetSpecificMetadata(t *testing.T) {
	if app.WindowsMihomoBuild.Platform == app.KernelSUMihomoBuild.Platform || app.WindowsMihomoBuild.ExecutableSHA256 == app.KernelSUMihomoBuild.ExecutableSHA256 {
		t.Fatalf("target metadata is not platform-specific: windows=%+v kernelsu=%+v", app.WindowsMihomoBuild, app.KernelSUMihomoBuild)
	}
	if app.WindowsMihomoBuild.Version == "" || app.KernelSUMihomoBuild.Version == "" || app.WindowsMihomoBuild.LicenseIdentifier == "" || app.KernelSUMihomoBuild.LicenseIdentifier == "" {
		t.Fatalf("pinned metadata is incomplete: windows=%+v kernelsu=%+v", app.WindowsMihomoBuild, app.KernelSUMihomoBuild)
	}
}
