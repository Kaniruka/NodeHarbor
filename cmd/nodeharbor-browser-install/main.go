package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	playwright "github.com/mxschmitt/playwright-go"
)

func main() {
	driverDirectory := flag.String("driver", "browser-runtime/driver", "directory for the Playwright driver")
	browserDirectory := flag.String("browsers", "browser-runtime/browsers", "directory for downloaded browser binaries")
	flag.Parse()

	driver, err := filepath.Abs(*driverDirectory)
	if err != nil {
		fail("resolve driver directory", err)
	}
	browsers, err := filepath.Abs(*browserDirectory)
	if err != nil {
		fail("resolve browser directory", err)
	}
	if err := os.MkdirAll(driver, 0o755); err != nil {
		fail("create driver directory", err)
	}
	if err := os.MkdirAll(browsers, 0o755); err != nil {
		fail("create browser directory", err)
	}
	if err := os.Setenv("PLAYWRIGHT_BROWSERS_PATH", browsers); err != nil {
		fail("configure browser directory", err)
	}
	if err := playwright.Install(&playwright.RunOptions{
		DriverDirectory: driver,
		Browsers:        []string{"chromium"},
		NoInstallShell:  true,
		Verbose:         true,
		Stdout:          os.Stdout,
		Stderr:          os.Stderr,
	}); err != nil {
		fail("install Playwright Chromium runtime", err)
	}
	fmt.Printf("Installed Playwright Chromium runtime in %s\n", browsers)
}

func fail(action string, err error) {
	fmt.Fprintf(os.Stderr, "NodeHarbor browser runtime: %s: %v\n", action, err)
	os.Exit(1)
}
