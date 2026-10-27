package app

import (
	"sync"
	"testing"
	"time"
)

func TestAccountPoolAliasesAndInflight(t *testing.T) {
	cfg := &Config{ImageAccountConcurrency: 3}
	pool := newAccountPool(cfg)

	// Inflight tracking
	pool.recordAlias("tok_old", "tok_new")
	if pool.resolveToken("tok_old") != "tok_new" {
		t.Fatalf("expected tok_new, got %s", pool.resolveToken("tok_old"))
	}

	// Multi-hop alias
	pool.recordAlias("tok_new", "tok_final")
	if pool.resolveToken("tok_old") != "tok_final" {
		t.Fatalf("expected tok_final, got %s", pool.resolveToken("tok_old"))
	}

	// Inflight migration
	pool.inflight["tok_final"] = 2
	pool.releaseToken("tok_old")
	if pool.inflight["tok_final"] != 1 {
		t.Fatalf("expected tok_final inflight 1, got %d", pool.inflight["tok_final"])
	}
	pool.releaseToken("tok_final")
	if _, ok := pool.inflight["tok_final"]; ok {
		t.Fatalf("expected tok_final deleted from inflight")
	}
}

func TestAccountPoolLRUScheduling(t *testing.T) {
	cfg := &Config{ImageAccountConcurrency: 2}
	pool := newAccountPool(cfg)

	t1 := "2026-10-01T10:00:00Z"
	t2 := "2026-10-01T09:00:00Z" // older used time -> should be preferred
	accounts := []Account{
		{AccessToken: "tok_1", Status: "正常", LastUsedAt: &t1},
		{AccessToken: "tok_2", Status: "正常", LastUsedAt: &t2},
	}

	// Both have 0 inflight, tok_2 is older, so tok_2 should be picked first
	picked, err := pool.pickTextTokenExcluding(accounts, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if picked != "tok_2" {
		t.Fatalf("expected tok_2 (LRU), got %s", picked)
	}

	// Now tok_2 has inflight=1, tok_1 has inflight=0.
	// tok_1 has less inflight, so tok_1 should be picked next.
	picked2, err := pool.pickTextTokenExcluding(accounts, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if picked2 != "tok_1" {
		t.Fatalf("expected tok_1 (least inflight), got %s", picked2)
	}
}

func TestAccountPoolRefreshSingleFlight(t *testing.T) {
	pool := newAccountPool(&Config{})
	token := "tok_target"

	flight1, isOwner1 := pool.acquireRefreshFlight(token)
	if !isOwner1 {
		t.Fatalf("flight1 should be owner")
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		flight2, isOwner2 := pool.acquireRefreshFlight(token)
		if isOwner2 {
			t.Errorf("flight2 should not be owner")
		}
		<-flight2.done
		if flight2.account.AccessToken != "tok_refreshed" {
			t.Errorf("expected tok_refreshed, got %s", flight2.account.AccessToken)
		}
	}()

	time.Sleep(20 * time.Millisecond)
	flight1.account = Account{AccessToken: "tok_refreshed"}
	pool.releaseRefreshFlight(token, flight1)

	wg.Wait()
}
