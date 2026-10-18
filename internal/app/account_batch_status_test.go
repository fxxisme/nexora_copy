package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccountsUpdateBatchStatus(t *testing.T) {
	s := &Server{cfg: Config{AuthKey: "admin-key"}, store: NewStore(t.TempDir()), mux: http.NewServeMux()}
	s.routes()
	if err := s.store.SaveAccounts([]Account{
		{AccessToken: "first", Status: "正常"},
		{AccessToken: "second", Status: "异常"},
		{AccessToken: "third", Status: "正常"},
	}); err != nil {
		t.Fatal(err)
	}
	request := func(auth, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/accounts/update", strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, req)
		return rec
	}
	if rec := request("", `{"access_tokens":["first"],"status":"禁用"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", rec.Code)
	}
	if rec := request("admin-key", `{"access_tokens":["first"],"status":"异常"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid status=%d", rec.Code)
	}
	if rec := request("admin-key", `{"access_tokens":["first",1],"status":"禁用"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid token status=%d", rec.Code)
	}
	if rec := request("admin-key", `{"access_tokens":["first","second","missing"],"status":"禁用"}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"updated":2`) {
		t.Fatalf("batch status=%d body=%s", rec.Code, rec.Body.String())
	}
	accounts := s.store.LoadAccounts()
	if accounts[0].Status != "禁用" || accounts[1].Status != "禁用" || accounts[2].Status != "正常" {
		t.Fatalf("accounts=%+v", accounts)
	}
}
