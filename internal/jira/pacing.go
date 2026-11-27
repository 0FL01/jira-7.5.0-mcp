package jira

import (
	"net/http"
	"time"
)

type pacedTransport struct {
	next     http.RoundTripper
	interval time.Duration
	gate     chan struct{}
	ready    time.Time // protected by gate
}

func (p *pacedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	select {
	case p.gate <- struct{}{}:
		defer func() { <-p.gate }()
	case <-ctx.Done():
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, ctx.Err()
	}
	if delay := time.Until(p.ready); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			if req.Body != nil {
				_ = req.Body.Close()
			}
			return nil, ctx.Err()
		}
	}
	response, err := p.next.RoundTrip(req)
	p.ready = time.Now().Add(p.interval)
	return response, err
}
