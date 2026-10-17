// Package proxy provides HTTP reverse proxy functionality
// and request routing primitives for Aegora.
package proxy

// Config contains proxy routing configuration.
type Config struct {
	Routes []RouteConfig
}
