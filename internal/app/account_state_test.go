package app

import (
	"errors"
	"testing"
)

func TestInvalidRefreshTokenState(t *testing.T) {
	account := Account{Status: "正常", Quota: 3}
	err := errors.New(`GET /backend-api/me failed: status=401 body={"error":{"code":"unauthorized_unknown"}}`)
	if !isInvalidTokenErrorText(err) {
		t.Fatal("401 account refresh must be classified as an invalid token")
	}
	applyAccountFailureState(&account, err, "2026-10-15T00:00:00Z")
	if account.Status != "异常" || account.Quota != 0 || account.Fail != 1 {
		t.Fatalf("invalid token state: %+v", account)
	}

	if isInvalidTokenErrorText(errors.New("proxyconnect tcp: connection refused")) {
		t.Fatal("proxy failure must not invalidate an account")
	}

	oauthErr := &oauthRefreshError{StatusCode: 400, Code: "invalid_grant", Terminal: true}
	if !isInvalidTokenErrorText(oauthErr) {
		t.Fatal("terminal oauth refresh error must be classified as an invalid token")
	}

	account2 := Account{Status: "正常", Quota: 5}
	applyAccountFailureState(&account2, oauthErr, "2026-10-15T00:00:00Z")
	if account2.Status != "异常" || account2.Quota != 0 || account2.Fail != 1 {
		t.Fatalf("oauth terminal failure state: %+v", account2)
	}

	disabledAccount := Account{Status: "禁用", Quota: 5}
	applyAccountFailureState(&disabledAccount, oauthErr, "2026-10-15T00:00:00Z")
	if disabledAccount.Status != "禁用" || disabledAccount.Quota != 0 || disabledAccount.Fail != 1 {
		t.Fatalf("disabled account should stay disabled: %+v", disabledAccount)
	}

	rateLimitErr := errors.New("POST /backend-api/conversation failed: status=429 body=too many requests")
	account3 := Account{Status: "正常", Quota: 5}
	applyAccountFailureState(&account3, rateLimitErr, "2026-10-15T00:00:00Z")
	if account3.Status != "限流" || account3.RestoreAt == nil {
		t.Fatalf("rate limit failure state: %+v", account3)
	}
}
