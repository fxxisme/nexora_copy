package app

import "testing"

func TestPersistReLoginAccountPreservesConcurrentAccountChanges(t *testing.T) {
	store := NewStore(t.TempDir())
	defer store.Close()
	if err := store.SaveAccounts([]Account{{AccessToken: "old-token", Quota: 1, Status: "异常"}}); err != nil {
		t.Fatal(err)
	}
	next := store.LoadAccounts()[0]
	next.AccessToken = "new-token"
	next.Status = "正常"
	if err := store.UpdateAccounts(func(accounts []Account) []Account {
		accounts[0].Quota = 9
		return append(accounts, Account{AccessToken: "new-account"})
	}); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store}
	if err := s.persistReLoginAccount("old-token", next); err != nil {
		t.Fatal(err)
	}
	accounts := store.LoadAccounts()
	if len(accounts) != 2 || accounts[0].AccessToken != "new-token" || accounts[0].Status != "正常" || accounts[0].Quota != 9 || accounts[1].AccessToken != "new-account" {
		t.Fatalf("concurrent account changes were lost: %+v", accounts)
	}
	if err := s.persistReLoginAccount("old-token", next); err == nil {
		t.Fatal("stale re-login result was accepted")
	}
}
