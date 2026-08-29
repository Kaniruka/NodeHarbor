package app_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Kaniruka/NodeHarbor/internal/app"
)

type runtimeInspectionStub struct {
	inspection app.SurfingRuntimeInspection
}

func (stub runtimeInspectionStub) Inspect(context.Context) (app.SurfingRuntimeInspection, error) {
	return stub.inspection, nil
}

func TestKernelSUSurfingIsolationClassifiesRuntimeModes(t *testing.T) {
	tests := []struct {
		name           string
		inspection     app.SurfingRuntimeInspection
		wantMode       string
		wantVerified   bool
		wantReasonPart string
	}{
		{
			name:         "Surfing absent",
			inspection:   app.SurfingRuntimeInspection{Mode: "inactive"},
			wantMode:     "inactive",
			wantVerified: true,
		},
		{
			name:           "TUN active",
			inspection:     app.SurfingRuntimeInspection{Detected: true, Mode: "tun"},
			wantMode:       "tun",
			wantVerified:   false,
			wantReasonPart: "TUN",
		},
		{
			name: "verified redirect",
			inspection: app.SurfingRuntimeInspection{
				Detected:                  true,
				Mode:                      "redirect",
				ProcessIdentityVerified:   true,
				TestChannelBypassVerified: true,
				ProbeTargetBypassVerified: true,
			},
			wantMode:     "redirect",
			wantVerified: true,
		},
		{
			name: "unknown isolation",
			inspection: app.SurfingRuntimeInspection{
				Detected: true,
				Mode:     "redirect",
			},
			wantMode:       "redirect",
			wantVerified:   false,
			wantReasonPart: "bypass",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			guard := app.NewKernelSUSurfingIsolation(runtimeInspectionStub{inspection: test.inspection})
			status, err := guard.Check(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if status.Mode != test.wantMode || status.Verified != test.wantVerified {
				t.Fatalf("status=%+v, want mode=%q verified=%v", status, test.wantMode, test.wantVerified)
			}
			if test.wantReasonPart != "" && !strings.Contains(strings.ToLower(status.Reason), strings.ToLower(test.wantReasonPart)) {
				t.Fatalf("reason=%q, want it to mention %q", status.Reason, test.wantReasonPart)
			}
		})
	}
}

func TestKernelSUMihomoTestPortsExcludeSurfingDefaults(t *testing.T) {
	for _, port := range app.SurfingDefaultPorts {
		if !app.IsSurfingDefaultPort(port) {
			t.Fatalf("port %d was not classified as a Surfing default", port)
		}
	}
	if app.IsSurfingDefaultPort(9876) {
		t.Fatal("NodeHarbor management port was classified as a Surfing default")
	}
}

func TestProcSurfingRuntimeInspectorUsesOnlyProcessMetadata(t *testing.T) {
	tests := []struct {
		name       string
		cmdline    string
		wantMode   string
		wantBypass bool
	}{
		{name: "tun", cmdline: "surfing\x00--tun=true\x00", wantMode: "tun"},
		{name: "verified redirect", cmdline: "mihomo\x00--redir-port=7892\x00--exclude-uid=1000\x00--exclude-gid=1000\x00", wantMode: "redirect", wantBypass: true},
		{name: "verified tproxy", cmdline: "mihomo\x00--tproxy-port=7893\x00--bypass-uid=1000\x00--bypass-gid=1000\x00", wantMode: "tproxy", wantBypass: true},
		{name: "unverifiable mode", cmdline: "mihomo\x00--mode=global\x00", wantMode: "unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			netRoot := filepath.Join(root, "net")
			if err := os.MkdirAll(netRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(root, "self"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "self", "status"), []byte("Name:\tnodeharbor\nUid:\t1000\t1000\t1000\t1000\nGid:\t1000\t1000\t1000\t1000\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(root, "123"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "123", "cmdline"), []byte(test.cmdline), 0o600); err != nil {
				t.Fatal(err)
			}
			inspection, err := (app.ProcSurfingRuntimeInspector{ProcRoot: root, NetRoot: netRoot}).Inspect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if inspection.Mode != test.wantMode || inspection.ProcessIdentityVerified != test.wantBypass || inspection.TestChannelBypassVerified != test.wantBypass || inspection.ProbeTargetBypassVerified != test.wantBypass {
				t.Fatalf("inspection=%+v, want mode=%q bypass=%v", inspection, test.wantMode, test.wantBypass)
			}
		})
	}
}

func TestDefaultDependenciesUseRealKernelSUSurfingGuard(t *testing.T) {
	dependencies := app.DefaultDependencies(app.NewMihomoKernelWithBuild("nodeharbor-core", app.KernelSUMihomoBuild))
	if _, ok := dependencies.Isolation.(app.KernelSUSurfingIsolation); !ok {
		t.Fatalf("isolation=%T, want app.KernelSUSurfingIsolation", dependencies.Isolation)
	}
}
