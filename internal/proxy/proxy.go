package proxy

import (
	"net/http"
	"net/http/httputil"
	"net/url"
)

// ReverseProxy wraps httputil.ReverseProxy and provides
// Aegora-specific request handling behavior.
type ReverseProxy struct {
	proxy *httputil.ReverseProxy
}

// sanitizeHeaders removes spoofed X-Forwarded headers, sets current forwarding
// headers, and attaches the Aegora gateway identification header.
func sanitizeHeaders(pr *httputil.ProxyRequest) {
	pr.Out.Header.Del("X-Forwarded-For")
	pr.Out.Header.Del("X-Forwarded-Host")
	pr.Out.Header.Del("X-Forwarded-Proto")

	pr.SetXForwarded()

	pr.Out.Header.Set("X-Aegora", "true")
}

// newReverseProxy constructs an httputil.ReverseProxy configured with Aegora's
// standard transport, custom rewrite logic, and an optional error handler.
func newReverseProxy(
	rewrite func(*httputil.ProxyRequest),
	errorHandler func(http.ResponseWriter, *http.Request, error),
) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite:      rewrite,
		Transport:    NewTransport(),
		ErrorHandler: errorHandler,
	}
}

// New creates a reverse proxy for the specified target URL.
func New(target string) (*ReverseProxy, error) {
	targetURL, err := url.Parse(target)
	if err != nil {
		return nil, err
	}

	rp := newReverseProxy(func(pr *httputil.ProxyRequest) {
		pr.SetURL(targetURL)
		sanitizeHeaders(pr)
	}, nil)

	return &ReverseProxy{
		proxy: rp,
	}, nil
}

func (r *ReverseProxy) ServeHTTP(
	w http.ResponseWriter,
	req *http.Request,
) {
	r.proxy.ServeHTTP(w, req)
}
