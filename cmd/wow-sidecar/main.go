package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/SemperSupra/wow-sidecar/go/node"
	"github.com/SemperSupra/wow-sidecar/go/protocol"
)

var (
	sourceRevision = "development"
	buildVersion   = "0.0.0-dev"
)

func main() {
	if err := run(); err != nil {
		log.Printf("wow-sidecar: %v", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) > 1 {
		if len(os.Args) == 2 && (os.Args[1] == "version" || os.Args[1] == "--version") {
			raw, err := protocol.CanonicalJSON(map[string]any{
				"product": "wow-sidecar",
				"source_revision": sourceRevision,
				"build_version": buildVersion,
			})
			if err != nil {
				return err
			}
			fmt.Println(string(raw))
			return nil
		}
		return fmt.Errorf("unsupported argument")
	}

	config, err := node.ConfigFromEnv()
	if err != nil {
		return err
	}
	runtime, err := node.NewRuntime(config, sourceRevision, buildVersion)
	if err != nil {
		return err
	}

	server := &http.Server{
		Addr:              runtime.ListenAddr(),
		Handler:           runtime.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errs := make(chan error, 1)
	go func() {
		log.Printf("wow-sidecar: serving node=%s addr=%s", config.NodeID, runtime.ListenAddr())
		errs <- server.ListenAndServe()
	}()

	select {
	case err := <-errs:
		if err == http.ErrServerClosed {
			return nil
		}
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		err := <-errs
		if err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("serve after shutdown: %w", err)
		}
		return nil
	}
}
