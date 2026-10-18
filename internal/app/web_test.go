package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestHandleWebServesExportedDirectoryPage(t *testing.T) {
	dist := t.TempDir()
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("root page"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dist, "login"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "login", "index.html"), []byte("login page"), 0644); err != nil {
		t.Fatal(err)
	}

	s := &Server{webDist: dist}
	for _, path := range []string{"/login", "/login/"} {
		recorder := httptest.NewRecorder()
		s.handleWeb(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK || recorder.Body.String() != "login page" {
			t.Errorf("GET %s: status=%d body=%q", path, recorder.Code, recorder.Body.String())
		}
	}
}
