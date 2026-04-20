package cleanup

import (
	"context"
	"fmt"

	imagev1client "github.com/openshift/client-go/image/clientset/versioned"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"go.uber.org/zap"

	"github.com/ag/pruner/internal/compliance"
	"github.com/ag/pruner/internal/policy"
)

type Engine struct {
	imageClient imagev1client.Interface
	policy      *policy.Config
	log         *zap.Logger
}

func New(imageClient imagev1client.Interface, cfg *policy.Config, log *zap.Logger) *Engine {
	return &Engine{imageClient: imageClient, policy: cfg, log: log}
}

type Result struct {
	Deleted []string
	Skipped []string
	Errors  []string
}

func (e *Engine) Run(ctx context.Context, violations []compliance.Violation) Result {
	var result Result

	if e.policy.Cleanup.DryRun {
		for _, v := range violations {
			e.log.Info("dry-run: would delete", zap.String("image", v.ImageRef))
			result.Skipped = append(result.Skipped, v.ImageRef)
		}
		return result
	}

	deleted := 0
	for _, v := range violations {
		if deleted >= e.policy.Cleanup.MaxDeletionsPerRun {
			e.log.Warn("max deletions reached, halting", zap.Int("limit", e.policy.Cleanup.MaxDeletionsPerRun))
			break
		}

		// Only auto-delete unreferenced images — age violations need human review
		if v.RuleType != "UNREFERENCED" {
			result.Skipped = append(result.Skipped, v.ImageRef)
			continue
		}

		if err := e.delete(ctx, v.Namespace, v.ImageRef); err != nil {
			e.log.Error("delete failed", zap.String("image", v.ImageRef), zap.Error(err))
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", v.ImageRef, err))
			continue
		}

		e.log.Info("deleted", zap.String("image", v.ImageRef), zap.String("namespace", v.Namespace))
		result.Deleted = append(result.Deleted, v.ImageRef)
		deleted++
	}
	return result
}

func (e *Engine) delete(ctx context.Context, namespace, imageRef string) error {
	return e.imageClient.ImageV1().ImageStreamTags(namespace).Delete(ctx, imageRef, metav1.DeleteOptions{})
}
