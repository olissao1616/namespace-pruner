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

// setupJIRA creates a test server that handles both user search and issue creation.
func setupJIRA(t *testing.T, issueStatus int, key, accountID string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/user/search"):
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode([]map[string]any{
				{"accountId": accountID, "emailAddress": "owner@example.com", "active": true},
			})
		case strings.HasSuffix(r.URL.Path, "/rest/api/3/issue"):
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}
			w.WriteHeader(issueStatus)
			if issueStatus < 300 {
				json.NewEncoder(w).Encode(map[string]string{"key": key})
			}
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
	srv := setupJIRA(t, http.StatusCreated, "PLAT-42", "account-123")
	defer srv.Close()
	setJIRAEnv(t, srv.URL)

	client := jira.NewClient()
	key, err := client.CreateTicket("vendor-ns", "vendor-ns/app:v1", "AGE", "HIGH", "owner@example.com", 90)
	if err != nil {
		t.Fatalf("CreateTicket() error = %v", err)
	}
	if key != "PLAT-42" {
		t.Errorf("key = %q, want PLAT-42", key)
	}
}

func TestCreateTicket_AssignsOwner(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/user/search"):
			json.NewEncoder(w).Encode([]map[string]any{
				{"accountId": "acct-xyz", "emailAddress": "owner@example.com", "active": true},
			})
		case strings.HasSuffix(r.URL.Path, "/rest/api/3/issue"):
			json.NewDecoder(r.Body).Decode(&capturedBody)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"key": "PLAT-1"})
		}
	}))
	defer srv.Close()
	setJIRAEnv(t, srv.URL)

	client := jira.NewClient()
	client.CreateTicket("vendor-ns", "app:v1", "AGE", "HIGH", "owner@example.com", 90)

	fields, _ := capturedBody["fields"].(map[string]any)
	assignee, _ := fields["assignee"].(map[string]any)
	if assignee["accountId"] != "acct-xyz" {
		t.Errorf("assignee.accountId = %v, want acct-xyz", assignee["accountId"])
	}
}

func TestCreateTicket_NoAssigneeIfLookupFails(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/user/search"):
			// return empty — user not found
			json.NewEncoder(w).Encode([]map[string]any{})
		case strings.HasSuffix(r.URL.Path, "/rest/api/3/issue"):
			json.NewDecoder(r.Body).Decode(&capturedBody)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"key": "PLAT-1"})
		}
	}))
	defer srv.Close()
	setJIRAEnv(t, srv.URL)

	client := jira.NewClient()
	key, err := client.CreateTicket("vendor-ns", "app:v1", "AGE", "HIGH", "unknown@example.com", 90)
	if err != nil {
		t.Fatalf("should succeed even if assignee lookup fails: %v", err)
	}
	if key != "PLAT-1" {
		t.Errorf("key = %q, want PLAT-1", key)
	}
	fields, _ := capturedBody["fields"].(map[string]any)
	if _, hasAssignee := fields["assignee"]; hasAssignee {
		t.Error("assignee should be absent when lookup fails")
	}
}

func TestCreateTicket_AccountIDCached(t *testing.T) {
	lookupCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/user/search"):
			lookupCount++
			json.NewEncoder(w).Encode([]map[string]any{
				{"accountId": "acct-abc", "emailAddress": "owner@example.com", "active": true},
			})
		case strings.HasSuffix(r.URL.Path, "/rest/api/3/issue"):
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"key": "PLAT-1"})
		}
	}))
	defer srv.Close()
	setJIRAEnv(t, srv.URL)

	client := jira.NewClient()
	client.CreateTicket("ns", "app:v1", "AGE", "HIGH", "owner@example.com", 90)
	client.CreateTicket("ns", "app:v2", "AGE", "HIGH", "owner@example.com", 90)
	client.CreateTicket("ns", "app:v3", "AGE", "HIGH", "owner@example.com", 90)

	if lookupCount != 1 {
		t.Errorf("user lookup should be cached: called %d times, want 1", lookupCount)
	}
}

func TestCreateTicket_ServerError(t *testing.T) {
	srv := setupJIRA(t, http.StatusInternalServerError, "", "")
	defer srv.Close()
	setJIRAEnv(t, srv.URL)

	client := jira.NewClient()
	_, err := client.CreateTicket("vendor-ns", "vendor-ns/app:v1", "AGE", "HIGH", "owner@example.com", 90)
	if err == nil {
		t.Error("expected error on server 500")
	}
}

func TestCreateTicket_RequestBody(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/user/search"):
			json.NewEncoder(w).Encode([]map[string]any{})
		case strings.HasSuffix(r.URL.Path, "/rest/api/3/issue"):
			json.NewDecoder(r.Body).Decode(&captured)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"key": "PLAT-1"})
		}
	}))
	defer srv.Close()
	setJIRAEnv(t, srv.URL)

	client := jira.NewClient()
	client.CreateTicket("vendor-ns", "vendor-ns/app:v1", "UNREFERENCED", "HIGH", "owner@example.com", 45)

	fields, _ := captured["fields"].(map[string]any)
	summary, _ := fields["summary"].(string)
	if !strings.Contains(summary, "UNREFERENCED") {
		t.Errorf("summary missing rule type: %q", summary)
	}
	if !strings.Contains(summary, "vendor-ns") {
		t.Errorf("summary missing namespace: %q", summary)
	}
}
