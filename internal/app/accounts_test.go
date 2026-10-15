package app

import "testing"

func TestAccountFromRecordInfersCodexSource(t *testing.T) {
	account := accountFromRecord("access-token", "web", map[string]any{
		"type":        "codex",
		"export_type": "codex",
	})
	if account.Type != "free" || account.SourceType != "codex" {
		t.Fatalf("account type/source = %q/%q, want free/codex", account.Type, account.SourceType)
	}
}

func TestMergeImportedTokenPreservesExistingAccountState(t *testing.T) {
	current := Account{AccessToken: "access-token", Type: "plus", SourceType: "codex", Status: "异常", Quota: 7}
	record := map[string]any{"access_token": "access-token"}
	mergeImportedAccount(&current, accountFromRecord("access-token", "web", record), record, false)
	if current.Type != "plus" || current.SourceType != "codex" || current.Status != "异常" || current.Quota != 7 {
		t.Fatalf("token-only import changed existing state: %+v", current)
	}
}

func TestAccountFromRecordPreservesPythonExportFields(t *testing.T) {
	account := accountFromRecord("access-token", "import", map[string]any{
		"type":          "codex",
		"export_type":   "codex",
		"email":         "user@example.com",
		"password":      "secret",
		"refresh_token": "refresh-token",
		"id_token":      "id-token",
		"account_id":    "account-id",
		"created_at":    "2026-07-16T12:00:00Z",
		"fp": map[string]any{
			"user-agent": "ua",
		},
	})

	if account.ExportType != "codex" {
		t.Fatalf("export_type = %q, want codex", account.ExportType)
	}
	if account.Password == nil || *account.Password != "secret" {
		t.Fatalf("password not preserved: %#v", account.Password)
	}
	if account.CreatedAt == nil || *account.CreatedAt != "2026-07-16T12:00:00Z" {
		t.Fatalf("created_at not preserved: %#v", account.CreatedAt)
	}
	if account.RefreshToken == nil || *account.RefreshToken != "refresh-token" {
		t.Fatalf("refresh_token not preserved: %#v", account.RefreshToken)
	}
	if account.IDToken == nil || *account.IDToken != "id-token" {
		t.Fatalf("id_token not preserved: %#v", account.IDToken)
	}
	if account.AccountID == nil || *account.AccountID != "account-id" {
		t.Fatalf("account_id not preserved: %#v", account.AccountID)
	}
	if account.FP["user-agent"] != "ua" {
		t.Fatalf("fp not preserved: %#v", account.FP)
	}
}

func TestAccountFromRecordKeepsEmailPasswordSeparate(t *testing.T) {
	account := accountFromRecord("access-token", "import", map[string]any{
		"email":          "user@example.com",
		"email_password": "mail-secret",
	})

	if account.Password != nil {
		t.Fatalf("email_password should not become OpenAI password: %#v", account.Password)
	}
	if account.EmailPassword == nil || *account.EmailPassword != "mail-secret" {
		t.Fatalf("email_password not preserved: %#v", account.EmailPassword)
	}
	if account.Mailbox == nil || account.Mailbox["password"] != "mail-secret" {
		t.Fatalf("mailbox password not preserved: %#v", account.Mailbox)
	}
}

func TestMergeAccountClearsMistakenEmailPassword(t *testing.T) {
	oldPassword := "mail-secret"
	dst := Account{AccessToken: "access-token", Password: &oldPassword}
	src := accountFromRecord("access-token", "import", map[string]any{
		"email":          "user@example.com",
		"email_password": "mail-secret",
	})

	mergeAccount(&dst, src)

	if dst.Password != nil {
		t.Fatalf("mistaken OpenAI password should be cleared: %#v", dst.Password)
	}
	if dst.EmailPassword == nil || *dst.EmailPassword != "mail-secret" {
		t.Fatalf("email_password not merged: %#v", dst.EmailPassword)
	}
}

func TestAccountFromRecordKeepsMailboxOAuthSeparate(t *testing.T) {
	account := accountFromRecord("access-token", "import", map[string]any{
		"email":                 "user@outlook.com",
		"refresh_token":         "openai-refresh",
		"mailbox_refresh_token": "mail-refresh",
		"mailbox_client_id":     "mail-client",
		"mailbox_provider":      "outlook_token",
	})

	if account.RefreshToken == nil || *account.RefreshToken != "openai-refresh" {
		t.Fatalf("openai refresh_token not preserved: %#v", account.RefreshToken)
	}
	if account.Mailbox == nil {
		t.Fatal("mailbox not created")
	}
	if account.Mailbox["refresh_token"] != "mail-refresh" || account.Mailbox["client_id"] != "mail-client" {
		t.Fatalf("mailbox oauth fields not preserved: %#v", account.Mailbox)
	}
}

func TestAccountFromRecordPreservesMailboxLoginAndIMAPHost(t *testing.T) {
	account := accountFromRecord("access-token", "import", map[string]any{
		"email":          "alias@privaterelay.appleid.com",
		"email_password": "app-secret",
		"mailbox_email":  "real@icloud.com",
		"imap_host":      "imap.mail.me.com:993",
	})

	if account.Mailbox == nil {
		t.Fatal("mailbox not created")
	}
	if account.Mailbox["login_email"] != "real@icloud.com" {
		t.Fatalf("mailbox login_email not preserved: %#v", account.Mailbox)
	}
	if account.Mailbox["imap_host"] != "imap.mail.me.com:993" {
		t.Fatalf("imap_host not preserved: %#v", account.Mailbox)
	}
}
