package app

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSQLiteStoreImportsLegacyOnce(t *testing.T) {
	dir := t.TempDir()
	legacy := `[{"access_token":"legacy-token","status":"正常"}]`
	if err := os.WriteFile(filepath.Join(dir, "accounts.json"), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth_keys.json"), []byte(`[{"id":"legacy-key","name":"old"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "logs.jsonl"), []byte(`{"id":"old-log","time":"2026-10-15 10:00:00","type":"call","detail":{"status":"success"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(dir, "sqlite", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.LoadAccounts(); len(got) != 1 || got[0].AccessToken != "legacy-token" {
		t.Fatalf("accounts = %+v", got)
	}
	if got := s.LoadAuthKeys(); len(got) != 1 || got[0].ID != "legacy-key" {
		t.Fatalf("auth keys = %+v", got)
	}
	logs := newLogService(dir)
	logs.store = s
	if got := logs.list("", "", "", 10); len(got) != 1 {
		t.Fatalf("logs = %+v", got)
	}
	if err := s.SaveAccounts([]Account{{AccessToken: "sql-token"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "accounts.json"), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	s, err = openStore(dir, "sqlite", "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := s.LoadAccounts(); len(got) != 1 || got[0].AccessToken != "sql-token" {
		t.Fatalf("reopened accounts = %+v", got)
	}
	logs.store = s
	if got := logs.list("", "", "", 10); len(got) != 1 {
		t.Fatalf("duplicate imported logs = %+v", got)
	}
}

func TestSQLiteStoreConcurrentUpdates(t *testing.T) {
	dir := t.TempDir()
	a := NewStore(dir)
	defer a.Close()
	if err := a.SaveAccounts([]Account{{AccessToken: "initial"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "accounts.json")); !os.IsNotExist(err) {
		t.Fatalf("business JSON file created: %v", err)
	}
	if err := a.SaveAccounts([]Account{}); err != nil {
		t.Fatal(err)
	}
	b := NewStore(dir)
	defer b.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store := a
			if i%2 == 0 {
				store = b
			}
			errs <- store.UpdateAccounts(func(items []Account) []Account {
				return append(items, Account{AccessToken: "token-" + string(rune('a'+i))})
			})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := a.LoadAccounts(); len(got) != 20 {
		t.Fatalf("accounts = %d, want 20", len(got))
	}
}

func TestSQLiteStoreMigratesExistingCollectionDocuments(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if _, err := s.db.Exec("INSERT INTO app_documents (name, payload) VALUES (?, ?)", "accounts.json", `[{"access_token":"`+strings.Repeat("x", 3500)+`","quota":7}]`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO app_documents (name, payload) VALUES (?, ?)", "auth_keys.json", `{"items":[{"id":"key-1","name":"Legacy"}]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("DELETE FROM app_migrations WHERE name = 'account_rows_v1'"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(dir, "sqlite", "")
	if err != nil {
		t.Fatal(err)
	}
	accounts := s.LoadAccounts()
	if len(accounts) != 1 || len(accounts[0].AccessToken) != 3500 || accounts[0].Quota != 7 {
		t.Fatalf("migrated accounts = %+v", accounts)
	}
	if keys := s.LoadAuthKeys(); len(keys) != 1 || keys[0].ID != "key-1" {
		t.Fatalf("migrated auth keys = %+v", keys)
	}
	if err := s.SaveAccounts([]Account{{AccessToken: "new-token"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = openStore(dir, "sqlite", "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if accounts := s.LoadAccounts(); len(accounts) != 1 || accounts[0].AccessToken != "new-token" {
		t.Fatalf("stale collection document replaced rows: %+v", accounts)
	}
}

func TestAccountRowMigrationRollsBackOnDuplicateKeys(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if _, err := s.db.Exec("INSERT INTO app_documents (name, payload) VALUES (?, ?)", "accounts.json", `[{"access_token":"legacy-token"}]`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO app_documents (name, payload) VALUES (?, ?)", "auth_keys.json", `{"items":[{"id":"same"},{"id":"same"}]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("DELETE FROM app_migrations WHERE name = 'account_rows_v1'"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := openStore(dir, "sqlite", ""); err == nil || !strings.Contains(err.Error(), "migrate auth key") {
		t.Fatalf("migration error = %v", err)
	}
	raw, err := sql.Open("sqlite", filepath.Join(dir, "chatgpt2api.db"))
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := raw.QueryRow("SELECT COUNT(*) FROM app_accounts").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("account rows committed despite failed migration: %d", count)
	}
	if _, err := raw.Exec("UPDATE app_documents SET payload = ? WHERE name = 'auth_keys.json'", `{"items":[{"id":"first"},{"id":"second"}]}`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = openStore(dir, "sqlite", "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(s.LoadAccounts()) != 1 || len(s.LoadAuthKeys()) != 2 {
		t.Fatalf("retry lost data: accounts=%d auth_keys=%d", len(s.LoadAccounts()), len(s.LoadAuthKeys()))
	}
}

func TestSQLiteLogQueryBackfillsAndFiltersInDatabase(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	legacy := map[string]any{
		"id": "legacy-call", "time": "2026-10-16 12:30:00", "type": "call", "summary": "Legacy summary",
		"detail": map[string]any{"status": "success", "endpoint": "/v1/chat/completions", "model": "auto", "request_text": "needle text"},
	}
	payload, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO app_logs (id, payload) VALUES (?, ?)", "legacy-call", string(payload)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("DELETE FROM app_migrations WHERE name = 'log_columns_v1'"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = openStore(dir, "sqlite", "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	logs := newLogService(dir)
	logs.store = s
	items, err := s.queryLogs(logQuery{typ: "call", startDate: "2026-10-16", endDate: "2026-10-16", status: "success", endpoint: "/v1/chat/completions", model: "auto", search: "NEEDLE"}, 10)
	if err != nil || len(items) != 1 || items[0]["id"] != "legacy-call" {
		t.Fatalf("query items=%+v err=%v", items, err)
	}
	now := time.Date(2026, 10, 16, 12, 45, 0, 0, time.Local)
	buckets := logs.dashboardBuckets("24h", now)
	last := buckets[len(buckets)-1]
	if last.Success != 1 || last.Models["auto"] != 1 {
		t.Fatalf("dashboard current-hour bucket=%+v", last)
	}
	logs.add("call", "new call", map[string]any{"status": "failed", "model": "gpt-image-2", "error": "network"})
	items, err = s.queryLogs(logQuery{status: "failed", model: "gpt-image-2"}, 1)
	if err != nil || len(items) != 1 || items[0]["summary"] != "new call" {
		t.Fatalf("new record query items=%+v err=%v", items, err)
	}
}

func TestStoreRejectsUnsupportedBackend(t *testing.T) {
	if _, err := openStore(t.TempDir(), "json", ""); err == nil {
		t.Fatal("JSON storage was accepted")
	}
	if _, err := openStore(t.TempDir(), "postgres", ""); err == nil {
		t.Fatal("missing PostgreSQL DSN was accepted")
	}
}

func TestResolveStoreConfigUsesDatabaseURL(t *testing.T) {
	tests := []struct {
		name, backend, databaseURL, wantBackend string
		wantError                               bool
	}{
		{name: "default", wantBackend: "sqlite"},
		{name: "postgres URL only", databaseURL: "postgresql://localhost/app", wantBackend: "postgres"},
		{name: "postgres URL with old sqlite default", backend: "sqlite", databaseURL: "postgres://localhost/app", wantBackend: "postgres"},
		{name: "sqlite URL", databaseURL: "sqlite:///tmp/app.db", wantBackend: "sqlite"},
		{name: "legacy JSON backend", backend: "json", wantError: true},
		{name: "postgres without URL", backend: "postgres", wantError: true},
		{name: "invalid URL", databaseURL: "mysql://localhost/app", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend, databaseURL, err := resolveStoreConfig(tt.backend, tt.databaseURL)
			if (err != nil) != tt.wantError {
				t.Fatalf("error = %v, want error %t", err, tt.wantError)
			}
			if !tt.wantError && (backend != tt.wantBackend || databaseURL != tt.databaseURL) {
				t.Fatalf("config = (%q, %q), want (%q, %q)", backend, databaseURL, tt.wantBackend, tt.databaseURL)
			}
		})
	}
}

func TestPostgresStoreIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_URL is not set")
	}
	adminDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("chatgpt2api_test_%d", time.Now().UnixNano())
	if _, err := adminDB.Exec("CREATE SCHEMA " + schema); err != nil {
		adminDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := adminDB.Exec("DROP SCHEMA " + schema + " CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
		adminDB.Close()
	})
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	params := parsed.Query()
	params.Set("search_path", schema)
	parsed.RawQuery = params.Encode()
	dsn = parsed.String()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "accounts.json"), []byte(`[{"access_token":"postgres-legacy"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(dir, "postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.LoadAccounts(); len(got) != 1 || got[0].AccessToken != "postgres-legacy" {
		t.Fatalf("imported accounts = %+v", got)
	}
	if err := s.UpdateAccounts(func(items []Account) []Account { return append(items, Account{AccessToken: "postgres-new"}) }); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAuthKeys([]UserKey{{ID: "postgres-key", ChatTotalUsed: 0}}); err != nil {
		t.Fatal(err)
	}
	other, err := openStore(dir, "postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			client := s
			if i%2 == 0 {
				client = other
			}
			errs <- client.UpdateAccounts(func(items []Account) []Account {
				return append(items, Account{AccessToken: "concurrent-" + string(rune('a'+i))})
			})
		}(i)
		go func(i int) {
			defer wg.Done()
			client := s
			if i%2 == 0 {
				client = other
			}
			errs <- client.UpdateAuthKeys(func(items []UserKey) []UserKey {
				items[0].ChatTotalUsed++
				return items
			})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	logs := newLogService(dir)
	logs.store = s
	logs.add("call", "integration", map[string]any{"status": "success"})
	if got := logs.list("call", "", "", 10); len(got) != 1 {
		t.Fatalf("logs = %+v", got)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = openStore(dir, "postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := s.LoadAccounts(); len(got) != 22 {
		t.Fatalf("reopened accounts = %+v", got)
	}
	if got := s.LoadAuthKeys(); len(got) != 1 || got[0].ChatTotalUsed != 20 {
		t.Fatalf("reopened auth keys = %+v", got)
	}
}
