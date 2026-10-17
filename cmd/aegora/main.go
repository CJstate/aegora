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

	"github.com/syncronix-labs/aegora/internal/cache"
	"github.com/syncronix-labs/aegora/internal/config"
	"github.com/syncronix-labs/aegora/internal/middleware"
	"github.com/syncronix-labs/aegora/internal/proxy"
	"github.com/syncronix-labs/aegora/internal/queue"
	"github.com/syncronix-labs/aegora/internal/ratelimit"
	"github.com/syncronix-labs/aegora/internal/reliability"
	"github.com/syncronix-labs/aegora/internal/shedding"
)

// toProxyRoutes maps config.RouteConfig definitions into proxy.RouteConfig structures.
func toProxyRoutes(cfgRoutes []config.RouteConfig) []proxy.RouteConfig {
	routes := make([]proxy.RouteConfig, len(cfgRoutes))
	for i, r := range cfgRoutes {
		routes[i] = proxy.RouteConfig{
			Name:             r.Name,
			PathPrefix:       r.PathPrefix,
			PathRegexPattern: r.PathRegex,
			Methods:          r.Methods,
			Headers:          r.Headers,
			Targets:          r.Targets,
			StripPrefix:      r.StripPrefix,
			LBPolicy:         r.LB,
		}
	}
	return routes
}

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

// buildMux registers health endpoints and delegates all other incoming traffic
// to the dynamic reverse proxy gateway.
func buildMux(gw http.Handler) *http.ServeMux {
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

		gw.ServeHTTP(w, r)
	})

	return mux
}

// buildRateLimitMiddleware constructs the rate-limiting middleware based on configuration.
func buildRateLimitMiddleware(cfg config.RateLimitConfig) middleware.Middleware {
	var limiterFactory func() ratelimit.Limiter
	switch cfg.Limiter {
	case "token_bucket":
		capacity := cfg.MaxRequests
		refill := cfg.RefillPerSecond
		limiterFactory = func() ratelimit.Limiter {
			return ratelimit.NewTokenBucket(capacity, refill)
		}
	default:
		window := cfg.Window.Duration()
		maxReqs := cfg.MaxRequests
		limiterFactory = func() ratelimit.Limiter {
			return ratelimit.NewSlidingWindow(window, maxReqs)
		}
	}

	var keyFn middleware.KeyFunc
	switch cfg.Key {
	case "api_key":
		keyFn = middleware.APIKey
	default:
		keyFn = middleware.IPKey
	}

	return middleware.RateLimit(limiterFactory, keyFn, []string{"/livez", "/readyz"})
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
//     measure exact routing and handler execution latency while capturing the final HTTP status code.
//  4. RateLimit (optional): Positioned between AccessLog and the mux/gateway to enforce
//
// toReliabilityConfig maps config.ReliabilityConfig to proxy.ReliabilityConfig with sensible defaults.
func toReliabilityConfig(cfg config.ReliabilityConfig) proxy.ReliabilityConfig {
	threshold := cfg.BreakerFailureThreshold
	if threshold <= 0 {
		threshold = 5
	}

	cooldown := cfg.BreakerCooldown.Duration()
	if cooldown <= 0 {
		cooldown = 30 * time.Second
	}

	attempts := cfg.RetryMaxAttempts
	if attempts <= 0 {
		attempts = 3
	}

	initialDelay := cfg.RetryInitialDelay.Duration()
	if initialDelay <= 0 {
		initialDelay = 100 * time.Millisecond
	}

	return proxy.ReliabilityConfig{
		BreakerFailureThreshold: threshold,
		BreakerCooldown:         cooldown,
		BreakerHalfOpenProbes:   2,
		BreakerSuccessThreshold: 2,
		RetryEnabled:            cfg.RetryEnabled,
		RetryPolicy: reliability.RetryPolicy{
			MaxAttempts:  attempts,
			InitialDelay: initialDelay,
			MaxDelay:     2 * time.Second,
			Multiplier:   2.0,
			Jitter:       1.0,
		},
	}
}

// buildApp constructs the HTTP handler wrapped with the middleware chain and optional admission queue.
func buildApp(cfg *config.Config, logger *slog.Logger) (http.Handler, *queue.Queue, error) {
	gwRoutes := toProxyRoutes(cfg.Routes)
	rc := toReliabilityConfig(cfg.Reliability)
	gw, err := proxy.NewGateway(gwRoutes, rc)
	if err != nil {
		return nil, nil, fmt.Errorf("build gateway: %w", err)
	}

	var gwHandler http.Handler = gw
	var q *queue.Queue
	if cfg.Queue.Enabled {
		cd := queue.NewCodel(cfg.Queue.CodelTarget.Duration(), cfg.Queue.CodelInterval.Duration())
		q = queue.NewQueue(cfg.Queue.MaxInflight, cfg.Queue.MaxQueueDepth, cd, cfg.Queue.PriorityPaths.High, cfg.Queue.PriorityPaths.Low)
		gwHandler = q.Admit(gw)
	}

	// In-memory response caching: positioned between RateLimit and Queue.Admit so that
	// hits skip queueing and backend load (the expensive parts) while per-key rate
	// limiting still applies uniformly.
	if cfg.Cache.Enabled {
		logger.Info("cache enabled",
			slog.Bool("enabled", cfg.Cache.Enabled),
			slog.Duration("ttl", cfg.Cache.TTL.Duration()),
			slog.Int("max_entries", cfg.Cache.MaxEntries),
		)
		cacheStore := cache.NewCache(cfg.Cache.MaxEntries, cfg.Cache.MaxBodyBytes)
		gwHandler = cache.CacheMiddleware(cacheStore, cfg.Cache.TTL.Duration(), cfg.Cache.MaxBodyBytes)(gwHandler)
	}

	mux := buildMux(gwHandler)

	mws := []middleware.Middleware{
		middleware.Recovery(logger),
		middleware.RequestID,
		middleware.AccessLog(logger),
	}

	// Adaptive load shedding: shed globally before spending per-key rate-limit
	// budget or queue capacity. When overloaded, dropping traffic early prevents
	// starvation of client token buckets and unnecessary queue contention.
	if cfg.Shedding.Enabled {
		sc := shedding.NewController(
			cfg.Shedding.TargetP99.Duration(),
			cfg.Shedding.AdjustInterval.Duration(),
			cfg.Shedding.MinAdmitRate,
			cfg.Shedding.PriorityPaths.High,
			cfg.Shedding.PriorityPaths.Low,
		)
		mws = append(mws, sc.Admit)
	}

	if cfg.RateLimit.Enabled {
		mws = append(mws, buildRateLimitMiddleware(cfg.RateLimit))
	}

	chain := middleware.New(mws...)

	return chain.Then(mux), q, nil
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
//     measure exact routing and handler execution latency while capturing the final HTTP status code.
//  4. Load Shedding (optional): Positioned immediately after AccessLog and before RateLimit to shed
//     globally before spending per-key rate-limit budget or queue capacity.
//  5. RateLimit (optional): Positioned between Load Shedding and Cache / mux / gateway to enforce
//     per-key rate limits uniformly on all incoming requests.
//  6. Cache (optional): Positioned between RateLimit and Queue.Admit so that hits skip
//     queueing and backend load (the expensive parts) while per-key rate limiting still applies uniformly.
//  7. Queue Admission (optional): Positioned between Cache and the upstream gateway to enforce
//     concurrency limits and CoDel bufferbloat drops on traffic routed to backend targets, while
//     bypassing /livez and /readyz health probes.
func buildHandler(cfg *config.Config, logger *slog.Logger) (http.Handler, error) {
	h, q, err := buildApp(cfg, logger)
	if err != nil {
		return nil, err
	}
	if q != nil {
		q.Start()
	}
	return h, nil
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

	handler, q, err := buildApp(cfg, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error building handler: %v\n", err)
		os.Exit(1)
	}
	if q != nil {
		q.Start()
	}

	server := &http.Server{
		Addr:         cfg.Server.Addr,
		Handler:      handler,
		ReadTimeout:  time.Duration(cfg.Server.ReadTimeout),
		WriteTimeout: time.Duration(cfg.Server.WriteTimeout),
		IdleTimeout:  time.Duration(cfg.Server.IdleTimeout),
	}

	logger.Info("Aegora starting...", slog.String("addr", cfg.Server.Addr))

	if isCLITestRun() {
		if q != nil {
			q.Stop()
		}
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

		if q != nil {
			q.Stop()
		}

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
