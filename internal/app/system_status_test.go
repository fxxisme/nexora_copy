package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestSystemStatusReportsImageTaskRunner(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	defer store.Close()
	runner := newImageTaskRunner(2, 3)
	s := &Server{store: store, cfg: Config{AuthKey: "admin-key"}, imageTasks: runner}
	req := httptest.NewRequest(http.MethodGet, "/api/system/status", nil)
	req.Header.Set("Authorization", "Bearer admin-key")
	response := httptest.NewRecorder()
	s.handleSystemStatus(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d %s", response.Code, response.Body.String())
	}
	var payload struct {
		Runner map[string]any `json:"image_task_runner"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Runner["workers"] != float64(2) || payload.Runner["capacity"] != float64(5) || payload.Runner["accepted"] != float64(0) || payload.Runner["started"] != false {
		t.Fatalf("runner status = %+v", payload.Runner)
	}
	runner.close()
}
