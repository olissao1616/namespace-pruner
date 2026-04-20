package cmdb_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/ag/pruner/internal/cmdb"
)

func setupServer(t *testing.T, namespaces []cmdb.Namespace) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"result": namespaces})
	}))
}

func TestGetNamespaces(t *testing.T) {
	want := []cmdb.Namespace{
		{Name: "vendor-ns-1", ClusterURL: "https://api.cluster-a.example.com", OwnerEmail: "owner@example.com"},
		{Name: "vendor-ns-2", ClusterURL: "https://api.cluster-a.example.com", OwnerEmail: "owner2@example.com"},
	}
	srv := setupServer(t, want)
	defer srv.Close()

	os.Setenv("SERVICENOW_URL", srv.URL)
	os.Setenv("SERVICENOW_USER", "user")
	os.Setenv("SERVICENOW_PASS", "pass")
	os.Setenv("SERVICENOW_TABLE", "test_table")
	os.Setenv("CLUSTER_URL", "https://api.cluster-a.example.com")
	t.Cleanup(func() {
		os.Unsetenv("SERVICENOW_URL")
		os.Unsetenv("SERVICENOW_USER")
		os.Unsetenv("SERVICENOW_PASS")
		os.Unsetenv("SERVICENOW_TABLE")
		os.Unsetenv("CLUSTER_URL")
	})

	client := cmdb.NewClient()
	got, err := client.GetNamespaces()
	if err != nil {
		t.Fatalf("GetNamespaces() error = %v", err)
	}
	if len(got) != len(want) {
		t.Errorf("got %d namespaces, want %d", len(got), len(want))
	}
	for i, ns := range got {
		if ns.Name != want[i].Name {
			t.Errorf("ns[%d].Name = %q, want %q", i, ns.Name, want[i].Name)
		}
	}
}

func TestGetNamespaces_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	os.Setenv("SERVICENOW_URL", srv.URL)
	os.Setenv("SERVICENOW_USER", "user")
	os.Setenv("SERVICENOW_PASS", "pass")
	os.Setenv("SERVICENOW_TABLE", "test_table")
	os.Setenv("CLUSTER_URL", "https://cluster")
	t.Cleanup(func() {
		os.Unsetenv("SERVICENOW_URL")
		os.Unsetenv("SERVICENOW_USER")
		os.Unsetenv("SERVICENOW_PASS")
		os.Unsetenv("SERVICENOW_TABLE")
		os.Unsetenv("CLUSTER_URL")
	})

	client := cmdb.NewClient()
	_, err := client.GetNamespaces()
	if err == nil {
		t.Error("expected error on server 500, got nil")
	}
}

func TestGetNamespaces_EmptyResult(t *testing.T) {
	srv := setupServer(t, []cmdb.Namespace{})
	defer srv.Close()

	os.Setenv("SERVICENOW_URL", srv.URL)
	os.Setenv("SERVICENOW_USER", "user")
	os.Setenv("SERVICENOW_PASS", "pass")
	os.Setenv("SERVICENOW_TABLE", "test_table")
	os.Setenv("CLUSTER_URL", "https://cluster")
	t.Cleanup(func() {
		os.Unsetenv("SERVICENOW_URL")
		os.Unsetenv("SERVICENOW_USER")
		os.Unsetenv("SERVICENOW_PASS")
		os.Unsetenv("SERVICENOW_TABLE")
		os.Unsetenv("CLUSTER_URL")
	})

	client := cmdb.NewClient()
	got, err := client.GetNamespaces()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %d", len(got))
	}
}
