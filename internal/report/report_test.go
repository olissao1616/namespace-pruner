package report_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ag/pruner/internal/cleanup"
	"github.com/ag/pruner/internal/compliance"
	"github.com/ag/pruner/internal/report"
)

func makeReport(violations []compliance.Violation, deleted []string) *report.NamespaceReport {
	return &report.NamespaceReport{
		Namespace:   "vendor-ns-42",
		ClusterURL:  "https://api.cluster-a.example.com",
		OwnerEmail:  "owner@example.com",
		ScannedAt:   time.Date(2026, 4, 20, 2, 0, 0, 0, time.UTC),
		TotalImages: 10,
		Violations:  violations,
		JIRAKeys:    []string{"PLAT-100", "PLAT-101"},
		Cleanup:     cleanup.Result{Deleted: deleted, Skipped: []string{"img:old"}, Errors: []string{}},
	}
}

func TestRenderHTML_ContainsNamespace(t *testing.T) {
	r := makeReport(nil, nil)
	html, err := report.RenderHTML(r)
	if err != nil {
		t.Fatalf("RenderHTML() error = %v", err)
	}
	if !strings.Contains(html, "vendor-ns-42") {
		t.Error("HTML missing namespace")
	}
}

func TestRenderHTML_ContainsViolations(t *testing.T) {
	violations := []compliance.Violation{
		{Namespace: "vendor-ns-42", ImageRef: "vendor-ns-42/app:v1", RuleType: "AGE", Severity: "HIGH", AgeDays: 90},
		{Namespace: "vendor-ns-42", ImageRef: "vendor-ns-42/old:v2", RuleType: "UNREFERENCED", Severity: "HIGH", AgeDays: 45},
	}
	r := makeReport(violations, nil)
	html, err := report.RenderHTML(r)
	if err != nil {
		t.Fatalf("RenderHTML() error = %v", err)
	}

	for _, want := range []string{"vendor-ns-42/app:v1", "AGE", "UNREFERENCED", "PLAT-100", "PLAT-101"} {
		if !strings.Contains(html, want) {
			t.Errorf("HTML missing %q", want)
		}
	}
}

func TestRenderHTML_NoViolations(t *testing.T) {
	r := makeReport(nil, nil)
	html, err := report.RenderHTML(r)
	if err != nil {
		t.Fatalf("RenderHTML() error = %v", err)
	}
	if !strings.Contains(html, "No violations") {
		t.Error("HTML should show no-violations message")
	}
}

func TestRenderHTML_CleanupSummary(t *testing.T) {
	r := makeReport(nil, []string{"vendor-ns-42/old:v1"})
	html, err := report.RenderHTML(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, "vendor-ns-42/old:v1") {
		t.Error("HTML missing deleted image")
	}
}

func TestRenderText_ContainsKeyFields(t *testing.T) {
	violations := []compliance.Violation{
		{Namespace: "vendor-ns-42", ImageRef: "vendor-ns-42/app:v1", RuleType: "AGE", Severity: "HIGH", AgeDays: 90},
	}
	r := makeReport(violations, nil)
	text := report.RenderText(r)

	for _, want := range []string{"vendor-ns-42", "AGE", "HIGH", "PLAT-100"} {
		if !strings.Contains(text, want) {
			t.Errorf("text report missing %q", want)
		}
	}
}

func TestCountBySeverity(t *testing.T) {
	violations := []compliance.Violation{
		{Severity: "HIGH"},
		{Severity: "HIGH"},
		{Severity: "MEDIUM"},
		{Severity: "LOW"},
	}
	r := makeReport(violations, nil)

	if r.CountBySeverity("HIGH") != 2 {
		t.Errorf("HIGH count = %d, want 2", r.CountBySeverity("HIGH"))
	}
	if r.CountBySeverity("MEDIUM") != 1 {
		t.Errorf("MEDIUM count = %d, want 1", r.CountBySeverity("MEDIUM"))
	}
	if r.CountBySeverity("LOW") != 1 {
		t.Errorf("LOW count = %d, want 1", r.CountBySeverity("LOW"))
	}
	if r.CountBySeverity("CRITICAL") != 0 {
		t.Errorf("CRITICAL count should be 0")
	}
}
