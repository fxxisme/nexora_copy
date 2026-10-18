package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	http "github.com/bogdanfinn/fhttp"
)

func TestTextProxyFallbackOnlyBeforeConversationDispatch(t *testing.T) {
	ctx := context.Background()
	connectErr := &proxyConnectionError{cause: errors.New("proxy connection reset")}
	client := &UpstreamClient{proxyRoute: proxyRoute{URL: "http://127.0.0.1:8081"}}
	if !shouldRetryTextWithFallback(ctx, Account{}, client, connectErr, false) {
		t.Fatal("preflight proxy failure should use the fallback outlet")
	}
	client.textConversationStarted = true
	if shouldRetryTextWithFallback(ctx, Account{}, client, connectErr, false) {
		t.Fatal("conversation dispatch must not be repeated")
	}
	client.textConversationStarted = false
	if shouldRetryTextWithFallback(ctx, Account{Proxy: "direct"}, client, connectErr, false) {
		t.Fatal("explicit account outlet must not be replaced")
	}
	if shouldRetryTextWithFallback(ctx, Account{}, client, connectErr, true) {
		t.Fatal("emitted content must not be repeated")
	}
	if shouldRetryTextWithFallback(ctx, Account{}, client, errors.New("upstream rejected request"), false) {
		t.Fatal("application failure must not change outlets")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if shouldRetryTextWithFallback(canceled, Account{}, client, connectErr, false) {
		t.Fatal("canceled request must not retry")
	}
}

func TestTextConversationDispatchBoundary(t *testing.T) {
	connectErr := &proxyConnectionError{cause: errors.New("proxy connection reset")}
	for _, tc := range []struct {
		name       string
		failPath   string
		dispatched bool
	}{
		{name: "bootstrap", failPath: "/", dispatched: false},
		{name: "conversation", failPath: "/backend-api/conversation", dispatched: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &UpstreamClient{
				token: "access-token", userAgent: upstreamUserAgent(),
				proxyRoute: proxyRoute{URL: "http://127.0.0.1:8081"},
				client: proxyRuntimeDoer(func(req *http.Request) (*http.Response, error) {
					if req.URL.Path == tc.failPath {
						return nil, connectErr
					}
					switch req.URL.Path {
					case "/":
						return upstreamTestResponse(200, "<html></html>"), nil
					case "/backend-api/sentinel/chat-requirements/prepare":
						return upstreamTestResponse(200, `{"prepare_token":"prepared"}`), nil
					case "/backend-api/sentinel/chat-requirements/finalize":
						return upstreamTestResponse(200, `{"token":"requirements"}`), nil
					default:
						t.Errorf("unexpected upstream path %q", req.URL.Path)
						return nil, errors.New("unexpected path")
					}
				}),
			}
			events, errs := client.StreamTextConversationEvents(context.Background(), []map[string]any{{"role": "user", "content": "hello"}}, "auto", "")
			for range events {
				t.Fatal("unexpected conversation event")
			}
			if err := <-errs; !isProxyConnectionFailure(err) {
				t.Fatalf("stream error = %v", err)
			}
			if client.textConversationStarted != tc.dispatched {
				t.Fatalf("conversation dispatched = %v, want %v", client.textConversationStarted, tc.dispatched)
			}
			if got := shouldRetryTextWithFallback(context.Background(), Account{}, client, connectErr, false); got == tc.dispatched {
				t.Fatalf("fallback eligible = %v for dispatched=%v", got, tc.dispatched)
			}
		})
	}
}

func TestTextGroupSiblingProxySelection(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	group, err := store.SaveProxyGroup(proxyGroupInput{
		Name: "text", Nodes: []proxyNodeInput{
			{URL: "http://127.0.0.1:8111"},
			{URL: "http://127.0.0.1:8112"},
			{URL: "http://127.0.0.1:8113"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store}
	account := Account{AccessToken: "token", Proxy: "group:" + group.ID}
	first := proxyRoute{URL: group.Nodes[0].URL, NodeID: group.Nodes[0].ID}
	attempted := map[string]bool{first.URL: true}
	second, err := s.newGroupSiblingUpstreamClientForAccount(account, first, attempted)
	if err != nil || second == nil || second.proxyRoute.NodeID != group.Nodes[1].ID {
		t.Fatalf("first sibling = %+v, %v", second, err)
	}
	attempted[second.proxyRoute.URL] = true
	third, err := s.newGroupSiblingUpstreamClientForAccount(account, second.proxyRoute, attempted)
	if err != nil || third == nil || third.proxyRoute.NodeID != group.Nodes[2].ID {
		t.Fatalf("second sibling = %+v, %v", third, err)
	}
	attempted[third.proxyRoute.URL] = true
	if next, err := s.newGroupSiblingUpstreamClientForAccount(account, third.proxyRoute, attempted); err != nil || next != nil {
		t.Fatalf("already attempted sibling = %+v, %v", next, err)
	}
	if next, err := s.newFallbackUpstreamClientForAccount(account, third.proxyRoute); err != nil || next != nil {
		t.Fatalf("account group escaped to global fallback: %+v, %v", next, err)
	}
	if err := store.setProxyNodeHealth(group.Nodes[1].ID, "unhealthy", nil, "CONNECT failed"); err != nil {
		t.Fatal(err)
	}
	if next, err := s.newGroupSiblingUpstreamClientForAccount(account, first, map[string]bool{first.URL: true, third.proxyRoute.URL: true}); err != nil || next != nil {
		t.Fatalf("unhealthy sibling selected: %+v, %v", next, err)
	}
}

func TestTextGroupSiblingRetryOnlyBeforeDispatch(t *testing.T) {
	client := &UpstreamClient{proxyRoute: proxyRoute{URL: "http://127.0.0.1:8111", NodeID: "node"}}
	proxyErr := &proxyConnectionError{cause: errors.New("connection reset")}
	account := Account{Proxy: "group:assigned"}
	if !canRetryTextProxyPreflight(context.Background(), client, proxyErr, false) {
		t.Fatal("group sibling should be eligible before dispatch")
	}
	if shouldRetryTextWithFallback(context.Background(), account, client, proxyErr, false) {
		t.Fatal("account group should not use global fallback")
	}
	client.textConversationStarted = true
	if canRetryTextProxyPreflight(context.Background(), client, proxyErr, false) {
		t.Fatal("conversation dispatch must not be repeated")
	}
}
