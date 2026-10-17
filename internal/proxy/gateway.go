package proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

type contextKey int

const (
	upstreamContextKey contextKey = iota
)

type upstreamInfo struct {
	pool  *Pool
	index int
}

type routeEntry struct {
	config RouteConfig
	pool   *Pool
	proxy  *httputil.ReverseProxy
}

// Gateway routes incoming HTTP requests to upstream server pools via reverse proxies,
// enforcing route matching, path prefix stripping, load balancing, and failure handling.
type Gateway struct {
	router *Router
	routes map[*RouteConfig]*routeEntry
}

// NewGateway constructs a Gateway with a compiled Router, one upstream Pool per route,
// and one reverse proxy per route utilizing Aegora's header sanitization and HTTP transport.
func NewGateway(routes []RouteConfig) (*Gateway, error) {
	router, err := NewRouter(routes)
	if err != nil {
		return nil, fmt.Errorf("gateway: router init: %w", err)
	}

	routeMap := make(map[*RouteConfig]*routeEntry, len(router.routes))
	for i := range router.routes {
		r := &router.routes[i]

		pool, err := NewPool(r.Targets, r.LBPolicy)
		if err != nil {
			return nil, fmt.Errorf("gateway: route %q pool init: %w", r.Name, err)
		}

		proxy := newReverseProxy(
			func(pr *httputil.ProxyRequest) {
				pr.SetURL(&url.URL{
					Scheme: pr.In.URL.Scheme,
					Host:   pr.In.URL.Host,
				})
				sanitizeHeaders(pr)
			},
			func(w http.ResponseWriter, req *http.Request, _ error) {
				if info, ok := req.Context().Value(upstreamContextKey).(*upstreamInfo); ok && info != nil {
					if info.pool != nil {
						info.pool.ReportFailure(info.index)
					}
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte(`{"error":"bad gateway"}`))
			},
		)

		routeMap[r] = &routeEntry{
			config: *r,
			pool:   pool,
			proxy:  proxy,
		}
	}

	return &Gateway{
		router: router,
		routes: routeMap,
	}, nil
}

// ServeHTTP matches incoming requests against configured routes, strips prefixes,
// selects an upstream target, and proxies the request.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if g == nil || g.router == nil {
		writeJSONError(w, http.StatusNotFound, `{"error":"no route matched"}`)
		return
	}

	route := g.router.Match(req)
	if route == nil {
		writeJSONError(w, http.StatusNotFound, `{"error":"no route matched"}`)
		return
	}

	entry := g.routes[route]
	if entry == nil {
		writeJSONError(w, http.StatusNotFound, `{"error":"no route matched"}`)
		return
	}

	path := req.URL.Path
	if entry.config.StripPrefix != "" {
		path = strings.TrimPrefix(path, entry.config.StripPrefix)
		if path == "" || !strings.HasPrefix(path, "/") {
			path = "/" + strings.TrimPrefix(path, "/")
		}
	}
	if path == "" {
		path = "/"
	}

	targetURL, idx, ok := entry.pool.Next()
	if !ok {
		writeJSONError(w, http.StatusBadGateway, `{"error":"all upstreams unavailable"}`)
		return
	}
	defer entry.pool.Done(idx)

	ctx := context.WithValue(req.Context(), upstreamContextKey, &upstreamInfo{
		pool:  entry.pool,
		index: idx,
	})

	outReq := req.Clone(ctx)
	outReq.URL.Scheme = targetURL.Scheme
	outReq.URL.Host = targetURL.Host
	if targetURL.Path == "" || targetURL.Path == "/" {
		outReq.URL.Path = path
	} else {
		outReq.URL.Path = singleJoiningSlash(targetURL.Path, path)
	}
	outReq.URL.RawPath = ""
	outReq.Host = targetURL.Host

	entry.proxy.ServeHTTP(w, outReq)
}

func writeJSONError(w http.ResponseWriter, statusCode int, jsonBody string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_, _ = w.Write([]byte(jsonBody))
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}
