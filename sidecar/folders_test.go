package sidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMacFolderBrowserIsAuthenticatedReadOnlyAndConfinedForLabs(t *testing.T) {
	rootDirectory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Project with spaces", "z-last", ".private"} {
		if err := os.Mkdir(filepath.Join(rootDirectory, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(rootDirectory, "secret.txt"), []byte("must not be returned"), 0600); err != nil {
		t.Fatal(err)
	}
	outsideDirectory := t.TempDir()
	if err := os.Symlink(outsideDirectory, filepath.Join(rootDirectory, "escape")); err != nil {
		t.Fatal(err)
	}
	handler, runner, service := setup(t, false, true)
	defer handler.Close()
	service.config.HerdrLabRoot = rootDirectory
	if response := request(handler, "GET", "/v1/folders", "", ""); response.Code != 401 {
		t.Fatal(response.Code)
	}
	service.config.AllowControl = false
	if response := request(handler, "GET", "/v1/folders", "secret", ""); response.Code != 403 {
		t.Fatal(response.Code)
	}
	service.config.AllowControl = true
	response := request(handler, "GET", "/v1/folders", "secret", "")
	var listing macFolderListing
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &listing) != nil {
		t.Fatal(response.Code, response.Body)
	}
	if listing.Path != rootDirectory || listing.ParentPath != "" || len(listing.Folders) != 2 || listing.Folders[0].Name != "Project with spaces" {
		t.Fatal(listing)
	}
	if strings.Contains(response.Body.String(), "secret") || strings.Contains(response.Body.String(), "escape") {
		t.Fatal("returned a file or escaping symlink")
	}
	nested := filepath.Join(rootDirectory, "Project with spaces")
	response = request(handler, "GET", "/v1/folders?path="+url.QueryEscape(nested), "secret", "")
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &listing) != nil || listing.ParentPath != rootDirectory || listing.Path != nested {
		t.Fatal(response.Code, response.Body)
	}
	for _, directory := range []string{outsideDirectory, filepath.Join(rootDirectory, "escape"), filepath.Join(rootDirectory, "secret.txt"), rootDirectory + "/../", "relative", "/does-not-exist-trill-test"} {
		response := request(handler, "GET", "/v1/folders?path="+url.QueryEscape(directory), "secret", "")
		if response.Code != 400 {
			t.Fatal(directory, response.Code, response.Body)
		}
	}
	for _, query := range []string{"path=%zz", "path=/&path=/tmp", "root=/", "path=" + url.QueryEscape(rootDirectory) + "&unexpected=1"} {
		if response := request(handler, "GET", "/v1/folders?"+query, "secret", ""); response.Code != 400 {
			t.Fatal(query, response.Code)
		}
	}
	if len(runner.calls) != 0 || len(service.leases) != 0 {
		t.Fatal("folder browsing ran a command or attached a terminal")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest("GET", "/v1/folders", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer secret")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != 503 {
		t.Fatal("cancelled browser", recorder.Code, recorder.Body)
	}
}

func TestDemoFolderBrowserNeverExposesHostDirectories(t *testing.T) {
	service, demo, _ := demoForTest(t, "herdr", true)
	handler := NewHTTP(service, "secret")
	defer handler.Close()
	response := request(handler, "GET", "/v1/folders", "secret", "")
	var listing macFolderListing
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &listing) != nil || !samePath(listing.Path, filepath.Join(demo.rootPath, "fixtures")) || listing.ParentPath != "" {
		t.Fatal(response.Code, response.Body)
	}
	for _, directory := range []string{"/", demo.rootPath, t.TempDir()} {
		if response := request(handler, "GET", "/v1/folders?path="+url.QueryEscape(directory), "secret", ""); response.Code != 400 {
			t.Fatal(response.Code, response.Body)
		}
	}
}

func TestMacFolderCreationInputValidationDoesNotInvokeHerdr(t *testing.T) {
	handler, runner, _ := setup(t, false, true)
	defer handler.Close()
	for _, fields := range []string{`"kind":"terminal","directory":null`, `"kind":"terminal","directory":"relative"`, `"kind":"terminal","directory":""`, `"kind":"worktree","branch":"feature/test","useWorktree":true,"directory":"/tmp"`} {
		response := request(handler, "POST", "/v1/sessions", "secret", fmt.Sprintf(`{"projectId":"p1",%s}`, fields))
		if response.Code != 400 {
			t.Fatal(fields, response.Code, response.Body)
		}
	}
	if len(runner.calls) != 0 {
		t.Fatal("invalid create ran a command")
	}
}

func TestDemoSelectedFolderAndCloseRequireControlAndStayOwned(t *testing.T) {
	service, demo, _ := demoForTest(t, "herdr", true)
	handler := NewHTTP(service, "secret")
	defer handler.Close()
	folder, err := filepath.EvalSymlinks(filepath.Join(demo.rootPath, "fixtures", "demo-herdr"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"projectId": "demo-herdr-empty", "kind": "terminal", "directory": folder})
	response := request(handler, "POST", "/v1/sessions", "secret", string(body))
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body)
	}
	created, err := service.find(context.Background(), "demo-created-1")
	if err != nil || !samePath(created.WorktreePath, folder) {
		t.Fatal(created, err)
	}
	for _, token := range []string{"", "wrong"} {
		if response := request(handler, "DELETE", "/v1/sessions/demo-created-1", token, ""); response.Code != 401 {
			t.Fatal(response.Code)
		}
	}
	service.config.AllowControl = false
	if response := request(handler, "DELETE", "/v1/sessions/demo-created-1", "secret", ""); response.Code != 403 {
		t.Fatal(response.Code)
	}
	service.config.AllowControl = true
	response = request(handler, "DELETE", "/v1/sessions/demo-created-1", "secret", "")
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body)
	}
	if _, err := service.find(context.Background(), "demo-created-1"); err != errNotFound {
		t.Fatal(err)
	}
	if _, err := service.find(context.Background(), "demo-herdr-1"); err != nil {
		t.Fatal("closed another terminal", err)
	}
	if response := request(handler, "DELETE", "/v1/sessions/demo-created-1", "secret", ""); response.Code != 404 {
		t.Fatal(response.Code, response.Body)
	}
}
