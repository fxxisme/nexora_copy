package app

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	http "github.com/bogdanfinn/fhttp"
)

func TestAccountRefreshBatchBoundsConcurrency(t *testing.T) {
	accounts := make([]Account, 20)
	for i := range accounts {
		accounts[i].AccessToken = fmt.Sprintf("token-%d", i)
	}
	var active, maximum atomic.Int32
	results := runAccountRefreshBatch(context.Background(), accounts, func(_ context.Context, _ Account) error {
		current := active.Add(1)
		for previous := maximum.Load(); current > previous && !maximum.CompareAndSwap(previous, current); previous = maximum.Load() {
		}
		time.Sleep(15 * time.Millisecond)
		active.Add(-1)
		return nil
	})
	if maximum.Load() < 2 || maximum.Load() > 8 {
		t.Fatalf("maximum concurrent refreshes = %d, want 2..8", maximum.Load())
	}
	for i, err := range results {
		if err != nil {
			t.Fatalf("account %d refresh: %v", i, err)
		}
	}
}

func TestAccountRefreshBatchHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	results := runAccountRefreshBatch(ctx, []Account{{AccessToken: "one"}}, func(context.Context, Account) error {
		t.Fatal("canceled batch sent an upstream request")
		return nil
	})
	if len(results) != 1 || !errors.Is(results[0], context.Canceled) {
		t.Fatalf("canceled batch results = %v", results)
	}
}

func TestLimitedWatcherRefreshPreservesConcurrentChanges(t *testing.T) {
	store := NewStore(t.TempDir())
	limitedAt := "2026-10-16T00:00:00Z"
	observed := Account{AccessToken: "token", Type: "Plus", Status: "限流", Fail: 1, RateLimitedAt: &limitedAt}
	if err := store.SaveAccounts([]Account{observed}); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store}
	if err := store.UpdateAccounts(func(accounts []Account) []Account {
		accounts[0].Success = 3
		return accounts
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.applyLimitedAccountRefresh(observed, Account{Status: "正常", Type: "free", Quota: 4}); err != nil {
		t.Fatal(err)
	}
	account := store.LoadAccounts()[0]
	if account.Status != "正常" || account.Type != "free" || account.Quota != 4 || account.Success != 3 || account.RateLimitedAt != nil {
		t.Fatalf("confirmed quota refresh = %+v", account)
	}
	if err := store.UpdateAccounts(func(accounts []Account) []Account {
		accounts[0].Status = "限流"
		accounts[0].Fail = 2
		return accounts
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.applyLimitedAccountRefresh(observed, Account{Status: "正常", Quota: 7}); err != nil {
		t.Fatal(err)
	}
	account = store.LoadAccounts()[0]
	if account.Status != "限流" || account.Fail != 2 || account.Quota != 4 {
		t.Fatalf("stale watcher result changed account = %+v", account)
	}
}

func TestAccountInfoRefreshDoesNotOverwriteNewerUsage(t *testing.T) {
	store := NewStore(t.TempDir())
	observed := Account{AccessToken: "token", Status: "正常", Quota: 5}
	if err := store.SaveAccounts([]Account{observed}); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store}
	if err := store.UpdateAccounts(func(accounts []Account) []Account {
		accounts[0].Fail++
		accounts[0].Status = "限流"
		accounts[0].Quota = 0
		return accounts
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.applyAccountInfoRefresh(observed, Account{Status: "正常", Quota: 5}); err == nil {
		t.Fatal("stale account refresh unexpectedly succeeded")
	}
	account := store.LoadAccounts()[0]
	if account.Status != "限流" || account.Quota != 0 || account.Fail != 1 {
		t.Fatalf("stale refresh changed newer account state = %+v", account)
	}
}

func TestUnknownQuotaDoesNotReleaseLimitedAccount(t *testing.T) {
	limitedAt := "2026-10-16T00:00:00Z"
	account := Account{AccessToken: "token", Type: "Plus", Status: "限流", RateLimitedAt: &limitedAt}
	mergeRefreshedAccount(&account, Account{Status: "正常", ImageQuotaUnknown: true})
	if account.Status != "限流" || account.Type != "Plus" || account.RateLimitedAt == nil {
		t.Fatalf("unknown quota refresh changed limited state = %+v", account)
	}
}

func TestGetUserInfoDoesNotGuessPlanOnCheckFailure(t *testing.T) {
	client := &UpstreamClient{token: "token", client: upstreamDoerFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/backend-api/me":
			return upstreamTestResponse(200, `{"email":"user@example.com"}`), nil
		case "/backend-api/conversation/init", "/backend-api/accounts/check/v4-2023-04-27":
			return upstreamTestResponse(503, "unavailable"), nil
		default:
			t.Fatalf("unexpected account info path %s", req.URL.Path)
			return nil, nil
		}
	})}
	info, err := client.GetUserInfo(context.Background())
	if err != nil || info.Type != "" || !info.ImageQuotaUnknown {
		t.Fatalf("account info type=%q unknown=%t err=%v", info.Type, info.ImageQuotaUnknown, err)
	}
}

func TestChineseImageQuotaFailureSetsRestoreTime(t *testing.T) {
	err := errors.New("你已达到 Free 套餐的图像生成请求上限。上限将在 12小时 后重置")
	if !isRateLimitErrorText(err) || rateLimitRestoreDelay(err) != 12*time.Hour {
		t.Fatalf("quota error classification=%t delay=%s", isRateLimitErrorText(err), rateLimitRestoreDelay(err))
	}
}
