package app

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestAccountGroupProxyPrecedenceAndDeletion(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	proxyGroup, err := store.SaveProxyGroup(proxyGroupInput{
		Name: "egress", Nodes: []proxyNodeInput{{URL: "http://127.0.0.1:8211"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveProxyDefaults(proxyReference{Mode: "custom", URL: "http://127.0.0.1:8212"}, nil); err != nil {
		t.Fatal(err)
	}
	group, err := store.SaveAccountGroup(accountGroupInput{Name: "free", Proxy: "group:" + proxyGroup.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAccounts([]Account{{AccessToken: "token", GroupID: group.ID, Status: "正常"}}); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store}
	account := store.LoadAccounts()[0]
	route, err := s.selectedProxyRouteForAccount(account)
	if err != nil || route.URL != proxyGroup.Nodes[0].URL || !route.Bound {
		t.Fatalf("account group route = %+v, %v", route, err)
	}
	if err := store.DeleteProxyGroup(proxyGroup.ID); err == nil {
		t.Fatal("deleted a proxy group referenced by an account group")
	}
	if err := store.DeleteAccountGroup(group.ID); err == nil {
		t.Fatal("deleted a group with assigned accounts")
	}
	account.Proxy = "direct"
	route, err = s.selectedProxyRouteForAccount(account)
	if err != nil || route.URL != "" || !route.Bound {
		t.Fatalf("account override route = %+v, %v", route, err)
	}
	disabled := false
	if _, err := store.SaveAccountGroup(accountGroupInput{ID: group.ID, Name: group.Name, Proxy: group.Proxy, Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	account.Proxy = ""
	route, err = s.selectedProxyRouteForAccount(account)
	if err != nil || route.URL != "http://127.0.0.1:8212" || route.Bound {
		t.Fatalf("disabled group should inherit global route: %+v, %v", route, err)
	}
	if err := store.SaveAccounts([]Account{{AccessToken: "token", Status: "正常"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAccountGroup(group.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteProxyGroup(proxyGroup.ID); err != nil {
		t.Fatal(err)
	}
}

func TestAccountGroupAssignmentAPIAndImport(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	s := &Server{store: store, cfg: Config{AuthKey: "test-key"}}
	post := func(path, body string, handler http.HandlerFunc) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer test-key")
		recorder := httptest.NewRecorder()
		handler(recorder, req)
		return recorder
	}
	response := post("/api/account-groups", `{"name":"free","proxy":"direct"}`, s.handleAccountGroups)
	if response.Code != http.StatusOK {
		t.Fatalf("create group: %d %s", response.Code, response.Body.String())
	}
	groups, err := store.LoadAccountGroups()
	if err != nil || len(groups) != 1 || groups[0].Proxy != "direct" {
		t.Fatalf("groups = %+v, %v", groups, err)
	}
	if err := store.SaveAccounts([]Account{{AccessToken: "token", Status: "正常"}}); err != nil {
		t.Fatal(err)
	}
	response = post("/api/accounts/update", `{"access_token":"token","group_id":"missing"}`, s.handleAccountsUpdate)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("missing group accepted: %d", response.Code)
	}
	response = post("/api/accounts/update", `{"access_token":"token","group_id":"`+groups[0].ID+`"}`, s.handleAccountsUpdate)
	if response.Code != http.StatusOK || store.LoadAccounts()[0].GroupID != groups[0].ID {
		t.Fatalf("assign account group: %d %s", response.Code, response.Body.String())
	}
	account := store.LoadAccounts()[0]
	mergeImportedAccount(&account, accountFromRecord("token", "web", map[string]any{"access_token": "token"}), map[string]any{"access_token": "token"}, false)
	if account.GroupID != groups[0].ID {
		t.Fatalf("token-only import removed account group: %q", account.GroupID)
	}
	response = post("/api/accounts/update", `{"access_token":"token","group_id":""}`, s.handleAccountsUpdate)
	if response.Code != http.StatusOK || store.LoadAccounts()[0].GroupID != "" {
		t.Fatalf("clear group: %d %s", response.Code, response.Body.String())
	}
	response = post("/api/accounts/update", `{"access_tokens":["token"],"group_id":"`+groups[0].ID+`"}`, s.handleAccountsUpdate)
	if response.Code != http.StatusOK || store.LoadAccounts()[0].GroupID != groups[0].ID || store.LoadAccounts()[0].Status != "正常" {
		t.Fatalf("batch assign group: %d %s", response.Code, response.Body.String())
	}
	response = post("/api/accounts/update", `{"access_tokens":["token"],"group_id":""}`, s.handleAccountsUpdate)
	if response.Code != http.StatusOK || store.LoadAccounts()[0].GroupID != "" {
		t.Fatalf("batch clear group: %d %s", response.Code, response.Body.String())
	}
}

func TestAccountGroupStorageRejectsMissingAssignment(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	if err := store.SaveAccounts([]Account{{AccessToken: "token", Status: "正常"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateAccounts(func(accounts []Account) []Account {
		accounts[0].GroupID = "missing"
		return accounts
	}); err == nil || !strings.Contains(err.Error(), "账号组不存在") {
		t.Fatalf("missing group assignment error = %v", err)
	}
	if got := store.LoadAccounts()[0].GroupID; got != "" {
		t.Fatalf("invalid group assignment persisted: %q", got)
	}
}

func TestDeleteAccountGroupPreservesGroupOnAccountReadError(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	group, err := store.SaveAccountGroup(accountGroupInput{Name: "protected"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("INSERT INTO app_accounts (token_hash, ordinal, payload) VALUES ("+store.bind(1)+", 0, "+store.bind(2)+")", "corrupt", "{"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAccountGroup(group.ID); err == nil {
		t.Fatal("deleted account group after account read failed")
	}
	groups, err := store.LoadAccountGroups()
	if err != nil || len(groups) != 1 || groups[0].ID != group.ID {
		t.Fatalf("account group changed after read failure: %+v, %v", groups, err)
	}
}
