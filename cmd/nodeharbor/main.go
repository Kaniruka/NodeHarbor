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

func main() {
	if err := run(); err != nil {
		log.Printf("NodeHarbor stopped: %v", err)
		os.Exit(1)
	}
}

func run() error {
	listenAddress := flag.String("listen", "", "HTTP listen address; defaults to the persisted listener address and port")
	dataDirectory := flag.String("data", "data", "directory containing persistent state")
	launchBrowser := flag.Bool("open-browser", runtime.GOOS == "windows", "open the management UI in the default browser")
	flag.Parse()

	assets, err := webassets.Assets()
	if err != nil {
		return fmt.Errorf("open embedded WebUI: %w", err)
	}
	mihomoBuild, err := app.MihomoBuildForPlatform(runtime.GOOS)
	if err != nil {
		return err
	}
	application, err := app.Open(context.Background(), app.Config{
		DatabasePath: filepath.Join(*dataDirectory, "nodeharbor.db"),
		WebAssets:    assets,
	}, app.DefaultDependencies(app.NewMihomoKernelWithBuild(defaultMihomoPath(), mihomoBuild)))
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

	listener, err := net.Listen("tcp", *listenAddress)
	if err != nil {
		return fmt.Errorf("listen for HTTP: %w", err)
	}
	listeners := []net.Listener{listener}
	if requiresLoopbackListener(listener) {
		localListener, localErr := net.Listen("tcp", net.JoinHostPort("127.0.0.1", listenerPort(listener)))
		if localErr != nil {
			_ = listener.Close()
			return fmt.Errorf("listen for loopback management: %w", localErr)
		}
		listeners = append(listeners, localListener)
	}
	managementURL := "http://" + listener.Addr().String()
	if len(listeners) > 1 || listenerIsWildcard(listener) {
		managementURL = "http://127.0.0.1:" + listenerPort(listener)
	}
	log.Printf("NodeHarbor listener is available at %s", "http://"+listener.Addr().String())
	log.Printf("Management UI is available at %s; Published Subscription is available at %s/sub/clash.yaml", managementURL, "http://"+listener.Addr().String())
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
	if runtime.GOOS != "windows" {
		return nil
	}
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}
