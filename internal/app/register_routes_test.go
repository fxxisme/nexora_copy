package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegisterRoutesAreAvailableToAdmin(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"auth-key":"admin-key"}`), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(root)
	if err != nil {
		t.Fatal(err)
	}
	if s.register == nil || s.register.server != s {
		t.Fatal("register service was not initialized")
	}
	s.register.runFunc = func(ctx context.Context) { <-ctx.Done() }

	request := func(method, path, body string, authorized bool) (int, RegisterConfig) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if authorized {
			req.Header.Set("Authorization", "Bearer admin-key")
		}
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, req)
		var response struct {
			Register RegisterConfig `json:"register"`
		}
		if rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
		}
		return rec.Code, response.Register
	}

	if status, _ := request(http.MethodGet, "/api/register", "", false); status != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d, want 401", status)
	}
	if status, cfg := request(http.MethodGet, "/api/register", "", true); status != http.StatusOK || cfg.Total != 10 {
		t.Fatalf("initial register response: status=%d config=%+v", status, cfg)
	}
	if status, cfg := request(http.MethodPost, "/api/register", `{"total":1}`, true); status != http.StatusOK || cfg.Total != 1 {
		t.Fatalf("update register response: status=%d config=%+v", status, cfg)
	}
	if status, cfg := request(http.MethodPost, "/api/register", `{"mail":{"providers":[{"type":"icloud_api","enable":true}]}}`, true); status != http.StatusOK || len(cfg.Mail.Providers) != 1 {
		t.Fatalf("mail provider was not saved: status=%d providers=%+v", status, cfg.Mail.Providers)
	}
	if err := s.register.load(); err != nil {
		t.Fatal(err)
	}
	if len(s.register.snapshot().Mail.Providers) != 1 {
		t.Fatal("mail provider was not persisted")
	}
	if status, cfg := request(http.MethodPost, "/api/register/start", "", true); status != http.StatusOK || !cfg.Enabled {
		t.Fatalf("start register response: status=%d config=%+v", status, cfg)
	}
	if status, cfg := request(http.MethodPost, "/api/register/reset", "", true); status != http.StatusOK || cfg.Enabled {
		t.Fatalf("reset register response: status=%d config=%+v", status, cfg)
	}
}
