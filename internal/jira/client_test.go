package jira_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ag/pruner/internal/jira"
)

func setupJIRA(t *testing.T, statusCode int, key string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/rest/api/3/issue") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(statusCode)
		if statusCode < 300 {
			json.NewEncoder(w).Encode(map[string]string{"key": key})
		}
	}))
}

func setJIRAEnv(t *testing.T, url string) {
	t.Helper()
	os.Setenv("JIRA_URL", url)
	os.Setenv("JIRA_EMAIL", "bot@example.com")
	os.Setenv("JIRA_TOKEN", "test-token")
	os.Setenv("JIRA_PROJECT", "PLAT")
	t.Cleanup(func() {
		os.Unsetenv("JIRA_URL")
		os.Unsetenv("JIRA_EMAIL")
		os.Unsetenv("JIRA_TOKEN")
		os.Unsetenv("JIRA_PROJECT")
	})
}

func TestCreateTicket(t *testing.T) {
	srv := setupJIRA(t, http.StatusCreated, "PLAT-42")
	defer srv.Close()
	setJIRAEnv(t, srv.URL)

	client := jira.NewClient()
	key, err := client.CreateTicket("vendor-ns", "vendor-ns/app:v1", "AGE", "HIGH", 90)
	if err != nil {
		t.Fatalf("CreateTicket() error = %v", err)
	}
	if key != "PLAT-42" {
		t.Errorf("key = %q, want PLAT-42", key)
	}
}

func TestCreateTicket_ServerError(t *testing.T) {
	srv := setupJIRA(t, http.StatusInternalServerError, "")
	defer srv.Close()
	setJIRAEnv(t, srv.URL)

	client := jira.NewClient()
	_, err := client.CreateTicket("vendor-ns", "vendor-ns/app:v1", "AGE", "HIGH", 90)
	if err == nil {
		t.Error("expected error on server 500")
	}
}

func TestCreateTicket_InvalidURL(t *testing.T) {
	setJIRAEnv(t, "http://localhost:0")

	client := jira.NewClient()
	_, err := client.CreateTicket("vendor-ns", "app:v1", "AGE", "HIGH", 90)
	if err == nil {
		t.Error("expected connection error")
	}
}

func TestCreateTicket_RequestBody(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&captured)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"key": "PLAT-1"})
	}))
	defer srv.Close()
	setJIRAEnv(t, srv.URL)

	client := jira.NewClient()
	client.CreateTicket("vendor-ns", "vendor-ns/app:v1", "UNREFERENCED", "HIGH", 45)

	fields, ok := captured["fields"].(map[string]any)
	if !ok {
		t.Fatal("missing fields in request body")
	}
	summary, _ := fields["summary"].(string)
	if !strings.Contains(summary, "UNREFERENCED") {
		t.Errorf("summary missing rule type: %q", summary)
	}
	if !strings.Contains(summary, "vendor-ns") {
		t.Errorf("summary missing namespace: %q", summary)
	}
}
