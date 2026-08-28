package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
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
	listenAddress := flag.String("listen", "127.0.0.1:9876", "HTTP listen address")
	dataDirectory := flag.String("data", "data", "directory containing persistent state")
	flag.Parse()

	assets, err := webassets.Assets()
	if err != nil {
		return fmt.Errorf("open embedded WebUI: %w", err)
	}
	application, err := app.Open(context.Background(), app.Config{
		DatabasePath: filepath.Join(*dataDirectory, "nodeharbor.db"),
		WebAssets:    assets,
	}, app.DefaultDependencies())
	if err != nil {
		return err
	}
	defer application.Close()

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

	log.Printf("NodeHarbor is available at http://%s", *listenAddress)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve HTTP: %w", err)
	}
	return nil
}
