package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSelectAccountsForRefreshOnlyIncludesRequestedTokens(t *testing.T) {
	accounts := []Account{{AccessToken: "first"}, {AccessToken: "second"}, {AccessToken: "third"}}
	selected, missing := selectAccountsForRefresh(accounts, []string{" second ", "missing", "second"})
	if len(selected) != 1 || selected[0].AccessToken != "second" || missing != 1 {
		t.Fatalf("selected=%+v missing=%d", selected, missing)
	}
	selected, missing = selectAccountsForRefresh(accounts, nil)
	if len(selected) != len(accounts) || missing != 0 {
		t.Fatalf("all accounts selected=%+v missing=%d", selected, missing)
	}
}

func TestAccountRefreshProgressReportsConcurrentResults(t *testing.T) {
	jobs := &accountRefreshJobs{}
	id, err := jobs.create(20)
	if err != nil {
		t.Fatal(err)
	}
	accounts := make([]Account, 20)
	for i := range accounts {
		accounts[i].AccessToken = fmt.Sprintf("token-%d", i)
	}
	runAccountRefreshBatchReport(context.Background(), accounts, func(_ context.Context, account Account) error {
		if strings.HasSuffix(account.AccessToken, "1") {
			return errors.New("test failure")
		}
		return nil
	}, func(index int, err error) {
		if err != nil {
			jobs.record(id, &accountRefreshFailure{Account: accounts[index].AccessToken, Error: err.Error()})
		} else {
			jobs.record(id, nil)
		}
	})
	jobs.finish(id)
	progress, ok := jobs.get(id)
	if !ok || !progress.Done || progress.Processed != 20 || progress.Refreshed != 18 || progress.Failed != 2 || len(progress.Errors) != 2 {
		t.Fatalf("progress=%+v exists=%t", progress, ok)
	}
	progress.Errors[0].Error = "changed"
	again, _ := jobs.get(id)
	if again.Errors[0].Error == "changed" {
		t.Fatal("progress snapshot aliases stored errors")
	}
}

func TestAccountRefreshProgressRoutesRequireAdmin(t *testing.T) {
	s := &Server{cfg: Config{AuthKey: "admin-key"}, store: NewStore(t.TempDir()), mux: http.NewServeMux()}
	s.routes()
	start := func(auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/accounts/refresh/start", strings.NewReader(`{"access_tokens":["deleted-token"]}`))
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, req)
		return rec
	}
	if rec := start(""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized start status=%d", rec.Code)
	}
	rec := start("admin-key")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start status=%d body=%s", rec.Code, rec.Body.String())
	}
	var started struct {
		ID string `json:"progress_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil || started.ID == "" {
		t.Fatalf("start response=%s err=%v", rec.Body.String(), err)
	}
	path := "/api/accounts/refresh/progress/" + started.ID
	check := func(auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, req)
		return rec
	}
	if rec := check(""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized progress status=%d", rec.Code)
	}
	deadline := time.Now().Add(time.Second)
	for {
		rec = check("admin-key")
		if rec.Code != http.StatusOK {
			t.Fatalf("progress status=%d body=%s", rec.Code, rec.Body.String())
		}
		var progress accountRefreshProgress
		if err := json.Unmarshal(rec.Body.Bytes(), &progress); err != nil {
			t.Fatal(err)
		}
		if progress.Done {
			if progress.Total != 1 || progress.Processed != 1 || progress.Failed != 1 || strings.Contains(rec.Body.String(), "deleted-token") {
				t.Fatalf("unexpected progress=%s", rec.Body.String())
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("refresh progress did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAccountRefreshProgressStoreConcurrentRead(t *testing.T) {
	jobs := &accountRefreshJobs{}
	id, err := jobs.create(100)
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for range 10 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 10 {
				jobs.record(id, nil)
				_, _ = jobs.get(id)
			}
		}()
	}
	workers.Wait()
	progress, _ := jobs.get(id)
	if progress.Processed != 100 {
		t.Fatalf("processed=%d", progress.Processed)
	}
}

func TestAccountRefreshProgressKeepsActiveJobsUntilCompletion(t *testing.T) {
	jobs := &accountRefreshJobs{}
	id, err := jobs.create(1)
	if err != nil {
		t.Fatal(err)
	}
	jobs.mu.Lock()
	jobs.items[id].updatedAt = time.Now().Add(-2 * time.Hour)
	jobs.mu.Unlock()
	if _, ok := jobs.get(id); !ok {
		t.Fatal("active job expired before completion")
	}
	if _, err := jobs.create(1); err != nil {
		t.Fatal(err)
	}
	jobs.finish(id)
	jobs.mu.Lock()
	jobs.items[id].updatedAt = time.Now().Add(-11 * time.Minute)
	jobs.mu.Unlock()
	if _, ok := jobs.get(id); ok {
		t.Fatal("completed job was retained past its TTL")
	}
}
