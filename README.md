# Aegora

Go Layer 7 reliability gateway.

Aegora is a high-throughput HTTP/1.1 reverse proxy and reliability gateway built with Go 1.25. It protects upstream microservices from overload, cascading failures, latency spikes, and traffic surges through defensive middleware, multi-tier caching, dynamic queueing, adaptive load shedding, and real-time observability.

---

## Features

### Routing
- **Flexible Path Matching**: Prefix-based and regular expression route matching with method and header constraints.
- **Load Balancing**: Round-robin and least-connections upstream server pool dispatching.
- **Passive Health Checking**: Automatic circuit-level target fault detection and recovery.
- **Path Rewriting**: Optional path prefix stripping before forwarding to upstream targets.

### Resilience
- **Rate Limiting**: Sliding window and token bucket limiters configurable by client IP or API key, with standalone in-memory operation or distributed Redis synchronization.
- **Circuit Breaker**: Per-route failure tracking with closed, open, and half-open state machines to isolate unhealthy backends.
- **Retry with Backoff**: Automatic retry policies for idempotent HTTP requests with exponential backoff and decorrelated full jitter.
- **Priority Queues**: Multi-level admission queues (high, normal, low) based on request path rules and `X-Priority` headers.
- **CoDel Active Queue Management**: Controlled Delay drop logic to prevent bufferbloat under sustained connection spikes.
- **Adaptive Load Shedding**: AIMD (Additive Increase / Multiplicative Decrease) controller dynamically throttling lower-priority traffic when P99 latency violates target thresholds.

### Performance
- **Multi-Tier Caching**: High-speed L1 in-memory response caching paired with distributed L2 Redis storage.
- **Conditional Validation**: Automatic `ETag` generation and `If-None-Match` 304 Not Modified evaluation.
- **Safe Cache Semantics**: Automatic exclusion of requests with `Authorization` headers, `no-store` directives, or `Set-Cookie` responses.

### Observability
- **Prometheus Telemetry**: Dedicated `/metrics` endpoint implementing the RED method (Request rate, Error count, Duration histograms, and current Inflight gauges).
- **OpenTelemetry Tracing**: Distributed tracing with W3C tracecontext propagation, stdout or OTLP exporters, and parent-based sampling.
- **Structured Logging**: Context-aware `log/slog` output capturing request IDs, HTTP status codes, routing latency, and client details.

### Operations
- **Dedicated Admin API**: Management server running on an isolated network port (`9091`) with endpoints for health checks, route inspection, and runtime configuration queries.
- **Zero-Downtime Hot Reload**: Atomic configuration reload triggered via HTTP POST without dropping active connections.
- **Container Packaging**: Lightweight multi-stage Docker image and ready-to-run Docker Compose environment.

---

## Architecture

Aegora sits between external clients and internal backend services. Requests traverse an ordered sequence of reliability and telemetry layers:

```text
                         +-----------------------------------+
                         |           Client Traffic          |
                         +-----------------+-----------------+
                                           |
                                           v [Port 8080]
     +---------------------------------------------------------------------------------+
     |                               Aegora Gateway                                    |
     |                                                                                 |
     |  Incoming Request                                                               |
     |         |                                                                       |
     |         v                                                                       |
     |   [1. Recovery]     Catch panics, write 500 JSON, maintain server uptime        |
     |         |                                                                       |
     |         v                                                                       |
     |   [2. RequestID]    Extract or generate UUID, attach to context and headers     |
     |         |                                                                       |
     |         v                                                                       |
     |   [3. Tracing]      Start OTel server span, propagate trace context             |
     |         |                                                                       |
     |         v                                                                       |
     |   [4. Metrics]      Record RED method metrics (rate, error, duration, inflight) |
     |         |                                                                       |
     |         v                                                                       |
     |   [5. AccessLog]    Emit structured slog entry with latency and status code     |
     |         |                                                                       |
     |         v                                                                       |
     |   [6. Shedding]     AIMD controller drops low-priority traffic under latency    |
     |         |                                                                       |
     |         v                                                                       |
     |   [7. RateLimit]    Sliding window or token bucket per IP/API key (L1 / Redis)  |
     |         |                                                                       |
     |         v                                                                       |
     |   [8. Cache]        Return cached 200/304 on hit; buffer on miss (L1 / Redis)   |
     |         |                                                                       |
     |         v (cache miss)                                                          |
     |   [9. Queue]        Priority classification and CoDel queue admission           |
     |         |                                                                       |
     |         v                                                                       |
     |   [10. Gateway]     Match route, select target pool (round-robin / least-conn)  |
     |         |           Per-route Circuit Breaker -> Retry with Jitter              |
     +---------+-----------------------------------------------------------------------+
               |
               +----------------------------+----------------------------+
               |                            |                            |
               v                            v                            v
      +-----------------+          +-----------------+          +-----------------+
      |    Backend 1    |          |    Backend 2    |          |    Backend N    |
      +-----------------+          +-----------------+          +-----------------+

  Sidecars and Management Ports:
      * Redis (Port 6379): Distributed rate limit counters and L2 response cache
      * Prometheus (Port 9090): /metrics endpoint scraping gateway RED telemetry
      * Admin API (Port 9091): /admin/health, /admin/routes, /admin/config, /admin/reload
```

---

## Quickstart

Start the gateway along with two mock backend services and a Redis instance using Docker Compose:

```bash
make compose-up
```

Alternatively, run Docker Compose directly:

```bash
docker compose -f deployments/docker-compose.yml up --build -d
```

### Verify Operations

Send a request through the gateway to verify routing and load balancing:

```bash
curl -i http://localhost:8080/api/users
```
Expected response:
```http
HTTP/1.1 200 OK
Content-Type: text/plain; charset=utf-8
X-Cache: MISS
X-Request-Id: 52d6beb3dde73b4696b056c8961b981c

backend-1
```

Send the same request again to test the response cache:

```bash
curl -i http://localhost:8080/api/users
```
Expected response:
```http
HTTP/1.1 200 OK
Content-Type: text/plain; charset=utf-8
X-Cache: HIT
...
```

Scrape Prometheus RED telemetry from the metrics listener:

```bash
curl -s http://localhost:9090/metrics | grep aegora
```

Inspect administrative health and build version:

```bash
curl -i http://localhost:9091/admin/health
```

When finished, tear down the demo environment:

```bash
make compose-down
```

---

## Configuration

Aegora is configured through YAML files, with optional overrides via environment variables (`AEGORA_*`). The full configuration specification is located in [configs/aegora.yaml](configs/aegora.yaml).

Here is a minimal annotated example configuring a listener, an upstream route, and client rate limiting:

```yaml
server:
  addr: "0.0.0.0:8080"        # Ingress port for incoming data-plane traffic
  read_timeout: "30s"
  write_timeout: "30s"
  idle_timeout: "60s"

routes:
  - name: "api-service"
    path_prefix: "/api"       # Match requests starting with /api
    methods:
      - "GET"
      - "POST"
    targets:
      - "http://127.0.0.1:8081"
      - "http://127.0.0.1:8082"
    strip_prefix: "/api"      # Forward request without /api prefix to backend
    lb: "round_robin"         # Selection policy: round_robin or least_connections

rate_limit:
  enabled: true
  limiter: "sliding_window"   # sliding_window or token_bucket
  max_requests: 120           # Maximum requests allowed within window
  window: "1m"
  key: "ip"                   # Rate limit partition: ip or api_key

observability:
  log_level: "info"
  metrics_addr: "0.0.0.0:9090" # Prometheus scrapers connect here

admin:
  enabled: true
  addr: "0.0.0.0:9091"         # Dedicated management interface
```

---

## Admin API

Aegora provides a separate administrative server listening on port `9091` by default. Keeping administration isolated from the data plane prevents management commands from contending with customer traffic.

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/admin/health` | Liveness verification and binary build version |
| `GET` | `/admin/routes` | JSON array of active route rules, match prefixes, target pools, and policies |
| `GET` | `/admin/config` | Runtime snapshot of the active server configuration |
| `POST` | `/admin/reload` | Trigger hot reload of configuration from disk without downtime |

Example configuration reload trigger:

```bash
curl -i -X POST http://localhost:9091/admin/reload
```

---

## Benchmarks

Aegora includes benchmarks that measure hot-path performance under realistic conditions.

Run the test suite benchmarks:

```bash
go test -bench=. -benchtime=2s -benchmem ./internal/...
```

### Measured Performance

The following benchmarks were conducted on macOS (Apple M1, 8 cores) running Go 1.25/1.26:

| Benchmark | Iterations (2s) | Latency | Memory | Allocs | Description |
|---|---|---|---|---|---|
| `BenchmarkGatewayChain-8` | 44,428 | 48,402 ns/op | 41,766 B/op | 124 allocs/op | Full chain cache-miss GET through ReverseProxy to live backend |
| `BenchmarkCacheHit-8` | 8,467,442 | 269.5 ns/op | 48 B/op | 3 allocs/op | Response cache hit path serving directly from memory |
| `BenchmarkSlidingWindowAllow-8` | 14,011,202 | 167.6 ns/op | 0 B/op | 0 allocs/op | Sliding window Allow() throughput under parallel contention |

### Analysis of Results

- **Cache Hit Acceleration**: `BenchmarkCacheHit` serves cached responses in ~270 nanoseconds and only 3 allocations. By comparison, `BenchmarkGatewayChain` takes ~48.4 microseconds and 124 allocations to traverse RequestID generation, Prometheus histogram recordings, rate limit checks, routing lookups, and a loopback HTTP network roundtrip. Serving hits from cache is ~180x faster and eliminates 98% of allocations.
- **Rate Limiter Allocation Profile**: `BenchmarkSlidingWindowAllow` registers 0 allocs/op under high contention. This is expected behavior: `NewSlidingWindow` pre-allocates timestamp storage capacity, so `Allow()` operations lock the mutex, prune expired entries, and append current timestamps without heap escapes or buffer reallocations.

---

## Project Status and License

Aegora is under active development by the Syncronix Labs team.

This project is licensed under the Apache License, Version 2.0. See the repository documentation for ongoing release details.
