package policy_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ag/pruner/internal/policy"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "policy-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(content)
	f.Close()
	return f.Name()
}

func TestLoad(t *testing.T) {
	path := writeTemp(t, `
maxImageAgeDays: 90
rules:
  - type: AGE
    threshold: 60
  - type: UNREFERENCED
    threshold: 30
whitelist:
  namespaces:
    - legacy-system
  images:
    - registry.example.com/base:latest
jira:
  project: PLAT
  issuetype: Bug
  slaDays: 14
cleanup:
  dryRun: true
  maxDeletionsPerRun: 25
`)
	cfg, err := policy.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.MaxImageAgeDays != 90 {
		t.Errorf("MaxImageAgeDays = %d, want 90", cfg.MaxImageAgeDays)
	}
	if len(cfg.Rules) != 2 {
		t.Errorf("Rules len = %d, want 2", len(cfg.Rules))
	}
	if cfg.Rules[0].Type != "AGE" || cfg.Rules[0].Threshold != 60 {
		t.Errorf("unexpected rule[0]: %+v", cfg.Rules[0])
	}
	if cfg.JIRA.Project != "PLAT" {
		t.Errorf("JIRA.Project = %q, want PLAT", cfg.JIRA.Project)
	}
	if !cfg.Cleanup.DryRun {
		t.Error("Cleanup.DryRun should be true")
	}
	if cfg.Cleanup.MaxDeletionsPerRun != 25 {
		t.Errorf("MaxDeletionsPerRun = %d, want 25", cfg.Cleanup.MaxDeletionsPerRun)
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := policy.Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	path := writeTemp(t, "{ invalid yaml [[[")
	_, err := policy.Load(path)
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestIsNamespaceWhitelisted(t *testing.T) {
	cfg := &policy.Config{
		Whitelist: policy.Whitelist{
			Namespaces: []string{"legacy-system", "exempt-ns"},
		},
	}

	tests := []struct {
		ns   string
		want bool
	}{
		{"legacy-system", true},
		{"exempt-ns", true},
		{"vendor-ns-1", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := cfg.IsNamespaceWhitelisted(tt.ns); got != tt.want {
			t.Errorf("IsNamespaceWhitelisted(%q) = %v, want %v", tt.ns, got, tt.want)
		}
	}
}

func TestIsImageWhitelisted(t *testing.T) {
	cfg := &policy.Config{
		Whitelist: policy.Whitelist{
			Images: []string{"registry.example.com/base:latest"},
		},
	}

	if !cfg.IsImageWhitelisted("registry.example.com/base:latest") {
		t.Error("expected image to be whitelisted")
	}
	if cfg.IsImageWhitelisted("registry.example.com/other:v1") {
		t.Error("expected image not to be whitelisted")
	}
}
