package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseJSONImageEditRequest(t *testing.T) {
	first := base64.StdEncoding.EncodeToString([]byte("first image"))
	second := base64.StdEncoding.EncodeToString([]byte("second image"))
	firstURL := "data:image/png;base64," + first
	body, _ := json.Marshal(map[string]any{
		"prompt": "edit both", "n": 2, "model": "gpt-image-2.5",
		"images": []any{firstURL, map[string]any{"b64_json": second, "mime_type": "image/png"}},
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	parsed, err := parseImageEditRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Prompt != "edit both" || parsed.N != 2 || parsed.Model != "gpt-image-2.5" || len(parsed.Inputs) != 2 || string(parsed.Inputs[0]) != "first image" || string(parsed.Inputs[1]) != "second image" {
		t.Fatalf("parsed JSON image edit: %+v", parsed)
	}
}

func TestParseJSONImageEditRequestRejectsInvalidInput(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("image"))
	tests := []struct {
		name string
		body string
		want string
	}{
		{"invalid JSON", `{`, "invalid JSON body"},
		{"multiple JSON values", `{"prompt":"edit","image":"` + encoded + `"}{}`, "invalid JSON body"},
		{"missing image", `{"prompt":"edit"}`, "image file or image_url is required"},
		{"invalid base64", `{"prompt":"edit","image":"%%%"}`, "invalid base64 image data"},
		{"invalid MIME", `{"prompt":"edit","image":{"b64_json":"` + encoded + `","mime_type":"text/html"}}`, "must point to an image"},
		{"private URL", `{"prompt":"edit","image_url":"http://127.0.0.1/image.png"}`, "not publicly reachable"},
		{"nonimage data URL", `{"prompt":"edit","image":"data:text/html;base64,` + encoded + `"}`, "must point to an image"},
		{"too many", `{"prompt":"edit","images":[` + strings.TrimSuffix(strings.Repeat(`"`+encoded+`",`, maxImageInputs+1), ",") + `]}`, "at most 16"},
		{"fractional count", `{"prompt":"edit","n":1.5,"image":"` + encoded + `"}`, "n must be an integer"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/images/edits", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			_, err := parseImageEditRequest(request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestParseMultipartImageEditRequest(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("prompt", "edit")
	_ = writer.WriteField("n", "2")
	file, err := writer.CreateFormFile("image[]", "input.png")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write([]byte("image data"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/images/edits", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	parsed, err := parseImageEditRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Prompt != "edit" || parsed.N != 2 || len(parsed.Inputs) != 1 || string(parsed.Inputs[0]) != "image data" {
		t.Fatalf("parsed multipart image edit: %+v", parsed)
	}
}

func TestImageURLValidationRejectsUnsafeDestinations(t *testing.T) {
	for _, raw := range []string{
		"http://localhost/image.png", "http://127.0.0.1/image.png",
		"http://169.254.169.254/latest/meta-data/", "http://100.64.0.1/image.png",
		"http://192.0.2.1/image.png", "http://[::1]/image.png",
		"http://user:pass@example.com/image.png", "file:///etc/passwd",
	} {
		if _, _, err := resolvePublicImageURL(context.Background(), raw); err == nil {
			t.Errorf("accepted unsafe image URL %q", raw)
		}
	}
	if _, _, err := resolvePublicImageURL(context.Background(), "https://1.1.1.1/image.png"); err != nil {
		t.Fatalf("rejected public image URL: %v", err)
	}
	if _, err := fetchPublicImageURL(context.Background(), "http://127.0.0.1/image.png"); err == nil {
		t.Fatal("private destination reached fetch")
	}
}
