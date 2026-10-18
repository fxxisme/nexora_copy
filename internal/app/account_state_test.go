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
}
