package app

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestConcurrentAuthenticationPreservesQuotaUsage(t *testing.T) {
	store := NewStore(t.TempDir())
	defer store.Close()
	const requests = 40
	if err := store.SaveAuthKeys([]UserKey{{
		ID: "user-1", Role: "user", Name: "Test", Enabled: true, KeyHash: hashKey("sk-test"),
		ChatDailyQuota: requests, ChatMonthlyQuota: requests, ChatTotalQuota: requests,
		ChatDailyResetAt: todayKey(), ChatMonthlyResetAt: monthKey(),
	}}); err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: Config{AuthKey: "admin-key"}, store: store}
	var wg sync.WaitGroup
	errs := make(chan string, requests)
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
			req.Header.Set("Authorization", "Bearer sk-test")
			identity, ok := s.requireIdentity(httptest.NewRecorder(), req)
			if !ok {
				errs <- "authentication failed"
				return
			}
			if !s.consumeChat(identity, 1) {
				errs <- "quota charge failed"
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	keys := store.LoadAuthKeys()
	if len(keys) != 1 || keys[0].ChatTotalUsed != requests || keys[0].ChatDailyUsed != requests {
		t.Fatalf("quota usage after concurrent authentication = %+v", keys)
	}
}
