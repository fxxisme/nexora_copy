package app

import (
	"context"
	"errors"
	"sync"
	"strings"
	"time"
)

func (s *Server) imageRequestTimeout() time.Duration {
	opts := s.imageGenerationOptions()
	return opts.Timeout + opts.PollInitialWait + 60*time.Second
}

func (s *Server) imageGenerationOptions() imageGenerationOptions {
	return normalizeImageGenerationOptions(imageGenerationOptions{
		Timeout:         time.Duration(s.cfg.ImagePollTimeoutSecs) * time.Second,
		PollInterval:    time.Duration(s.cfg.ImagePollIntervalSecs) * time.Second,
		PollInitialWait: time.Duration(s.cfg.ImagePollInitialWaitSecs) * time.Second,
		UploadTimeout:   time.Duration(s.cfg.ImagePollTimeoutSecs) * time.Second,
	})
}

func (s *Server) generateImageWithPool(ctx context.Context, prompt, model, size, resolution string, refs [][]byte) ([]upstreamImageResult, error) {
	accounts := s.store.LoadAccounts()
	maxAttempts := len(accounts)
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	excluded := map[string]bool{}
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		traceLogf(ctx, "├─ image account attempt %d/%d model=%s resolution=%s excluded=%d", attempt+1, maxAttempts, model, resolution, len(excluded))
		account, err := s.pickAccountExcluding(model, resolution, excluded)
		if err != nil {
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, err
		}
		client, actualAccount, err := s.upstreamClientForImageAccount(model, resolution, account)
		if err != nil {
			s.accountPool.releaseToken(account.AccessToken)
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, err
		}
		poolToken := account.AccessToken
		token := actualAccount.AccessToken
		traceLogf(ctx, "│  ├─ selected image account %s", accountLabel(actualAccount))
		excluded[poolToken] = true
		excluded[token] = true
		items, client, err := s.generateImageWithReservedEgress(ctx, client, actualAccount, prompt, model, size, resolution, refs)
		attemptedRoutes := map[string]bool{}
		if client != nil {
			attemptedRoutes[client.proxyRoute.URL] = true
		}
		for canRetryImageProxyPreflight(ctx, client, err, model, resolution) {
			sibling, siblingErr := s.newGroupSiblingUpstreamClientForAccount(actualAccount, client.proxyRoute, attemptedRoutes)
			if siblingErr != nil {
				traceLogf(ctx, "│  ├─ image group sibling unavailable: %v", siblingErr)
				break
			}
			if sibling == nil {
				break
			}
			traceLogf(ctx, "│  ├─ retry image preflight with group sibling")
			items, client, err = s.generateImageWithReservedEgressExcluding(ctx, sibling, actualAccount, prompt, model, size, resolution, refs, attemptedRoutes)
			if client != nil {
				attemptedRoutes[client.proxyRoute.URL] = true
			}
		}
		if shouldRetryImageWithFallback(ctx, actualAccount, client, err, model, resolution) {
			fallback, fallbackErr := s.newFallbackUpstreamClientForAccount(actualAccount, client.proxyRoute)
			if fallbackErr != nil {
				traceLogf(ctx, "│  ├─ image fallback unavailable: %v", fallbackErr)
			} else if fallback != nil {
				traceLogf(ctx, "│  ├─ retry image preflight with fallback outlet")
				items, _, err = s.generateImageWithReservedEgress(ctx, fallback, actualAccount, prompt, model, size, resolution, refs)
			}
		}
		if isInvalidTokenErrorText(err) && actualAccount.RefreshToken != nil && strings.TrimSpace(*actualAccount.RefreshToken) != "" {
			traceLogf(ctx, "│  ├─ image 401 received, attempting oauth refresh with rt")
			ctxRefresh, cancel := context.WithTimeout(ctx, 45*time.Second)
			refreshed, refErr := s.refreshOAuthAccount(ctxRefresh, actualAccount.AccessToken)
			cancel()
			if refErr == nil && refreshed.AccessToken != "" {
				actualAccount = refreshed
				token = refreshed.AccessToken
				excluded[token] = true
				if newClient, clientErr := s.newUpstreamClientForAccount(actualAccount); clientErr == nil {
					client = newClient
					items, client, err = s.generateImageWithReservedEgress(ctx, client, actualAccount, prompt, model, size, resolution, refs)
				}
			} else {
				traceLogf(ctx, "│  ├─ reactive oauth refresh failed for image: %v", refErr)
			}
		}
		s.accountPool.releaseToken(poolToken)
		if err == nil && len(items) == 0 {
			err = errNoImageGenerated
		}
		if err == nil {
			traceLogf(ctx, "└─ image account attempt %d success images=%d", attempt+1, len(items))
			s.markAccountSuccess(token, true)
			return items, nil
		}
		traceLogf(ctx, "│  └─ image account attempt %d failed error=%v", attempt+1, err)
		s.markAccountFailure(token, err, true, "")
		lastErr = err
		if !shouldRetryImageAccount(err) {
			return nil, err
		}
		traceLogf(ctx, "│  ├─ retry with another image account")
	}
	if lastErr == nil {
		lastErr = errors.New("no available image quota")
	}
	return nil, lastErr
}

func (s *Server) generateImageWithReservedEgress(ctx context.Context, client *UpstreamClient, account Account, prompt, model, size, resolution string, refs [][]byte) ([]upstreamImageResult, *UpstreamClient, error) {
	return s.generateImageWithReservedEgressExcluding(ctx, client, account, prompt, model, size, resolution, refs, nil)
}

func (s *Server) generateImageWithReservedEgressExcluding(ctx context.Context, client *UpstreamClient, account Account, prompt, model, size, resolution string, refs [][]byte, excluded map[string]bool) ([]upstreamImageResult, *UpstreamClient, error) {
	if client != nil {
		recordImageProxyAttempt(ctx, s.formatProxyRouteLabel(client.proxyRoute))
	}
	route, release, err := s.acquireImageProxyRouteExcluding(ctx, client.proxyRoute, excluded)
	if err != nil {
		return nil, client, err
	}
	defer release()
	if route != client.proxyRoute {
		client, err = s.newUpstreamClientForRoute(account, route)
		if err != nil {
			return nil, client, &proxyConnectionError{cause: redactProxyTransportError(err, route.URL)}
		}
	}
	proxyLabel := s.formatProxyRouteLabel(route)
	recordImageProxyAttempt(ctx, proxyLabel)
	items, err := client.GenerateImage(ctx, prompt, model, size, resolution, refs, s.imageGenerationOptions())
	for i := range items {
		items[i].Proxy = proxyLabel
	}
	return items, client, err
}

func shouldRetryImageWithFallback(ctx context.Context, account Account, client *UpstreamClient, err error, model, resolution string) bool {
	return account.Proxy == "" && client != nil && !client.proxyRoute.Bound && canRetryImageProxyPreflight(ctx, client, err, model, resolution)
}

func canRetryImageProxyPreflight(ctx context.Context, client *UpstreamClient, err error, model, resolution string) bool {
	return err != nil && ctx.Err() == nil && client != nil && client.proxyRoute.URL != "" &&
		!isCodexImageRequest(model, resolution) && !client.imageConversationStarted && isProxyConnectionFailure(err)
}

func (s *Server) generateImagesWithPool(ctx context.Context, prompt, model, size, resolution string, refs [][]byte, n int) ([]upstreamImageResult, error) {
	if n <= 1 {
		return s.generateImageWithPool(ctx, prompt, model, size, resolution, refs)
	}
	limit := s.cfg.ImageAccountConcurrency
	if limit <= 0 {
		limit = 1
	}
	if limit > n {
		limit = n
	}
	sem := make(chan struct{}, limit)
	results := make([][]upstreamImageResult, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-ctx.Done():
				errs[i] = ctx.Err()
				return
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()
			items, err := s.generateImageWithPool(ctx, prompt, model, size, resolution, refs)
			if err != nil {
				errs[i] = err
				return
			}
			results[i] = items
		}()
	}
	wg.Wait()
	out := []upstreamImageResult{}
	var lastErr error
	for i := 0; i < n; i++ {
		if len(results[i]) > 0 {
			out = append(out, results[i][0])
		}
		if errs[i] != nil {
			lastErr = errs[i]
		}
	}
	if len(out) > 0 {
		return out, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("upstream returned no image")
}

func shouldRetryImageAccount(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if isImageDeliveryFailure(err) {
		return true
	}
	if isImageTextReplyFailure(err) {
		return false
	}
	if errors.Is(err, errNoImageGenerated) {
		return true
	}
	return isRateLimitErrorText(err) || isInvalidTokenErrorText(err) || isUpstreamBlockErrorText(err) || isTurnstileRequirementErrorText(err) || isRetryableBootstrapError(err) || isTemporaryUpstreamErrorText(err)
}
