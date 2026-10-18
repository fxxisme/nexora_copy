package app

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestImageTaskSubmissionIsScopedToOwner(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	if err := store.SaveAuthKeys([]UserKey{
		{ID: "owner-a", Role: "user", Enabled: true, KeyHash: hashKey("key-a"), ImageDailyQuota: 3, ImageMonthlyQuota: 3, ImageTotalQuota: 3},
		{ID: "owner-b", Role: "user", Enabled: true, KeyHash: hashKey("key-b"), ImageDailyQuota: 3, ImageMonthlyQuota: 3, ImageTotalQuota: 3},
	}); err != nil {
		t.Fatal(err)
	}
	first := ImageTask{ID: "shared", OwnerID: "owner-a", Status: "running", Mode: "generate", Model: "gpt-image-2", CreatedAt: nowISO(), UpdatedAt: nowISO()}
	if err := store.SaveTasks([]ImageTask{first}); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store, cfg: Config{AuthKey: "admin-key", SensitiveWords: []string{"blocked"}}, logSvc: &logService{store: store}, taskCancels: map[imageTaskKey]context.CancelFunc{}}
	request := func(token, path, body string, handler http.HandlerFunc) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		handler(response, req)
		if response.Code != http.StatusOK {
			t.Fatalf("%s response = %d %s", path, response.Code, response.Body.String())
		}
		return response
	}
	duplicate := request("key-a", "/api/image-tasks/generations", `{"client_task_id":"shared","prompt":"different"}`, s.handleImageTaskGeneration)
	var returned ImageTask
	if err := json.Unmarshal(duplicate.Body.Bytes(), &returned); err != nil {
		t.Fatal(err)
	}
	if returned.OwnerID != "owner-a" || returned.Status != "running" || returned.CreatedAt != first.CreatedAt {
		t.Fatalf("duplicate submission changed existing task: %+v", returned)
	}
	second := request("key-b", "/api/image-tasks/generations", `{"client_task_id":"shared","prompt":"blocked"}`, s.handleImageTaskGeneration)
	if err := json.Unmarshal(second.Body.Bytes(), &returned); err != nil {
		t.Fatal(err)
	}
	if returned.OwnerID != "owner-b" || returned.Status != "error" {
		t.Fatalf("second owner's task = %+v", returned)
	}
	if tasks := store.LoadTasks(); len(tasks) != 2 {
		t.Fatalf("task count = %d, want 2", len(tasks))
	}
	for _, key := range store.LoadAuthKeys() {
		if key.ImageTotalUsed != 0 {
			t.Fatalf("duplicate or rejected request charged quota: %+v", key)
		}
	}
	for _, owner := range []struct{ token, id string }{{"key-a", "owner-a"}, {"key-b", "owner-b"}} {
		req := httptest.NewRequest(http.MethodGet, "/api/image-tasks?ids=shared", nil)
		req.Header.Set("Authorization", "Bearer "+owner.token)
		response := httptest.NewRecorder()
		s.handleImageTasks(response, req)
		var page struct {
			Items []ImageTask `json:"items"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 || page.Items[0].OwnerID != owner.id {
			t.Fatalf("%s task list = %+v", owner.id, page.Items)
		}
	}
	request("key-a", "/api/image-tasks/cancel", `{"id":"shared"}`, s.handleImageTaskCancel)
	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	if err := writer.WriteField("client_task_id", "shared"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	editRequest := httptest.NewRequest(http.MethodPost, "/api/image-tasks/edits", &form)
	editRequest.Header.Set("Authorization", "Bearer key-a")
	editRequest.Header.Set("Content-Type", writer.FormDataContentType())
	editResponse := httptest.NewRecorder()
	s.handleImageTaskEdit(editResponse, editRequest)
	if editResponse.Code != http.StatusOK || json.Unmarshal(editResponse.Body.Bytes(), &returned) != nil || returned.Mode != "generate" || returned.Status != "canceled" {
		t.Fatalf("cross-mode duplicate = %d %s", editResponse.Code, editResponse.Body.String())
	}
	for _, task := range store.LoadTasks() {
		if task.OwnerID == "owner-a" && task.Status != "canceled" || task.OwnerID == "owner-b" && task.Status != "error" {
			t.Fatalf("cancel affected another owner: %+v", task)
		}
	}
}

func TestImageTaskConcurrentInsertKeepsOneTaskPerOwner(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	s := &Server{store: store}
	var created atomic.Int32
	var workers sync.WaitGroup
	for range 24 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, isNew, err := s.insertTask(ImageTask{ID: "same", OwnerID: "owner-a", Status: "queued"})
			if err != nil {
				t.Errorf("insert task: %v", err)
			} else if isNew {
				created.Add(1)
			}
		}()
	}
	workers.Wait()
	if created.Load() != 1 {
		t.Fatalf("created %d tasks for one owner", created.Load())
	}
	if _, isNew, err := s.insertTask(ImageTask{ID: "same", OwnerID: "owner-b", Status: "queued"}); err != nil || !isNew {
		t.Fatalf("second owner insert = %t, %v", isNew, err)
	}
	if updated, err := s.updateTaskStatus("owner-a", "same", "success", "", nil); err != nil || !updated {
		t.Fatalf("owner-a task update = %t, %v", updated, err)
	}
	tasks := store.LoadTasks()
	if len(tasks) != 2 {
		t.Fatalf("task count = %d, want 2", len(tasks))
	}
	for _, task := range tasks {
		if task.OwnerID == "owner-a" && task.Status != "success" || task.OwnerID == "owner-b" && task.Status != "queued" {
			t.Fatalf("task state crossed owners: %+v", task)
		}
	}
}

func TestImageEditTaskRejectsMalformedMultipart(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	s := &Server{store: store, cfg: Config{AuthKey: "admin-key"}}
	req := httptest.NewRequest(http.MethodPost, "/api/image-tasks/edits", bytes.NewBufferString("bad form"))
	req.Header.Set("Authorization", "Bearer admin-key")
	req.Header.Set("Content-Type", "multipart/form-data")
	response := httptest.NewRecorder()
	s.handleImageTaskEdit(response, req)
	if response.Code != http.StatusBadRequest || len(store.LoadTasks()) != 0 {
		t.Fatalf("malformed multipart response = %d %s", response.Code, response.Body.String())
	}
}
