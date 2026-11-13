// Public-safe synthetic service used only on disposable GitHub runners.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
)

func main() {
	version := os.Getenv("SYNTHETIC_VERSION")
	address := os.Getenv("WOW_LISTEN_ADDR")
	if version == "" || address == "" || os.Getenv("WOW_NODE_ID") == "" || os.Getenv("WOW_LOCALITY") == "" {
		panic("required synthetic image or external host environment is absent")
	}
	file, err := os.OpenFile("/var/lib/wow-sidecar/synthetic-receipts", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		panic(fmt.Errorf("protected external state not writable: %w", err))
	}
	if _, err := fmt.Fprintln(file, version); err != nil {
		panic(err)
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "version": version})
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ready", "version": version})
	})
	if err := http.ListenAndServe(address, mux); err != nil {
		panic(err)
	}
}
