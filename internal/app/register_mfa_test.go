package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type mfaRoundTripper func(*http.Request) (*http.Response, error)

func (fn mfaRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

func TestRegisterTOTP(t *testing.T) {
	code, err := registerTOTP("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", time.Unix(59, 0))
	if err != nil || code != "287082" {
		t.Fatalf("code = %q, err = %v", code, err)
	}
	if _, err := registerTOTP("invalid!", time.Now()); err == nil {
		t.Fatal("invalid secret accepted")
	}
}

func TestImportRegisterAccountConcurrent(t *testing.T) {
	store := NewStore(t.TempDir())
	svc := &registerService{server: &Server{store: store}}
	var group sync.WaitGroup
	errors := make(chan error, 20)
	for i := 0; i < 20; i++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			errors <- svc.importRegisterAccount(map[string]any{
				"access_token": fmt.Sprintf("token-%d", index),
				"email":        fmt.Sprintf("user-%d@example.com", index),
			})
		}(i)
	}
	group.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := len(store.LoadAccounts()); got != 20 {
		t.Fatalf("saved accounts = %d, want 20", got)
	}
}

func TestAccountMFACodeRequiresAdminAndEnabledStatus(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"auth-key":"admin-key"}`), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(root)
	if err != nil {
		t.Fatal(err)
	}
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	if err := s.store.SaveAccounts([]Account{{AccessToken: "token", Status: "正常", MFAStatus: "enabled", MFASecret: &secret}}); err != nil {
		t.Fatal(err)
	}
	request := func(admin bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/accounts/mfa/code", strings.NewReader(`{"access_token":"token"}`))
		if admin {
			req.Header.Set("Authorization", "Bearer admin-key")
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}
	if rec := request(false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d", rec.Code)
	}
	rec := request(true)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin status = %d: %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || len(result.Code) != 6 {
		t.Fatalf("code response invalid: %v", err)
	}
	if err := s.store.updateAccountMFA("token", "pending", secret); err != nil {
		t.Fatal(err)
	}
	if rec := request(true); rec.Code != http.StatusConflict {
		t.Fatalf("pending status = %d", rec.Code)
	}
}

func TestRegisterMFAActivationAndPersistence(t *testing.T) {
	for _, tc := range []struct {
		name              string
		activation        string
		wantStatus        string
		wantAccountStatus string
		wantError         bool
	}{
		{"enabled", `{"success":true}`, "enabled", "正常", false},
		{"not confirmed", `{"success":false}`, "pending", "异常", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			store := NewStore(root)
			if err := store.SaveAccounts([]Account{{AccessToken: "test-token", Status: "异常", MFAStatus: "pending"}}); err != nil {
				t.Fatal(err)
			}
			calls := 0
			session := &registerSession{client: &http.Client{Transport: mfaRoundTripper(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Header.Get("Authorization") != "Bearer test-token" {
					t.Errorf("authorization header missing")
				}
				var body map[string]string
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				response := `{"secret":"GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ","session_id":"session"}`
				if strings.HasSuffix(req.URL.Path, "/enroll") {
					if body["factor_type"] != "totp" || body["source"] != "settings" {
						t.Errorf("enroll body = %v", body)
					}
				} else if strings.HasSuffix(req.URL.Path, "/activate_enrollment") {
					if len(body["code"]) != 6 || body["factor_type"] != "totp" || body["session_id"] != "session" {
						t.Errorf("activation body = %v", body)
					}
					response = tc.activation
				} else {
					t.Errorf("unexpected path %s", req.URL.Path)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(response)), Header: http.Header{}}, nil
			})}}
			svc := &registerService{server: &Server{store: store}}
			err := svc.enableRegisterMFA(context.Background(), session, "test-token", "device-id")
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v", err)
			}
			if calls != 2 {
				t.Fatalf("calls = %d", calls)
			}
			account := store.LoadAccounts()[0]
			if account.MFAStatus != tc.wantStatus || account.Status != tc.wantAccountStatus || account.MFASecret == nil || *account.MFASecret != "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" {
				t.Fatalf("MFA/account status or secret not saved: mfa_status=%q account_status=%q secret_present=%t", account.MFAStatus, account.Status, account.MFASecret != nil)
			}
			info, err := os.Stat(filepath.Join(root, "chatgpt2api.db"))
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0600 {
				t.Fatalf("chatgpt2api.db mode = %v", info.Mode().Perm())
			}
			store.Close()
			reopened := NewStore(root)
			defer reopened.Close()
			if got := reopened.LoadAccounts(); len(got) != 1 || got[0].MFAStatus != tc.wantStatus {
				t.Fatalf("MFA status after reopen = %+v", got)
			}
		})
	}
}
