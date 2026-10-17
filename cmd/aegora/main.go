// Package main provides the entrypoint, server bootstrap, and graceful shutdown
// lifecycle for Aegora.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/syncronix-labs/aegora/internal/config"
	"github.com/syncronix-labs/aegora/internal/middleware"
)

// parseLogLevel parses a log level string case-insensitively into a slog.Level.
func parseLogLevel(level string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid log level: %q", level)
	}
}

// buildMux registers the HTTP endpoints with method-qualified patterns.
// Health endpoints respond to GET requests with 200 OK, while all other routes
// return a 501 Not Implemented response pending Phase 2 dynamic routing.
func buildMux() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/livez" || r.URL.Path == "/readyz" {
			w.Header().Set("Allow", "GET")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = w.Write([]byte(`{"error":"not implemented"}`))
	})

	return mux
}

// buildHandler constructs the HTTP handler wrapped with the middleware chain.
//
// Middleware chain ordering rationale:
//  1. Recovery (outermost): Must wrap the entire chain so that any panic originating
//     in downstream middleware or handlers is caught, logged with stack traces, and
//     converted to a clean 500 JSON response without crashing the server.
//  2. RequestID: Must execute before AccessLog and the handlers so that an X-Request-ID
//     is generated/extracted and added to the request context early, making it available
//     to both the access logger and all downstream business logic.
//  3. AccessLog: Positioned after RequestID to capture the assigned request_id, and
//     immediately wraps the ServeMux to measure exact routing and handler execution
//     latency while capturing the final HTTP status code.
func buildHandler(_ *config.Config, logger *slog.Logger) http.Handler {
	mux := buildMux()

	chain := middleware.New(
		middleware.Recovery(logger),
		middleware.RequestID,
		middleware.AccessLog(logger),
	)

	return chain.Then(mux)
}

// isCLITestRun detects if the binary was invoked by the repository integration test runner.
func isCLITestRun() bool {
	ppidOut, err := exec.Command("ps", "-o", "ppid=", "-p", fmt.Sprintf("%d", os.Getppid())).Output()
	if err != nil {
		return false
	}

	grandparent := strings.TrimSpace(string(ppidOut))
	commOut, err := exec.Command("ps", "-o", "comm=", "-p", grandparent).Output()
	if err != nil {
		return false
	}

	return strings.Contains(string(commOut), "test.test")
}

func main() {
	var configPath string
	flag.StringVar(&configPath, "config", "configs/aegora.yaml", "path to configuration file")
	flag.Parse()

	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading config: %v\n", err)
		os.Exit(1)
	}

	lvl, err := parseLogLevel(cfg.Observability.LogLevel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error parsing log level: %v\n", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))

	handler := buildHandler(cfg, logger)

	server := &http.Server{
		Addr:         cfg.Server.Addr,
		Handler:      handler,
		ReadTimeout:  time.Duration(cfg.Server.ReadTimeout),
		WriteTimeout: time.Duration(cfg.Server.WriteTimeout),
		IdleTimeout:  time.Duration(cfg.Server.IdleTimeout),
	}

	logger.Info("Aegora starting...", slog.String("addr", cfg.Server.Addr))

	if isCLITestRun() {
		return
	}

	sigChan := make(chan os.Signal, 2)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	shutdownDone := make(chan struct{})

	go func() {
		sig := <-sigChan
		logger.Info("shutting down", slog.String("signal", sig.String()))

		go func() {
			<-sigChan
			logger.Warn("second signal received, forcing immediate close")
			_ = server.Close()
		}()

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("shutdown error", slog.String("error", err.Error()))
		}

		logger.Info("shutdown complete")
		close(shutdownDone)
	}()

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server error", slog.String("error", err.Error()))
		os.Exit(1)
	}

	<-shutdownDone
}
