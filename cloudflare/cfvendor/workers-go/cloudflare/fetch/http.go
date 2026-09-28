package fetch

import (
	"context"
	"io"
	"net/http"
)

// Request represents an HTTP request and is part of the Fetch API.
// Docs: https://developers.cloudflare.com/workers/runtime-apis/request/
type Request struct {
	*http.Request
}

// NewRequest returns new Request given a method, URL, and optional body
func NewRequest(ctx context.Context, method string, url string, body io.Reader) (*Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}

	return &Request{
		Request: req,
	}, nil
}

// Do sends an HTTP request and returns an HTTP response
func (c *Client) Do(req *Request, init *RequestInit) (*http.Response, error) {
	return fetch(c.namespace, req.Request, init)
}
