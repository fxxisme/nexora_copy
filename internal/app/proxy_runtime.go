package app

import (
	"errors"
	"net/url"
	"strings"
	"time"

	http "github.com/bogdanfinn/fhttp"
)

type proxyHealthDoer struct {
	next  upstreamHTTPDoer
	store *Store
	route proxyRoute
}

func (d *proxyHealthDoer) Do(req *http.Request) (*http.Response, error) {
	resp, err := d.next.Do(req)
	chatGPTRequest := req != nil && req.URL != nil && strings.EqualFold(req.URL.Hostname(), "chatgpt.com")
	if d.store != nil && chatGPTRequest {
		if resp != nil && resp.StatusCode == http.StatusProxyAuthRequired {
			_ = d.store.recordProxyNodeRuntimeResult(d.route, false)
		} else if resp != nil {
			_ = d.store.recordProxyNodeRuntimeResult(d.route, true)
		} else if err != nil && req.Context().Err() == nil {
			_ = d.store.recordProxyNodeRuntimeResult(d.route, false)
		}
	}
	if err != nil {
		err = redactProxyTransportError(err, d.route.URL)
		if chatGPTRequest && resp == nil && req.Context().Err() == nil {
			err = &proxyConnectionError{cause: err}
		}
	}
	return resp, err
}

type proxyConnectionError struct{ cause error }

func (e *proxyConnectionError) Error() string { return e.cause.Error() }
func (e *proxyConnectionError) Unwrap() error { return e.cause }

func isProxyConnectionFailure(err error) bool {
	var proxyErr *proxyConnectionError
	if errors.As(err, &proxyErr) {
		return true
	}
	var statusErr *upstreamStatusError
	return errors.As(err, &statusErr) && statusErr.StatusCode == http.StatusProxyAuthRequired
}

func (s *Store) recordProxyNodeRuntimeResult(route proxyRoute, connected bool) error {
	if route.NodeID == "" || route.URL == "" {
		return nil
	}
	checkedAt := time.Now().UTC().Format(time.RFC3339Nano)
	if connected {
		_, err := s.db.Exec("UPDATE app_proxy_nodes SET health_state='healthy', checked_at="+s.bind(1)+", latency_ms=NULL, health_error='', success_count=success_count+1 WHERE id="+s.bind(2)+" AND url="+s.bind(3)+" AND health_state<>'healthy'", checkedAt, route.NodeID, route.URL)
		return err
	}
	_, err := s.db.Exec("UPDATE app_proxy_nodes SET health_state='unhealthy', checked_at="+s.bind(1)+", latency_ms=NULL, health_error='上游连接失败', fail_count=fail_count+1 WHERE id="+s.bind(2)+" AND url="+s.bind(3), checkedAt, route.NodeID, route.URL)
	return err
}

type redactedTransportError struct {
	cause   error
	message string
}

func (e *redactedTransportError) Error() string { return e.message }
func (e *redactedTransportError) Unwrap() error { return e.cause }

func redactProxyTransportError(err error, proxyURL string) error {
	parsed, parseErr := url.Parse(proxyURL)
	if parseErr != nil || parsed.User == nil {
		return err
	}
	message := err.Error()
	redactedURL := *parsed
	redactedURL.User = url.User("***")
	message = strings.ReplaceAll(message, proxyURL, redactedURL.String())
	message = strings.ReplaceAll(message, parsed.User.String(), "***")
	return &redactedTransportError{cause: err, message: message}
}
