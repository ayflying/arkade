package oidclogin

import (
	"io"
	"net/http"
)

// Bound all discovery, JWKS and token responses. A configured provider can be
// private, but it cannot make authentication allocate an unbounded response.
type boundedTransport struct{ base http.RoundTripper }
type boundedBody struct {
	io.Reader
	closer io.Closer
}

func (b boundedBody) Close() error { return b.closer.Close() }
func (t boundedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if response.Body != nil {
		response.Body = boundedBody{Reader: io.LimitReader(response.Body, 1<<20), closer: response.Body}
	}
	return response, nil
}
