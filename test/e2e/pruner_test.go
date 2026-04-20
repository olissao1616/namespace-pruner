//go:build e2e

package e2e_test

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/ag/pruner/internal/cleanup"
	"github.com/ag/pruner/internal/cmdb"
	"github.com/ag/pruner/internal/compliance"
	"github.com/ag/pruner/internal/jira"
	"github.com/ag/pruner/internal/notify"
	"github.com/ag/pruner/internal/policy"
	"github.com/ag/pruner/internal/report"
	"github.com/ag/pruner/internal/scanner"
	imageclient "github.com/openshift/client-go/image/clientset/versioned"
	"go.uber.org/zap"
)

const e2eNS = "pruner-e2e"

// TestE2E_ScanDetectsViolations verifies the full scan → evaluate pipeline
// against a real Kubernetes cluster (Kind + OpenShift CRDs).
func TestE2E_ScanDetectsViolations(t *testing.T) {
	s := newSuite(t)
	s.createNamespace(t, e2eNS)
	s.setEnv(t, e2eNS)

	// Create an old unreferenced image (should trigger UNREFERENCED violation)
	s.createImageStream(t, e2eNS, "old-app", []imagev1.NamedTagEventList{
		tagEvent("v1", "registry.example.com/old-app:v1", 45),
	})

	// Create an old but referenced image (should trigger AGE violation only)
	s.createImageStream(t, e2eNS, "active-app", []imagev1.NamedTagEventList{
		tagEvent("v2", "registry.example.com/active-app:v2", 90),
	})
	s.createPod(t, e2eNS, "active-pod", "registry.example.com/active-app:v2", corev1.PodRunning)

	// Create a fresh image — no violation expected
	s.createImageStream(t, e2eNS, "new-app", []imagev1.NamedTagEventList{
		tagEvent("v1", "registry.example.com/new-app:v1", 5),
	})

	// Wait briefly for resources to be available
	time.Sleep(500 * time.Millisecond)

	sc, err := scanner.New(s.restCfg)
	if err != nil {
		t.Fatalf("scanner.New: %v", err)
	}

	images, err := sc.ScanNamespace(s.ctx, e2eNS)
	if err != nil {
		t.Fatalf("ScanNamespace: %v", err)
	}
	if len(images) == 0 {
		t.Fatal("expected images, got none — are OpenShift CRDs installed on the cluster?")
	}
	t.Logf("scanned %d images", len(images))

	cfg := &policy.Config{
		Rules: []policy.Rule{
			{Type: "AGE", Threshold: 60},
			{Type: "UNREFERENCED", Threshold: 30},
		},
	}
	violations := compliance.Evaluate(images, cfg)
	t.Logf("found %d violations", len(violations))

	hasUnreferenced := false
	hasAge := false
	for _, v := range violations {
		switch v.RuleType {
		case "UNREFERENCED":
			hasUnreferenced = true
		case "AGE":
			hasAge = true
		}
	}
	if !hasUnreferenced {
		t.Error("expected UNREFERENCED violation for old-app:v1")
	}
	if !hasAge {
		t.Error("expected AGE violation for active-app:v2")
	}
}

// TestE2E_FullPipeline runs the complete scan → evaluate → JIRA → cleanup → email flow.
func TestE2E_FullPipeline(t *testing.T) {
	s := newSuite(t)
	s.createNamespace(t, e2eNS+"-full")
	ns := e2eNS + "-full"
	s.setEnv(t, ns)

	s.createImageStream(t, ns, "stale-app", []imagev1.NamedTagEventList{
		tagEvent("v1", "registry.example.com/stale-app:v1", 50),
	})

	time.Sleep(500 * time.Millisecond)

	// --- Scan ---
	sc, err := scanner.New(s.restCfg)
	if err != nil {
		t.Fatalf("scanner.New: %v", err)
	}
	images, err := sc.ScanNamespace(s.ctx, ns)
	if err != nil {
		t.Fatalf("ScanNamespace: %v", err)
	}

	// --- Evaluate ---
	cfg := &policy.Config{
		Rules: []policy.Rule{{Type: "UNREFERENCED", Threshold: 30}},
		Cleanup: policy.CleanupConfig{DryRun: true, MaxDeletionsPerRun: 10},
	}
	violations := compliance.Evaluate(images, cfg)
	if len(violations) == 0 {
		t.Fatal("expected violations")
	}

	// --- JIRA ---
	jiraClient := jira.NewClient()
	var keys []string
	for _, v := range violations {
		key, err := jiraClient.CreateTicket(v.Namespace, v.ImageRef, v.RuleType, v.Severity, "owner@example.com", v.AgeDays)
		if err != nil {
			t.Errorf("CreateTicket: %v", err)
			continue
		}
		keys = append(keys, key)
		t.Logf("JIRA ticket: %s", key)
	}
	if len(s.jiraTickets) == 0 {
		t.Error("expected JIRA tickets to be created")
	}

	// --- Cleanup (dry-run) ---
	imgClient, _ := imageclient.NewForConfig(s.restCfg)
	engine := cleanup.New(imgClient, cfg, zap.NewNop())
	result := engine.Run(s.ctx, violations)
	if len(result.Errors) > 0 {
		t.Errorf("cleanup errors: %v", result.Errors)
	}
	t.Logf("cleanup: deleted=%d skipped=%d", len(result.Deleted), len(result.Skipped))

	// --- Report + Email ---
	r := &report.NamespaceReport{
		Namespace:   ns,
		OwnerEmail:  "owner@example.com",
		TotalImages: len(images),
		Violations:  violations,
		JIRAKeys:    keys,
		Cleanup:     result,
	}
	html, err := report.RenderHTML(r)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}

	notifyClient := notify.NewClient()
	if err := notifyClient.Send("owner@example.com", "[E2E] Pruner report", html); err != nil {
		t.Errorf("Send: %v", err)
	}
	if len(s.chesEmails) == 0 {
		t.Error("expected CHES email to be sent")
	}
	t.Logf("email sent to: %v", s.chesEmails[0]["to"])
}

// TestE2E_CMDBNamespaceDiscovery verifies ServiceNow CMDB integration.
func TestE2E_CMDBNamespaceDiscovery(t *testing.T) {
	s := newSuite(t)
	s.setEnv(t, e2eNS)

	client := cmdb.NewClient()
	namespaces, err := client.GetNamespaces()
	if err != nil {
		t.Fatalf("GetNamespaces: %v", err)
	}
	if len(namespaces) == 0 {
		t.Fatal("expected namespaces from CMDB mock")
	}
	if namespaces[0].Name != "pruner-e2e-ns" {
		t.Errorf("namespace name = %q, want pruner-e2e-ns", namespaces[0].Name)
	}
	if namespaces[0].OwnerEmail != "owner@example.com" {
		t.Errorf("owner email = %q, want owner@example.com", namespaces[0].OwnerEmail)
	}
	t.Logf("CMDB returned %d namespaces", len(namespaces))
}
