package app

import (
	"context"
	"errors"
	stdhttp "net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	http "github.com/bogdanfinn/fhttp"
)

type proxyRuntimeDoer func(*http.Request) (*http.Response, error)

func (d proxyRuntimeDoer) Do(req *http.Request) (*http.Response, error) { return d(req) }

func TestProxyRuntimeFailureSkipsNodeAndHTTPResponseRecoversIt(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	group, err := store.SaveProxyGroup(proxyGroupInput{
		Name: "runtime", Strategy: "round_robin",
		Nodes: []proxyNodeInput{{URL: "http://user:secret@127.0.0.1:8041"}, {URL: "http://127.0.0.1:8042"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveProxyDefaults(proxyReference{Mode: "group", GroupID: group.ID}, nil); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store}
	route, err := s.selectedProxyRoute()
	if err != nil || route.NodeID != group.Nodes[0].ID {
		t.Fatalf("selected route = %+v, %v", route, err)
	}
	req, err := http.NewRequest(http.MethodGet, "https://chatgpt.com/backend-api/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("proxyconnect tcp http://user:secret@127.0.0.1:8041: connection reset")
	doer := &proxyHealthDoer{next: proxyRuntimeDoer(func(*http.Request) (*http.Response, error) { return nil, cause }), store: store, route: route}
	if _, err := doer.Do(req); !errors.Is(err, cause) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("transport error = %v", err)
	}
	groups, err := store.LoadProxyGroups()
	if err != nil {
		t.Fatal(err)
	}
	if groups[0].Nodes[0].Health.State != "unhealthy" || groups[0].Nodes[0].FailCount != 1 {
		t.Fatalf("failed node health = %+v", groups[0].Nodes[0])
	}
	next, err := s.selectedProxyRoute()
	if err != nil || next.NodeID != group.Nodes[1].ID {
		t.Fatalf("next route = %+v, %v", next, err)
	}
	doer.next = proxyRuntimeDoer(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized}, nil
	})
	if _, err := doer.Do(req); err != nil {
		t.Fatal(err)
	}
	groups, err = store.LoadProxyGroups()
	if err != nil {
		t.Fatal(err)
	}
	if groups[0].Nodes[0].Health.State != "healthy" || groups[0].Nodes[0].SuccessCount != 1 {
		t.Fatalf("recovered node health = %+v", groups[0].Nodes[0])
	}
}

func TestProxyRuntimeIgnoresOtherTargetsCancellationAndChangedNode(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	group, err := store.SaveProxyGroup(proxyGroupInput{Name: "runtime", Nodes: []proxyNodeInput{{URL: "http://127.0.0.1:8051"}}})
	if err != nil {
		t.Fatal(err)
	}
	route := proxyRoute{URL: group.Nodes[0].URL, NodeID: group.Nodes[0].ID}
	doer := &proxyHealthDoer{next: proxyRuntimeDoer(func(*http.Request) (*http.Response, error) { return nil, errors.New("connection reset") }), store: store, route: route}
	other, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	_, _ = doer.Do(other)
	ctx, cancel := context.WithCancel(context.Background())
	canceled, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://chatgpt.com/", nil)
	cancel()
	_, _ = doer.Do(canceled)
	if state := proxyNodeState(t, store); state != "unknown" {
		t.Fatalf("unrelated failure changed health to %q", state)
	}
	if _, err := store.db.Exec("UPDATE app_proxy_nodes SET url="+store.bind(1)+" WHERE id="+store.bind(2), "http://127.0.0.1:8052", route.NodeID); err != nil {
		t.Fatal(err)
	}
	current, _ := http.NewRequest(http.MethodGet, "https://chatgpt.com/", nil)
	_, _ = doer.Do(current)
	if state := proxyNodeState(t, store); state != "unknown" {
		t.Fatalf("stale route changed edited node health to %q", state)
	}
}

func TestUpstreamClientTracksSelectedProxyGroupNode(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	group, err := store.SaveProxyGroup(proxyGroupInput{Name: "runtime", Nodes: []proxyNodeInput{{URL: "http://127.0.0.1:8061"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveProxyDefaults(proxyReference{Mode: "group", GroupID: group.ID}, nil); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store}
	client, err := s.newUpstreamClientForAccount(Account{})
	if err != nil {
		t.Fatal(err)
	}
	tracked, ok := client.client.(*proxyHealthDoer)
	if !ok || tracked.route.NodeID != group.Nodes[0].ID {
		t.Fatalf("upstream transport not associated with proxy group node: %T", client.client)
	}
}

func TestProxyRuntimeRejectsProxyAuthResponse(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	group, err := store.SaveProxyGroup(proxyGroupInput{Name: "auth", Nodes: []proxyNodeInput{{URL: "http://127.0.0.1:8071"}}})
	if err != nil {
		t.Fatal(err)
	}
	route := proxyRoute{URL: group.Nodes[0].URL, NodeID: group.Nodes[0].ID}
	doer := &proxyHealthDoer{next: proxyRuntimeDoer(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusProxyAuthRequired}, nil
	}), store: store, route: route}
	req, _ := http.NewRequest(http.MethodGet, "https://chatgpt.com/", nil)
	if _, err := doer.Do(req); err != nil {
		t.Fatal(err)
	}
	if state := proxyNodeState(t, store); state != "unhealthy" {
		t.Fatalf("proxy authentication response changed health to %q", state)
	}
}

func TestTLSClientConnectFailureMarksProxyNodeUnhealthy(t *testing.T) {
	t.Setenv("CHATGPT2API_UPSTREAM_TRANSPORT", "")
	var connects atomic.Int32
	proxy := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if r.Method != stdhttp.MethodConnect {
			t.Errorf("proxy method = %s, want CONNECT", r.Method)
		}
		connects.Add(1)
		conn, _, err := w.(stdhttp.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	defer proxy.Close()
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	group, err := store.SaveProxyGroup(proxyGroupInput{
		Name: "connect-reset", Strategy: "round_robin",
		Nodes: []proxyNodeInput{{URL: proxy.URL}, {URL: "http://127.0.0.1:8099"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveProxyDefaults(proxyReference{Mode: "group", GroupID: group.ID}, nil); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store}
	client, err := s.newUpstreamClientForAccount(Account{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.bootstrap(ctx); !isProxyConnectionFailure(err) {
		t.Fatalf("bootstrap error = %v, want proxy connection failure", err)
	}
	if connects.Load() != 1 {
		t.Fatalf("CONNECT attempts = %d, want 1", connects.Load())
	}
	next, err := s.selectedProxyRoute()
	if err != nil || next.NodeID != group.Nodes[1].ID {
		t.Fatalf("next route after reset = %+v, %v", next, err)
	}
}

func proxyNodeState(t *testing.T, store *Store) string {
	t.Helper()
	groups, err := store.LoadProxyGroups()
	if err != nil {
		t.Fatal(err)
	}
	return groups[0].Nodes[0].Health.State
}
