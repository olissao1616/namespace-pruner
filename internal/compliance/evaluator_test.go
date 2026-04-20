package compliance_test

import (
	"testing"
	"time"

	"github.com/ag/pruner/internal/compliance"
	"github.com/ag/pruner/internal/policy"
	"github.com/ag/pruner/internal/scanner"
)

var testPolicy = &policy.Config{
	Rules: []policy.Rule{
		{Type: "AGE", Threshold: 60},
		{Type: "UNREFERENCED", Threshold: 30},
	},
	Whitelist: policy.Whitelist{
		Namespaces: []string{"exempt-ns"},
		Images:     []string{"registry.example.com/base:latest"},
	},
}

func img(namespace, ref, docker string, ageDays int, referenced bool) scanner.ImageResult {
	return scanner.ImageResult{
		Namespace:   namespace,
		ImageRef:    ref,
		DockerImage: docker,
		AgeDays:     ageDays,
		Referenced:  referenced,
		CreatedAt:   time.Now().AddDate(0, 0, -ageDays),
	}
}

func TestEvaluate_AgeViolation(t *testing.T) {
	images := []scanner.ImageResult{
		img("vendor-ns", "vendor-ns/app:v1", "registry/app:v1", 90, true),
	}
	violations := compliance.Evaluate(images, testPolicy)
	if len(violations) != 1 {
		t.Fatalf("expected 1 violation, got %d", len(violations))
	}
	if violations[0].RuleType != "AGE" {
		t.Errorf("RuleType = %q, want AGE", violations[0].RuleType)
	}
}

func TestEvaluate_UnreferencedViolation(t *testing.T) {
	images := []scanner.ImageResult{
		img("vendor-ns", "vendor-ns/old:v1", "registry/old:v1", 45, false),
	}
	violations := compliance.Evaluate(images, testPolicy)
	if len(violations) != 1 {
		t.Fatalf("expected 1 violation, got %d", len(violations))
	}
	if violations[0].RuleType != "UNREFERENCED" {
		t.Errorf("RuleType = %q, want UNREFERENCED", violations[0].RuleType)
	}
	if violations[0].Severity != "HIGH" {
		t.Errorf("Severity = %q, want HIGH", violations[0].Severity)
	}
}

func TestEvaluate_NoViolation_Young(t *testing.T) {
	images := []scanner.ImageResult{
		img("vendor-ns", "vendor-ns/app:v2", "registry/app:v2", 10, true),
	}
	violations := compliance.Evaluate(images, testPolicy)
	if len(violations) != 0 {
		t.Errorf("expected no violations, got %d", len(violations))
	}
}

func TestEvaluate_WhitelistedNamespace(t *testing.T) {
	images := []scanner.ImageResult{
		img("exempt-ns", "exempt-ns/app:old", "registry/app:old", 200, false),
	}
	violations := compliance.Evaluate(images, testPolicy)
	if len(violations) != 0 {
		t.Errorf("whitelisted namespace should produce no violations, got %d", len(violations))
	}
}

func TestEvaluate_WhitelistedImage(t *testing.T) {
	images := []scanner.ImageResult{
		img("vendor-ns", "vendor-ns/base:latest", "registry.example.com/base:latest", 200, false),
	}
	violations := compliance.Evaluate(images, testPolicy)
	if len(violations) != 0 {
		t.Errorf("whitelisted image should produce no violations, got %d", len(violations))
	}
}

func TestEvaluate_ReferencedImageNotUnreferenced(t *testing.T) {
	images := []scanner.ImageResult{
		img("vendor-ns", "vendor-ns/app:v1", "registry/app:v1", 45, true), // referenced — no UNREFERENCED violation
	}
	violations := compliance.Evaluate(images, testPolicy)
	for _, v := range violations {
		if v.RuleType == "UNREFERENCED" {
			t.Error("referenced image should not produce UNREFERENCED violation")
		}
	}
}

func TestAgeSeverity(t *testing.T) {
	tests := []struct {
		ageDays   int
		wantHigh  bool
		wantMed   bool
	}{
		{180, true, false},  // 3× threshold (60) → HIGH
		{120, false, true},  // 2× threshold → MEDIUM
		{70, false, false},  // just over threshold → LOW
	}

	cfg := &policy.Config{
		Rules: []policy.Rule{{Type: "AGE", Threshold: 60}},
	}

	for _, tt := range tests {
		images := []scanner.ImageResult{
			img("vendor-ns", "vendor-ns/app:v1", "registry/app:v1", tt.ageDays, true),
		}
		violations := compliance.Evaluate(images, cfg)
		if len(violations) != 1 {
			t.Fatalf("ageDays=%d: expected 1 violation", tt.ageDays)
		}
		sev := violations[0].Severity
		if tt.wantHigh && sev != "HIGH" {
			t.Errorf("ageDays=%d: want HIGH, got %s", tt.ageDays, sev)
		}
		if tt.wantMed && sev != "MEDIUM" {
			t.Errorf("ageDays=%d: want MEDIUM, got %s", tt.ageDays, sev)
		}
		if !tt.wantHigh && !tt.wantMed && sev != "LOW" {
			t.Errorf("ageDays=%d: want LOW, got %s", tt.ageDays, sev)
		}
	}
}

func TestEvaluate_MultipleViolations(t *testing.T) {
	images := []scanner.ImageResult{
		img("vendor-ns", "vendor-ns/a:v1", "registry/a:v1", 90, true),   // AGE
		img("vendor-ns", "vendor-ns/b:v1", "registry/b:v1", 50, false),  // UNREFERENCED
		img("vendor-ns", "vendor-ns/c:v1", "registry/c:v1", 10, true),   // no violation
	}
	violations := compliance.Evaluate(images, testPolicy)
	if len(violations) != 2 {
		t.Errorf("expected 2 violations, got %d", len(violations))
	}
}
