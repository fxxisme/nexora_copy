package app

import (
	"context"
	"strings"
	"time"
)

type upstreamStreamAttempt struct {
	Client *UpstreamClient
	Events <-chan UpstreamTextEvent
	Errs   <-chan error
}

func (s *Server) streamTextWithRetry(ctx context.Context, messages []map[string]any, model, conversationID, planType string) (<-chan UpstreamTextEvent, <-chan error) {
	return s.streamTextWithRetryMode(ctx, messages, model, conversationID, planType, true)
}

func (s *Server) streamChatWithRetry(ctx context.Context, messages []map[string]any, model, conversationID, planType string) (<-chan UpstreamTextEvent, <-chan error) {
	return s.streamTextWithRetryMode(ctx, messages, model, conversationID, planType, false)
}

func (s *Server) streamTextWithRetryMode(ctx context.Context, messages []map[string]any, model, conversationID, planType string, historyDisabled bool) (<-chan UpstreamTextEvent, <-chan error) {
	out := make(chan UpstreamTextEvent)
	errs := make(chan error, 1)
	go func() {
		defer close(out)
		defer close(errs)
		excluded := map[string]bool{}
		attempt := 0
		for {
			attempt++
			traceLogf(ctx, "├─ text account attempt %d plan_type=%s excluded=%d", attempt, planType, len(excluded))
			account, err := s.pickTextAccountExcluding(excluded, planType)
			if err != nil {
				errs <- err
				return
			}
			token := account.AccessToken
			traceLogf(ctx, "│  ├─ selected text account %s", accountLabel(account))
			excluded[token] = true
			if account.RefreshToken != nil && strings.TrimSpace(*account.RefreshToken) != "" && accessTokenNeedsRefresh(account.AccessToken) {
				ctxRefresh, cancel := context.WithTimeout(ctx, 45*time.Second)
				refreshed, refErr := s.refreshOAuthAccount(ctxRefresh, account.AccessToken)
				cancel()
				if refErr == nil && refreshed.AccessToken != "" {
					account = refreshed
					token = refreshed.AccessToken
					excluded[token] = true
				} else {
					traceLogf(ctx, "│  ├─ preemptive refresh failed: %v", refErr)
				}
			}
			client, err := s.newUpstreamClientForAccount(account)
			if err != nil {
				s.accountPool.releaseToken(token)
				errs <- err
				return
			}
			emitted := false
			runStream := func(current *UpstreamClient) error {
				var events <-chan UpstreamTextEvent
				var streamErrs <-chan error
				if historyDisabled {
					events, streamErrs = current.StreamTextConversationEvents(ctx, messages, model, conversationID)
				} else {
					events, streamErrs = current.StreamChatConversationEvents(ctx, messages, model, conversationID)
				}
				for ev := range events {
					ev.AccountToken = token
					if ev.Delta != "" {
						emitted = true
					}
					out <- ev
				}
				return <-streamErrs
			}
			err = runStream(client)
			attemptedRoutes := map[string]bool{client.proxyRoute.URL: true}
			for canRetryTextProxyPreflight(ctx, client, err, emitted) {
				sibling, siblingErr := s.newGroupSiblingUpstreamClientForAccount(account, client.proxyRoute, attemptedRoutes)
				if siblingErr != nil {
					traceLogf(ctx, "│  ├─ text group sibling unavailable: %v", siblingErr)
					break
				}
				if sibling == nil {
					break
				}
				client = sibling
				attemptedRoutes[client.proxyRoute.URL] = true
				traceLogf(ctx, "│  ├─ retry text preflight with group sibling")
				err = runStream(client)
			}
			if shouldRetryTextWithFallback(ctx, account, client, err, emitted) {
				fallback, fallbackErr := s.newFallbackUpstreamClientForAccount(account, client.proxyRoute)
				if fallbackErr != nil {
					traceLogf(ctx, "│  ├─ text fallback unavailable: %v", fallbackErr)
				} else if fallback != nil {
					traceLogf(ctx, "│  ├─ retry text preflight with fallback outlet")
					err = runStream(fallback)
				}
			}
			if !emitted && isInvalidTokenErrorText(err) && account.RefreshToken != nil && strings.TrimSpace(*account.RefreshToken) != "" {
				traceLogf(ctx, "│  ├─ token 401 expired, attempting oauth refresh with rt")
				ctxRefresh, cancel := context.WithTimeout(ctx, 45*time.Second)
				refreshed, refErr := s.refreshOAuthAccount(ctxRefresh, account.AccessToken)
				cancel()
				if refErr == nil && refreshed.AccessToken != "" {
					account = refreshed
					token = refreshed.AccessToken
					excluded[token] = true
					if newClient, clientErr := s.newUpstreamClientForAccount(account); clientErr == nil {
						client = newClient
						err = runStream(client)
					}
				} else {
					traceLogf(ctx, "│  ├─ reactive oauth refresh failed: %v", refErr)
				}
			}
			s.accountPool.releaseToken(token)
			if err == nil {
				traceLogf(ctx, "└─ text account attempt %d success", attempt)
				s.markAccountSuccess(token, false)
				errs <- nil
				return
			}
			traceLogf(ctx, "│  └─ text account attempt %d failed emitted=%v error=%v", attempt, emitted, err)
			s.markAccountFailure(token, err, false, model)
			if !emitted && (isInvalidTokenErrorText(err) || isAutoConversationModelNotFound(err, model) || isUpstreamBlockErrorText(err) || isTurnstileRequirementErrorText(err) || isRetryableBootstrapError(err)) {
				traceLogf(ctx, "│  ├─ retry with another text account")
				continue
			}
			errs <- err
			return
		}
	}()
	return out, errs
}

func shouldRetryTextWithFallback(ctx context.Context, account Account, client *UpstreamClient, err error, emitted bool) bool {
	return account.Proxy == "" && client != nil && !client.proxyRoute.Bound && canRetryTextProxyPreflight(ctx, client, err, emitted)
}

func canRetryTextProxyPreflight(ctx context.Context, client *UpstreamClient, err error, emitted bool) bool {
	return err != nil && ctx.Err() == nil && client != nil && client.proxyRoute.URL != "" &&
		!client.textConversationStarted && !emitted && isProxyConnectionFailure(err)
}

func (s *Server) collectTextWithRetry(ctx context.Context, messages []map[string]any, model string) (string, error) {
	events, errs := s.streamTextWithRetry(ctx, messages, model, "", "")
	text := ""
	for ev := range events {
		text += ev.Delta
	}
	if err := <-errs; err != nil {
		return "", err
	}
	return text, nil
}
