package notify_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/ag/pruner/internal/notify"
)

func setupCHES(t *testing.T, tokenStatus, emailStatus int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/token"):
			w.WriteHeader(tokenStatus)
			if tokenStatus == http.StatusOK {
				json.NewEncoder(w).Encode(map[string]string{"access_token": "test-token"})
			}
		case strings.HasSuffix(r.URL.Path, "/email"):
			if auth := r.Header.Get("Authorization"); !strings.HasPrefix(auth, "Bearer ") {
				t.Errorf("missing Bearer token, got: %q", auth)
			}
			w.WriteHeader(emailStatus)
			if emailStatus == http.StatusCreated {
				json.NewEncoder(w).Encode(map[string]any{"txId": "abc-123"})
			}
		}
	}))
}

func setCHESEnv(t *testing.T, baseURL, tokenURL string) {
	t.Helper()
	// Override the constants via env (we'll refactor to support this in test)
	os.Setenv("CHES_CLIENT_ID", "test-client")
	os.Setenv("CHES_CLIENT_SECRET", "test-secret")
	os.Setenv("CHES_FROM", "pruner@example.com")
	os.Setenv("CHES_BASE_URL", baseURL)
	os.Setenv("CHES_TOKEN_URL", tokenURL)
	t.Cleanup(func() {
		os.Unsetenv("CHES_CLIENT_ID")
		os.Unsetenv("CHES_CLIENT_SECRET")
		os.Unsetenv("CHES_FROM")
		os.Unsetenv("CHES_BASE_URL")
		os.Unsetenv("CHES_TOKEN_URL")
	})
}

func TestSend_Success(t *testing.T) {
	srv := setupCHES(t, http.StatusOK, http.StatusCreated)
	defer srv.Close()
	setCHESEnv(t, srv.URL, srv.URL+"/token")

	client := notify.NewClient()
	err := client.Send("owner@example.com", "Test Subject", "<p>Hello</p>")
	if err != nil {
		t.Errorf("Send() unexpected error: %v", err)
	}
}

func TestSend_TokenFailure(t *testing.T) {
	srv := setupCHES(t, http.StatusUnauthorized, http.StatusCreated)
	defer srv.Close()
	setCHESEnv(t, srv.URL, srv.URL+"/token")

	client := notify.NewClient()
	err := client.Send("owner@example.com", "Test", "<p>Hello</p>")
	if err == nil {
		t.Error("expected error on token failure")
	}
}

func TestSend_EmailAPIFailure(t *testing.T) {
	srv := setupCHES(t, http.StatusOK, http.StatusBadRequest)
	defer srv.Close()
	setCHESEnv(t, srv.URL, srv.URL+"/token")

	client := notify.NewClient()
	err := client.Send("owner@example.com", "Test", "<p>Hello</p>")
	if err == nil {
		t.Error("expected error on email API 400")
	}
}

func TestSend_RequestPayload(t *testing.T) {
	var capturedEmail map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/token"):
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"access_token": "tok"})
		case strings.HasSuffix(r.URL.Path, "/email"):
			json.NewDecoder(r.Body).Decode(&capturedEmail)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"txId": "x"})
		}
	}))
	defer srv.Close()
	setCHESEnv(t, srv.URL, srv.URL+"/token")

	client := notify.NewClient()
	client.Send("owner@example.com", "My Subject", "<p>body</p>")

	to, _ := capturedEmail["to"].([]any)
	if len(to) == 0 || to[0] != "owner@example.com" {
		t.Errorf("unexpected to field: %v", to)
	}
	if capturedEmail["bodyType"] != "html" {
		t.Errorf("bodyType = %v, want html", capturedEmail["bodyType"])
	}
	if capturedEmail["subject"] != "My Subject" {
		t.Errorf("subject = %v, want My Subject", capturedEmail["subject"])
	}
}

func TestFetchToken_FormEncoded(t *testing.T) {
	var capturedForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		capturedForm = r.Form
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"access_token": "tok"})
	}))
	defer srv.Close()

	// second server for email endpoint
	emailSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"txId": "x"})
	}))
	defer emailSrv.Close()

	setCHESEnv(t, emailSrv.URL, srv.URL)

	client := notify.NewClient()
	client.Send("owner@example.com", "Sub", "<p>b</p>")

	if capturedForm.Get("grant_type") != "client_credentials" {
		t.Errorf("grant_type = %q, want client_credentials", capturedForm.Get("grant_type"))
	}
	if capturedForm.Get("client_id") != "test-client" {
		t.Errorf("client_id = %q, want test-client", capturedForm.Get("client_id"))
	}
}
