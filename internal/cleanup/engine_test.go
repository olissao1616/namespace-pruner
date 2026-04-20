package cleanup_test

import (
	"context"
	"testing"

	imagev1 "github.com/openshift/api/image/v1"
	fakeimage "github.com/openshift/client-go/image/clientset/versioned/fake"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"go.uber.org/zap"

	"github.com/ag/pruner/internal/cleanup"
	"github.com/ag/pruner/internal/compliance"
	"github.com/ag/pruner/internal/policy"
)

func testPolicy(dryRun bool, max int) *policy.Config {
	return &policy.Config{
		Cleanup: policy.CleanupConfig{DryRun: dryRun, MaxDeletionsPerRun: max},
	}
}

func makeViolation(ns, ref, ruleType string) compliance.Violation {
	return compliance.Violation{Namespace: ns, ImageRef: ref, RuleType: ruleType, Severity: "HIGH"}
}

func fakeImageClient(ns, streamTag string) *fakeimage.Clientset {
	return fakeimage.NewClientset(&imagev1.ImageStreamTag{
		ObjectMeta: metav1.ObjectMeta{Name: streamTag, Namespace: ns},
	})
}

func TestRun_DryRun(t *testing.T) {
	client := fakeImageClient("vendor-ns", "app:v1")
	engine := cleanup.New(client, testPolicy(true, 50), zap.NewNop())

	violations := []compliance.Violation{
		makeViolation("vendor-ns", "app:v1", "UNREFERENCED"),
	}
	result := engine.Run(context.Background(), violations)

	if len(result.Deleted) != 0 {
		t.Errorf("dry-run should delete nothing, got %d deletions", len(result.Deleted))
	}
	if len(result.Skipped) != 1 {
		t.Errorf("expected 1 skipped, got %d", len(result.Skipped))
	}
}

func TestRun_DeletesUnreferenced(t *testing.T) {
	client := fakeImageClient("vendor-ns", "app:v1")
	engine := cleanup.New(client, testPolicy(false, 50), zap.NewNop())

	violations := []compliance.Violation{
		makeViolation("vendor-ns", "app:v1", "UNREFERENCED"),
	}
	result := engine.Run(context.Background(), violations)

	if len(result.Deleted) != 1 {
		t.Errorf("expected 1 deletion, got %d", len(result.Deleted))
	}
	if len(result.Errors) != 0 {
		t.Errorf("unexpected errors: %v", result.Errors)
	}
}

func TestRun_SkipsAgeViolations(t *testing.T) {
	client := fakeimage.NewClientset()
	engine := cleanup.New(client, testPolicy(false, 50), zap.NewNop())

	violations := []compliance.Violation{
		makeViolation("vendor-ns", "app:v1", "AGE"),
	}
	result := engine.Run(context.Background(), violations)

	if len(result.Deleted) != 0 {
		t.Error("AGE violations should not be auto-deleted")
	}
	if len(result.Skipped) != 1 {
		t.Errorf("expected 1 skipped, got %d", len(result.Skipped))
	}
}

func TestRun_CircuitBreaker(t *testing.T) {
	client := fakeimage.NewClientset(
		&imagev1.ImageStreamTag{ObjectMeta: metav1.ObjectMeta{Name: "app:v1", Namespace: "ns"}},
		&imagev1.ImageStreamTag{ObjectMeta: metav1.ObjectMeta{Name: "app:v2", Namespace: "ns"}},
		&imagev1.ImageStreamTag{ObjectMeta: metav1.ObjectMeta{Name: "app:v3", Namespace: "ns"}},
	)
	engine := cleanup.New(client, testPolicy(false, 2), zap.NewNop())

	violations := []compliance.Violation{
		makeViolation("ns", "app:v1", "UNREFERENCED"),
		makeViolation("ns", "app:v2", "UNREFERENCED"),
		makeViolation("ns", "app:v3", "UNREFERENCED"),
	}
	result := engine.Run(context.Background(), violations)

	if len(result.Deleted) > 2 {
		t.Errorf("circuit breaker should stop at 2, got %d deletions", len(result.Deleted))
	}
}
