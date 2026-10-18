package app

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	http "github.com/bogdanfinn/fhttp"
)

func TestImageProxyRouteUsesOtherNodeAndWaitsForCapacity(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	group, err := store.SaveProxyGroup(proxyGroupInput{
		Name: "image", Nodes: []proxyNodeInput{
			{URL: "http://127.0.0.1:8011", ImageConcurrencyLimit: 1},
			{URL: "http://127.0.0.1:8012", ImageConcurrencyLimit: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store}
	selected := proxyRoute{URL: group.Nodes[0].URL, NodeID: group.Nodes[0].ID}
	first, releaseFirst, err := s.acquireImageProxyRoute(context.Background(), selected)
	if err != nil || first.NodeID != group.Nodes[0].ID {
		t.Fatalf("first route = %+v, %v", first, err)
	}
	second, releaseSecond, err := s.acquireImageProxyRoute(context.Background(), selected)
	if err != nil || second.NodeID != group.Nodes[1].ID {
		t.Fatalf("second route = %+v, %v", second, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, _, err = s.acquireImageProxyRoute(ctx, selected)
	if !errors.Is(err, context.DeadlineExceeded) || !isProxyConnectionFailure(err) {
		t.Fatalf("full group should respect request deadline: %v", err)
	}
	if err := store.SaveAccounts([]Account{{AccessToken: "token", Status: "正常", Quota: 3}}); err != nil {
		t.Fatal(err)
	}
	s.markAccountFailure("token", err, true, "")
	if account := store.LoadAccounts()[0]; account.Fail != 0 || account.Status != "正常" {
		t.Fatalf("egress capacity wait changed account health: %+v", account)
	}
	releaseFirst()
	third, releaseThird, err := s.acquireImageProxyRoute(context.Background(), selected)
	if err != nil || third.NodeID != group.Nodes[0].ID {
		t.Fatalf("released route = %+v, %v", third, err)
	}
	releaseFirst()
	releaseSecond()
	releaseThird()
	if len(s.imageEgressInflight) != 0 {
		t.Fatalf("image egress reservations leaked: %+v", s.imageEgressInflight)
	}
}

func TestImageProxyRouteConcurrentLimit(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	group, err := store.SaveProxyGroup(proxyGroupInput{
		Name: "image", Nodes: []proxyNodeInput{
			{URL: "http://127.0.0.1:8021", ImageConcurrencyLimit: 2},
			{URL: "http://127.0.0.1:8022", ImageConcurrencyLimit: 2},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store}
	selected := proxyRoute{URL: group.Nodes[0].URL, NodeID: group.Nodes[0].ID}
	var mu sync.Mutex
	active := make(map[string]int)
	peak := make(map[string]int)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			route, release, err := s.acquireImageProxyRoute(ctx, selected)
			if err != nil {
				t.Errorf("acquire image route: %v", err)
				return
			}
			mu.Lock()
			active[route.NodeID]++
			peak[route.NodeID] = max(peak[route.NodeID], active[route.NodeID])
			mu.Unlock()
			time.Sleep(10 * time.Millisecond)
			mu.Lock()
			active[route.NodeID]--
			mu.Unlock()
			release()
		}()
	}
	wg.Wait()
	for _, node := range group.Nodes {
		if peak[node.ID] == 0 || peak[node.ID] > 2 {
			t.Fatalf("node %s peak concurrency = %d", node.ID, peak[node.ID])
		}
	}
}

func TestImageProxyRouteURLChangeDoesNotInheritReservation(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	group, err := store.SaveProxyGroup(proxyGroupInput{
		Name: "changing", Nodes: []proxyNodeInput{{URL: "http://127.0.0.1:8041", ImageConcurrencyLimit: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store}
	selected := proxyRoute{NodeID: group.Nodes[0].ID, URL: group.Nodes[0].URL}
	_, releaseOld, err := s.acquireImageProxyRoute(context.Background(), selected)
	if err != nil {
		t.Fatal(err)
	}
	newURL := "http://127.0.0.1:8042"
	_, err = store.SaveProxyGroup(proxyGroupInput{
		ID: group.ID, Name: group.Name, Nodes: []proxyNodeInput{{ID: selected.NodeID, URL: newURL, ImageConcurrencyLimit: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	newRoute, releaseNew, err := s.acquireImageProxyRoute(ctx, selected)
	if err != nil || newRoute.URL != newURL {
		t.Fatalf("new URL inherited old reservation: %+v, %v", newRoute, err)
	}
	releaseOld()
	if got := s.imageEgressInflight[imageProxyRouteKey(selected.NodeID, newURL)]; got != 1 {
		t.Fatalf("old release changed new URL reservation: %d", got)
	}
	releaseNew()
	if len(s.imageEgressInflight) != 0 {
		t.Fatalf("reservations leaked: %+v", s.imageEgressInflight)
	}
}

func TestImageProxyRouteSkipsAttemptedNodes(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	group, err := store.SaveProxyGroup(proxyGroupInput{
		Name: "retry", Nodes: []proxyNodeInput{
			{URL: "http://127.0.0.1:8051", ImageConcurrencyLimit: 1},
			{URL: "http://127.0.0.1:8052", ImageConcurrencyLimit: 1},
			{URL: "http://127.0.0.1:8053", ImageConcurrencyLimit: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store}
	selected := proxyRoute{URL: group.Nodes[0].URL, NodeID: group.Nodes[0].ID}
	excluded := map[string]bool{selected.URL: true}
	second, releaseSecond, err := s.acquireImageProxyRouteExcluding(context.Background(), selected, excluded)
	if err != nil || second.NodeID != group.Nodes[1].ID {
		t.Fatalf("first retry route = %+v, %v", second, err)
	}
	third, releaseThird, err := s.acquireImageProxyRouteExcluding(context.Background(), selected, excluded)
	if err != nil || third.NodeID != group.Nodes[2].ID {
		t.Fatalf("capacity fallback route = %+v, %v", third, err)
	}
	releaseSecond()
	releaseThird()
	excluded[second.URL] = true
	excluded[third.URL] = true
	if _, _, err := s.acquireImageProxyRouteExcluding(context.Background(), selected, excluded); !isProxyConnectionFailure(err) {
		t.Fatalf("all attempted nodes should stop retrying: %v", err)
	}
}

func TestImageProxyReservationReleasedAfterGenerationFailure(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	group, err := store.SaveProxyGroup(proxyGroupInput{
		Name: "image", Nodes: []proxyNodeInput{{URL: "http://127.0.0.1:8031", ImageConcurrencyLimit: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store}
	client := &UpstreamClient{
		token: "token", proxyRoute: proxyRoute{URL: group.Nodes[0].URL, NodeID: group.Nodes[0].ID},
		client: proxyRuntimeDoer(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("connection reset")
		}),
	}
	_, _, err = s.generateImageWithReservedEgress(context.Background(), client, Account{AccessToken: "token"}, "test", "gpt-image-2", "1024x1024", "", nil)
	if err == nil {
		t.Fatal("expected generation failure")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, release, err := s.acquireImageProxyRoute(ctx, client.proxyRoute)
	if err != nil {
		t.Fatalf("reservation was not released: %v", err)
	}
	release()
}
