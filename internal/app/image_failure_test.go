package app

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	fhttp "github.com/bogdanfinn/fhttp"
)

func TestSignedImageDownloadFailureDoesNotInvalidateAccount(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	if err := store.SaveAccounts([]Account{{AccessToken: "token", Status: "正常", Quota: 3}}); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store}
	downloadErr := &imageDownloadStatusError{StatusCode: http.StatusUnauthorized, Body: "expired signed URL"}
	signedErr := imageDownloadFailure("https://asset.example.com/image.png?signature=secret", downloadErr)
	if !isImageDeliveryFailure(signedErr) || !shouldRetryImageAccount(signedErr) {
		t.Fatalf("signed URL failure must allow another account without penalizing this one: %v", signedErr)
	}
	s.markAccountFailure("token", signedErr, true, "")
	account := store.LoadAccounts()[0]
	if account.Fail != 0 || account.Status != "正常" || account.Quota != 3 {
		t.Fatalf("signed URL failure changed account health: %+v", account)
	}
	if strings.Contains(signedErr.Error(), "secret") {
		t.Fatal("signed URL appeared in public error")
	}

	accountErr := imageDownloadFailure("https://chatgpt.com/backend-api/files/id/download", &upstreamStatusError{StatusCode: http.StatusUnauthorized})
	if isImageDeliveryFailure(accountErr) {
		t.Fatal("same-origin authorization failure classified as asset delivery failure")
	}
	s.markAccountFailure("token", accountErr, true, "")
	account = store.LoadAccounts()[0]
	if account.Fail != 1 || account.Status != "异常" || account.Quota != 0 {
		t.Fatalf("same-origin 401 did not invalidate account: %+v", account)
	}
}

func TestCanceledImageRequestDoesNotPenalizeOrRetry(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	if err := store.SaveAccounts([]Account{{AccessToken: "token", Status: "正常", Quota: 3}}); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store}
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, imageDownloadFailure("https://asset.example.com/image.png", context.Canceled)} {
		s.markAccountFailure("token", err, true, "")
		if shouldRetryImageAccount(err) {
			t.Fatalf("canceled request retried: %v", err)
		}
	}
	account := store.LoadAccounts()[0]
	if account.Fail != 0 || account.Status != "正常" || account.Quota != 3 {
		t.Fatalf("canceled request changed account health: %+v", account)
	}
}

func TestNoImageGeneratedRetriesWithoutAccountPenalty(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	if err := store.SaveAccounts([]Account{{AccessToken: "token", Status: "正常", Quota: 3}}); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store}
	if !shouldRetryImageAccount(errNoImageGenerated) {
		t.Fatal("empty image result should try another account")
	}
	s.markAccountFailure("token", errNoImageGenerated, true, "")
	account := store.LoadAccounts()[0]
	if account.Fail != 0 || account.Status != "正常" || account.Quota != 3 {
		t.Fatalf("empty result changed account health: %+v", account)
	}
}

func TestImageTextReplyDoesNotPenalizeAccountButQuotaReplyDoes(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	if err := store.SaveAccounts([]Account{{AccessToken: "token", Status: "正常", Quota: 3}}); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store}
	client := &UpstreamClient{}
	_, _, textErr := client.imageIDsAfterSSE(context.Background(), "", "I can help with a different image prompt", &upstreamConversationState{Blocked: true}, nil, nil, imageGenerationOptions{})
	if !isImageTextReplyFailure(textErr) || shouldRetryImageAccount(textErr) {
		t.Fatalf("text reply classification = %v", textErr)
	}
	s.markAccountFailure("token", textErr, true, "")
	account := store.LoadAccounts()[0]
	if account.Fail != 0 || account.Status != "正常" {
		t.Fatalf("text reply changed account health: %+v", account)
	}
	quotaErr := imageTextReplyFailure("You've hit the free plan limit for image generation")
	if isImageTextReplyFailure(quotaErr) || !isRateLimitErrorText(quotaErr) || !shouldRetryImageAccount(quotaErr) {
		t.Fatalf("quota reply classification = %v", quotaErr)
	}
	s.markAccountFailure("token", quotaErr, true, "")
	account = store.LoadAccounts()[0]
	if account.Fail != 1 || account.Status != "限流" {
		t.Fatalf("quota reply did not limit account: %+v", account)
	}
}

func TestImageDownloadTransportErrorRedactsSignedURL(t *testing.T) {
	const signedURL = "https://asset.example.com/image.png?signature=secret-value"
	client := &UpstreamClient{client: upstreamDoerFunc(func(*fhttp.Request) (*fhttp.Response, error) {
		return nil, &url.Error{Op: "Get", URL: signedURL, Err: errors.New("connection reset")}
	})}
	_, err := client.downloadOnce(context.Background(), signedURL)
	if err == nil || strings.Contains(err.Error(), "secret-value") || !strings.Contains(err.Error(), "connection error") {
		t.Fatalf("transport error = %v", err)
	}
}
