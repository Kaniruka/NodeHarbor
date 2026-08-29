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
	listenAddress := flag.String("listen", "", "HTTP listen address; defaults to the persisted management port on loopback")
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
		port, err := application.ListenPort(context.Background())
		if err != nil {
			return fmt.Errorf("read configured listen port: %w", err)
		}
		*listenAddress = fmt.Sprintf("127.0.0.1:%d", port)
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
	managementURL := "http://" + listener.Addr().String()
	log.Printf("NodeHarbor is available at %s", managementURL)
	if *launchBrowser {
		if err := openBrowser(managementURL); err != nil {
			log.Printf("could not open the browser: %v", err)
		}
	}
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve HTTP: %w", err)
	}
	return nil
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
