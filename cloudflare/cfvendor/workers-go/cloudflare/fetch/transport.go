package fetch

import (
	"net/http"
	"syscall/js"
)

// transport is an implementation of http.RoundTripper
type transport struct {
	// namespace - Objects that Fetch API belongs to. Default is undefined
	namespace js.Value
	redirect  RedirectMode
}

// RoundTrip replaces http.DefaultTransport.RoundTrip to use cloudflare fetch
func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	return fetch(t.namespace, req, &RequestInit{
		Redirect: t.redirect,
	})
}
