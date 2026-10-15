// Command picture-puzzles serves the Puzzle Table web app with a family photo
// gallery and per-person puzzle progress.
//
// Configuration comes from flags, with environment variables as defaults:
//
//	PUZZLE_PASSCODE  the family passcode (required)
//	PUZZLE_ADDR      listen address (default ":8080"; PORT is honoured too)
//	PUZZLE_DATA      directory for photos, progress and the signing key (default "./data")
//	PUZZLE_ADMINS    comma-separated names who may remove anyone's photos
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	defaultAddr := envOr("PUZZLE_ADDR", ":8080")
	if port := os.Getenv("PORT"); port != "" && os.Getenv("PUZZLE_ADDR") == "" {
		defaultAddr = ":" + port
	}
	addr := flag.String("addr", defaultAddr, "listen address")
	dataDir := flag.String("data", envOr("PUZZLE_DATA", "./data"), "data directory")
	flag.Parse()

	passcode := os.Getenv("PUZZLE_PASSCODE")
	if strings.TrimSpace(passcode) == "" {
		log.Fatal("PUZZLE_PASSCODE must be set to the family passcode")
	}

	store, err := OpenStore(*dataDir)
	if err != nil {
		log.Fatalf("open data directory: %v", err)
	}
	auth, err := NewAuth(*dataDir, passcode, splitNames(os.Getenv("PUZZLE_ADMINS")))
	if err != nil {
		log.Fatalf("set up sign-in: %v", err)
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           NewServer(store, auth).Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		log.Printf("puzzle table listening on %s (data in %s)", *addr, *dataDir)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	<-ctx.Done()

	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func splitNames(s string) []string {
	var out []string
	for _, n := range strings.Split(s, ",") {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}
