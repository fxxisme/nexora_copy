package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDashboardBucketsCountCompletedCalls(t *testing.T) {
	dir := t.TempDir()
	logs := newLogService(dir)
	now := time.Now().Local().Truncate(time.Hour)
	stamp := now.Format("2006-01-02 15:04:05")
	old := now.Add(-31 * 24 * time.Hour).Format("2006-01-02 15:04:05")
	lines := []string{
		`{"time":"` + stamp + `","type":"call","detail":{"status":"started","model":"auto"}}`,
		`{"time":"` + stamp + `","type":"call","detail":{"status":"success","model":"auto"}}`,
		`{"time":"` + stamp + `","type":"call","detail":{"status":"partial_success","model":"gpt-image-2"}}`,
		`{"time":"` + stamp + `","type":"call","detail":{"status":"failed","model":"gpt-image-2"}}`,
		`{"time":"` + stamp + `","type":"account","detail":{"status":"success","model":"auto"}}`,
		`{"time":"` + old + `","type":"call","detail":{"status":"success","model":"auto"}}`,
	}
	if err := os.WriteFile(filepath.Join(dir, "logs.jsonl"), []byte(strings.Join(lines, "\n")), 0600); err != nil {
		t.Fatal(err)
	}
	for _, rangeName := range []string{"24h", "7d", "30d"} {
		buckets := logs.dashboardBuckets(rangeName, now)
		last := buckets[len(buckets)-1]
		if last.Success != 2 || last.Failed != 1 || last.Models["auto"] != 1 || last.Models["gpt-image-2"] != 2 {
			t.Fatalf("range=%s last=%+v", rangeName, last)
		}
	}
}

func TestDashboardBucketsCountPartialSuccessInSQL(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	defer store.Close()
	logs := &logService{store: store}
	logs.add("call", "部分成功", map[string]any{"status": "partial_success", "model": "gpt-image-2"})
	logs.add("call", "开始", map[string]any{"status": "started", "model": "gpt-image-2"})

	items, err := store.queryLogs(logQuery{status: "partial_success"}, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("partial success filter: items=%v err=%v", items, err)
	}
	buckets := logs.dashboardBuckets("24h", time.Now())
	last := buckets[len(buckets)-1]
	if last.Success != 1 || last.Failed != 0 || last.Models["gpt-image-2"] != 1 {
		t.Fatalf("partial success bucket=%+v", last)
	}
}

func TestDashboardRequiresAdmin(t *testing.T) {
	dir := t.TempDir()
	s := &Server{cfg: Config{AuthKey: "admin-key"}, store: NewStore(dir), logSvc: newLogService(dir)}
	request := func(key, rangeName string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/dashboard?range="+rangeName, nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		rec := httptest.NewRecorder()
		s.handleDashboard(rec, req)
		return rec
	}
	if rec := request("", "7d"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", rec.Code)
	}
	if rec := request("admin-key", "invalid"); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid range status=%d", rec.Code)
	}
	if rec := request("admin-key", "7d"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"buckets"`) {
		t.Fatalf("dashboard status=%d body=%s", rec.Code, rec.Body.String())
	}
	s.mux = http.NewServeMux()
	s.routes()
	req := httptest.NewRequest(http.MethodGet, "/api/dashboard/?range=7d", nil)
	req.Header.Set("Authorization", "Bearer admin-key")
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("trailing slash status=%d body=%s", rec.Code, rec.Body.String())
	}
}
