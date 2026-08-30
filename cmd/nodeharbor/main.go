package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/Kaniruka/NodeHarbor/internal/app"
	webassets "github.com/Kaniruka/NodeHarbor/internal/web"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		log.Printf("NodeHarbor stopped: %v", err)
		os.Exit(1)
	}
}

func run() error {
	listenAddress := flag.String("listen", "", "HTTP listen address; defaults to the persisted listener address and port")
	dataDirectory := flag.String("data", "data", "directory containing persistent state")
	listenerFile := flag.String("listener-file", "", "file for the actual loopback management listener URL")
	launchBrowser := flag.Bool("open-browser", true, "open the management UI in the default browser")
	browserPath := flag.String("browser-path", "", "advanced: override the bundled Chromium executable")
	browserDriver := flag.String("browser-driver", "", "advanced: override the bundled Playwright driver directory")
	browserHeaded := flag.Bool("browser-headed", false, "advanced: show scoring pages while diagnosing provider failures")
	testIPSuperEndpoint := flag.String("test-ipsuper-endpoint", "", "explicit deterministic smoke-test IPSuper endpoint")
	testIPv4IdentityEndpoint := flag.String("test-ipv4-identity-endpoint", "", "explicit deterministic smoke-test IPv4 Exit Identity endpoint")
	testIPv6IdentityEndpoint := flag.String("test-ipv6-identity-endpoint", "", "explicit deterministic smoke-test IPv6 Exit Identity endpoint")
	showVersion := flag.Bool("version", false, "print the NodeHarbor version")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return nil
	}
	if runtime.GOOS != "windows" {
		return errors.New("NodeHarbor supports Windows 10/11 x64 only")
	}

	assets, err := webassets.Assets()
	if err != nil {
		return fmt.Errorf("open embedded WebUI: %w", err)
	}
	kernel := app.NewMihomoKernel(defaultMihomoPath())
	dependencies := app.DefaultDependencies(kernel)
	executable, _ := os.Executable()
	runtimeRoot := filepath.Join(filepath.Dir(executable), "browser-runtime")
	resolvedBrowserPath := *browserPath
	if resolvedBrowserPath == "" {
		resolvedBrowserPath = app.FindBundledChromium(runtimeRoot)
	}
	resolvedBrowserDriver := *browserDriver
	if resolvedBrowserDriver == "" {
		resolvedBrowserDriver = filepath.Join(runtimeRoot, "driver")
	}
	testEndpointsRequested := *testIPSuperEndpoint != "" || *testIPv4IdentityEndpoint != "" || *testIPv6IdentityEndpoint != ""
	if testEndpointsRequested && os.Getenv("NODEHARBOR_PACKAGE_SMOKE") != "1" {
		return errors.New("deterministic smoke endpoints require NODEHARBOR_PACKAGE_SMOKE=1")
	}
	if testEndpointsRequested {
		dependencies = app.DefaultDependenciesWithTestEndpoints(kernel, app.TestEndpointConfig{
			IPSuperEndpoint:      *testIPSuperEndpoint,
			IPv4IdentityEndpoint: *testIPv4IdentityEndpoint,
			IPv6IdentityEndpoint: *testIPv6IdentityEndpoint,
		})
	}
	dependencies.BrowserRuntime = app.NewPlaywrightBrowserRuntime(app.BrowserRuntimeConfig{
		DriverDirectory:      resolvedBrowserDriver,
		ExecutablePath:       resolvedBrowserPath,
		DiagnosticsDirectory: filepath.Join(*dataDirectory, "browser-diagnostics"),
		Headless:             !*browserHeaded,
	})
	application, err := app.Open(context.Background(), app.Config{
		DatabasePath: filepath.Join(*dataDirectory, "nodeharbor.db"),
		WebAssets:    assets,
	}, dependencies)
	if err != nil {
		return err
	}
	defer application.Close()
	if *listenAddress == "" {
		endpoint, err := application.ListenEndpoint(context.Background())
		if err != nil {
			return fmt.Errorf("read configured listener: %w", err)
		}
		*listenAddress = endpoint
	}

	server := &http.Server{
		Addr:              *listenAddress,
		Handler:           application.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	stopped := make(chan os.Signal, 1)
	signal.Notify(stopped, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stopped
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
	}()

	publicListener, err := net.Listen("tcp", *listenAddress)
	var managementListener net.Listener
	var listeners []net.Listener
	if err != nil {
		application.SetListenerError(fmt.Errorf("listen for Published Subscription at %s: %w", *listenAddress, err))
		managementListener, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return fmt.Errorf("listen for fallback loopback management: %w", err)
		}
		listeners = []net.Listener{managementListener}
	} else {
		application.SetListenerError(nil)
		managementListener = publicListener
		listeners = []net.Listener{publicListener}
		if requiresLoopbackListener(publicListener) {
			managementListener, err = net.Listen("tcp", net.JoinHostPort("127.0.0.1", listenerPort(publicListener)))
			if err != nil {
				application.SetListenerError(fmt.Errorf("listen for loopback management: %w", err))
				managementListener, err = net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					_ = publicListener.Close()
					return fmt.Errorf("listen for fallback loopback management: %w", err)
				}
			}
			listeners = append(listeners, managementListener)
		}
	}
	managementURL := listenerURL(managementListener)
	if publicListener != nil && listenerIsWildcard(publicListener) {
		loopbackAddress := "127.0.0.1"
		if address, ok := publicListener.Addr().(*net.TCPAddr); ok && address.IP.To4() == nil {
			loopbackAddress = "::1"
		}
		managementURL = "http://" + net.JoinHostPort(loopbackAddress, listenerPort(publicListener))
	}
	if publicListener != nil {
		log.Printf("Published Subscription listener is available at %s", listenerURL(publicListener))
	} else {
		log.Printf("Published Subscription listener is unavailable; serving loopback diagnostics only")
	}
	log.Printf("Management UI is available at %s", managementURL)
	if *listenerFile != "" {
		if err := os.WriteFile(*listenerFile, []byte(managementURL), 0o600); err != nil {
			return fmt.Errorf("write management listener file: %w", err)
		}
	}
	if *launchBrowser {
		if err := openBrowser(managementURL); err != nil {
			log.Printf("could not open the browser: %v", err)
		}
	}
	serveErrors := make(chan error, len(listeners))
	for _, currentListener := range listeners {
		go func() {
			serveErrors <- server.Serve(currentListener)
		}()
	}
	var serveErr error
	for range listeners {
		if currentErr := <-serveErrors; currentErr != nil && !errors.Is(currentErr, http.ErrServerClosed) && serveErr == nil {
			serveErr = currentErr
			_ = server.Close()
		}
	}
	if serveErr != nil {
		return fmt.Errorf("serve HTTP: %w", serveErr)
	}
	return nil
}

func listenerURL(listener net.Listener) string {
	return "http://" + listener.Addr().String()
}

func requiresLoopbackListener(listener net.Listener) bool {
	return !listenerIsLoopback(listener) && !listenerIsWildcard(listener)
}

func listenerIsLoopback(listener net.Listener) bool {
	address, ok := listener.Addr().(*net.TCPAddr)
	return ok && address.IP.IsLoopback()
}

func listenerIsWildcard(listener net.Listener) bool {
	address, ok := listener.Addr().(*net.TCPAddr)
	return ok && address.IP.IsUnspecified()
}

func listenerPort(listener net.Listener) string {
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_, port, _ := net.SplitHostPort(listener.Addr().String())
		return port
	}
	return strconv.Itoa(address.Port)
}

func defaultMihomoPath() string {
	executable, err := os.Executable()
	if err != nil {
		if runtime.GOOS == "windows" {
			return "nodeharbor-core.exe"
		}
		return "nodeharbor-core"
	}
	coreName := "nodeharbor-core"
	if runtime.GOOS == "windows" {
		coreName += ".exe"
	}
	return filepath.Join(filepath.Dir(executable), coreName)
}

func openBrowser(url string) error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}
