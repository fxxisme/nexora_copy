package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	fhttp "github.com/bogdanfinn/fhttp"
)

func TestImageProxyFallbackRequiresPreflightConnectionFailure(t *testing.T) {
	ctx := context.Background()
	client := &UpstreamClient{proxyRoute: proxyRoute{URL: "http://127.0.0.1:8081"}}
	connectErr := &proxyConnectionError{cause: errors.New("proxy connection reset")}
	if !shouldRetryImageWithFallback(ctx, Account{}, client, connectErr, "gpt-image-2", "") {
		t.Fatal("preflight proxy failure should use a fallback outlet")
	}
	client.imageConversationStarted = true
	if shouldRetryImageWithFallback(ctx, Account{}, client, connectErr, "gpt-image-2", "") {
		t.Fatal("conversation dispatch must not be repeated")
	}
	client.imageConversationStarted = false
	if shouldRetryImageWithFallback(ctx, Account{Proxy: "direct"}, client, connectErr, "gpt-image-2", "") {
		t.Fatal("explicit account outlet must not be replaced")
	}
	if shouldRetryImageWithFallback(ctx, Account{}, client, errors.New("upstream returned no image"), "gpt-image-2", "") {
		t.Fatal("application failure must not change outlets")
	}
	if !shouldRetryImageWithFallback(ctx, Account{}, client, &upstreamStatusError{StatusCode: http.StatusProxyAuthRequired}, "gpt-image-2", "") {
		t.Fatal("proxy authentication failure should use a fallback outlet")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if shouldRetryImageWithFallback(canceled, Account{}, client, connectErr, "gpt-image-2", "") {
		t.Fatal("canceled request must not retry")
	}
}

func TestImageGroupSiblingRetryBoundary(t *testing.T) {
	client := &UpstreamClient{proxyRoute: proxyRoute{URL: "http://127.0.0.1:8081", NodeID: "node"}}
	proxyErr := &proxyConnectionError{cause: errors.New("connection reset")}
	account := Account{Proxy: "group:assigned"}
	if !canRetryImageProxyPreflight(context.Background(), client, proxyErr, "gpt-image-2", "") {
		t.Fatal("group sibling should be eligible before dispatch")
	}
	if shouldRetryImageWithFallback(context.Background(), account, client, proxyErr, "gpt-image-2", "") {
		t.Fatal("account group should not use global fallback")
	}
	client.imageConversationStarted = true
	if canRetryImageProxyPreflight(context.Background(), client, proxyErr, "gpt-image-2", "") {
		t.Fatal("generation dispatch must not be repeated")
	}
	client.imageConversationStarted = false
	if canRetryImageProxyPreflight(context.Background(), client, proxyErr, "plus-codex-gpt-image-2", "") {
		t.Fatal("codex image path must not use web proxy retry")
	}
}

func TestImageBootstrapProxyFailureStopsBeforeDispatch(t *testing.T) {
	attempts := 0
	route := proxyRoute{URL: "http://127.0.0.1:8081"}
	client := &UpstreamClient{token: "token", proxyRoute: route}
	client.client = &proxyHealthDoer{route: route, next: proxyRuntimeDoer(func(*fhttp.Request) (*fhttp.Response, error) {
		attempts++
		return nil, errors.New("connection reset")
	})}
	_, err := client.GenerateImage(context.Background(), "test", "gpt-image-2", "1024x1024", "", nil, imageGenerationOptions{})
	if !isProxyConnectionFailure(err) || client.imageConversationStarted || attempts != 1 {
		t.Fatalf("bootstrap failure: error=%v dispatched=%v attempts=%d", err, client.imageConversationStarted, attempts)
	}
}

func TestBootstrapProxyAuthResponseIsConnectionFailure(t *testing.T) {
	attempts := 0
	client := &UpstreamClient{client: proxyRuntimeDoer(func(*fhttp.Request) (*fhttp.Response, error) {
		attempts++
		return &fhttp.Response{StatusCode: fhttp.StatusProxyAuthRequired, Header: fhttp.Header{}, Body: io.NopCloser(strings.NewReader("proxy authentication required"))}, nil
	})}
	if err := client.bootstrap(context.Background()); !isProxyConnectionFailure(err) || attempts != 1 {
		t.Fatalf("bootstrap proxy auth: error=%v attempts=%d", err, attempts)
	}
}

func TestImageFallbackUsesDistinctConfiguredOutlet(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	primary, err := store.SaveProxyGroup(proxyGroupInput{Name: "primary", Nodes: []proxyNodeInput{{URL: "http://127.0.0.1:8081"}}})
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := store.SaveProxyGroup(proxyGroupInput{Name: "fallback", Nodes: []proxyNodeInput{{URL: "http://127.0.0.1:8082"}}})
	if err != nil {
		t.Fatal(err)
	}
	fallbackRef := proxyReference{Mode: "group", GroupID: fallback.ID}
	if err := store.SaveProxyDefaults(proxyReference{Mode: "group", GroupID: primary.ID}, &fallbackRef); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store}
	account := Account{AccessToken: "token"}
	first, err := s.newUpstreamClientForAccount(account)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.newFallbackUpstreamClientForAccount(account, first.proxyRoute)
	if err != nil || second == nil || second.proxyRoute.NodeID != fallback.Nodes[0].ID {
		t.Fatalf("fallback client = %+v, %v", second, err)
	}
	if next, err := s.newFallbackUpstreamClientForAccount(account, second.proxyRoute); err != nil || next != nil {
		t.Fatalf("selected the same fallback outlet again: %+v, %v", next, err)
	}
	if next, err := s.newFallbackUpstreamClientForAccount(Account{AccessToken: "token", Proxy: "group:" + primary.ID}, first.proxyRoute); err != nil || next != nil {
		t.Fatalf("explicit account proxy used fallback: %+v, %v", next, err)
	}
}

func TestProxyConnectionFailureDoesNotCountAgainstAccount(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	if err := store.SaveAccounts([]Account{{AccessToken: "token", Status: "正常", Quota: 3}}); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store}
	s.markAccountFailure("token", &proxyConnectionError{cause: errors.New("connection reset")}, true, "")
	s.markAccountFailure("token", &upstreamStatusError{StatusCode: http.StatusProxyAuthRequired}, true, "")
	account := store.LoadAccounts()[0]
	if account.Fail != 0 || account.Status != "正常" || account.Quota != 3 {
		t.Fatalf("proxy failure changed account health: %+v", account)
	}
}
