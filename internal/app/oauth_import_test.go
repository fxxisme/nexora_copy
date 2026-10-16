package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testAccessToken(t *testing.T, exp int64) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"exp": exp})
	if err != nil {
		t.Fatal(err)
	}
	return "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func TestAccessTokenNeedsRefresh(t *testing.T) {
	now := time.Now()
	if !accessTokenNeedsRefresh(testAccessToken(t, now.Add(time.Hour).Unix())) {
		t.Fatal("token expiring within 24 hours should refresh")
	}
	if accessTokenNeedsRefresh(testAccessToken(t, now.Add(48*time.Hour).Unix())) {
		t.Fatal("token with more than 24 hours left should not refresh")
	}
	if accessTokenNeedsRefresh("not-a-jwt") {
		t.Fatal("non-JWT token should not trigger preflight refresh")
	}
}

func TestOAuthRefreshPreservesImportedAccountSource(t *testing.T) {
	for _, tc := range []struct {
		name, source, clientID string
		collision              bool
	}{
		{name: "web", source: "web", clientID: webOAuthClientID},
		{name: "codex", source: "codex", clientID: codexOAuthClientID},
		{name: "collision", source: "web", clientID: webOAuthClientID, collision: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("method = %s", r.Method)
				}
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				if got := r.Form.Get("client_id"); got != tc.clientID {
					t.Errorf("client_id = %q, want %q", got, tc.clientID)
				}
				if got := r.Form.Get("refresh_token"); got != "old-refresh" {
					t.Errorf("refresh_token = %q", got)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","id_token":"new-id","expires_in":3600}`))
			}))
			defer endpoint.Close()

			store := NewStore(t.TempDir())
			refresh := "old-refresh"
			items := []Account{{AccessToken: "old-access", RefreshToken: &refresh, SourceType: tc.source, Status: "正常"}}
			if tc.collision {
				items = append(items, Account{AccessToken: "new-access", Type: "plus", SourceType: "web", Status: "正常", Quota: 7, Success: 4})
			}
			if err := store.SaveAccounts(items); err != nil {
				t.Fatal(err)
			}
			s := &Server{store: store}
			account, err := s.refreshOAuthAccountAt(context.Background(), "old-access", endpoint.URL)
			if err != nil {
				t.Fatal(err)
			}
			if account.AccessToken != "new-access" || account.SourceType != tc.source || ptrString(account.RefreshToken) != "new-refresh" {
				t.Fatalf("refreshed account = %+v", account)
			}
			stored := store.LoadAccounts()
			if len(stored) != 1 || stored[0].AccessToken != "new-access" || stored[0].SourceType != tc.source {
				t.Fatalf("stored accounts = %+v", stored)
			}
			if tc.collision && (stored[0].Type != "plus" || stored[0].Quota != 7 || stored[0].Success != 4) {
				t.Fatalf("existing account data lost during token rotation: %+v", stored[0])
			}
		})
	}
}

func TestOAuthRefreshClassifiesTerminalAndTransientFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		terminal   bool
	}{
		{name: "revoked", status: 400, body: `{"error":"invalid_grant"}`, terminal: true},
		{name: "nested code", status: 401, body: `{"error":{"code":"invalid_refresh_token"}}`, terminal: true},
		{name: "top-level code", status: 400, body: `{"code":"REFRESH_TOKEN_INVALIDATED"}`, terminal: true},
		{name: "throttled", status: 429, body: `{"error":"invalid_grant"}`},
		{name: "upstream unavailable", status: 503, body: `{"error":"invalid_grant"}`},
		{name: "client error", status: 401, body: `{"error":"invalid_client"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer endpoint.Close()
			store := NewStore(t.TempDir())
			refresh := "refresh-token"
			if err := store.SaveAccounts([]Account{{AccessToken: "access-token", RefreshToken: &refresh, Status: "正常"}}); err != nil {
				t.Fatal(err)
			}
			s := &Server{store: store}
			_, err := s.refreshOAuthAccountAt(context.Background(), "access-token", endpoint.URL)
			var refreshErr *oauthRefreshError
			if !errors.As(err, &refreshErr) || terminalOAuthRefreshError(err) != tc.terminal {
				t.Fatalf("oauth failure = %v, terminal want %t", err, tc.terminal)
			}
			if account := store.LoadAccounts()[0]; account.Status != "正常" || account.AccessToken != "access-token" {
				t.Fatalf("oauth failure changed account: %+v", account)
			}
		})
	}
}

func TestOAuthRefreshDoesNotOverwriteChangedRefreshToken(t *testing.T) {
	started := make(chan struct{})
	finish := make(chan struct{})
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-finish
		_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"stale-refresh"}`))
	}))
	defer endpoint.Close()
	store := NewStore(t.TempDir())
	refresh := "old-refresh"
	if err := store.SaveAccounts([]Account{{AccessToken: "old-access", RefreshToken: &refresh, Status: "正常"}}); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store}
	result := make(chan error, 1)
	go func() {
		_, err := s.refreshOAuthAccountAt(context.Background(), "old-access", endpoint.URL)
		result <- err
	}()
	<-started
	if err := store.UpdateAccounts(func(accounts []Account) []Account {
		next := "newer-refresh"
		accounts[0].RefreshToken = &next
		return accounts
	}); err != nil {
		close(finish)
		t.Fatal(err)
	}
	close(finish)
	if err := <-result; err == nil || !strings.Contains(err.Error(), "credentials changed") {
		t.Fatalf("stale OAuth exchange = %v", err)
	}
	account := store.LoadAccounts()[0]
	if account.AccessToken != "old-access" || ptrString(account.RefreshToken) != "newer-refresh" {
		t.Fatalf("stale OAuth exchange overwrote credentials: %+v", account)
	}
}
