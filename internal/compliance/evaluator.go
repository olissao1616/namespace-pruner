package compliance

import (
	"github.com/ag/pruner/internal/policy"
	"github.com/ag/pruner/internal/scanner"
)

type Violation struct {
	Namespace   string
	ImageRef    string
	DockerImage string
	RuleType    string
	Severity    string
	AgeDays     int
}

func Evaluate(results []scanner.ImageResult, cfg *policy.Config) []Violation {
	var violations []Violation

	for _, img := range results {
		if cfg.IsNamespaceWhitelisted(img.Namespace) || cfg.IsImageWhitelisted(img.DockerImage) {
			continue
		}
		for _, rule := range cfg.Rules {
			switch rule.Type {
			case "AGE":
				if img.AgeDays > rule.Threshold {
					violations = append(violations, Violation{
						Namespace:   img.Namespace,
						ImageRef:    img.ImageRef,
						DockerImage: img.DockerImage,
						RuleType:    "AGE",
						Severity:    ageSeverity(img.AgeDays, rule.Threshold),
						AgeDays:     img.AgeDays,
					})
				}
			case "UNREFERENCED":
				if !img.Referenced && img.AgeDays > rule.Threshold {
					violations = append(violations, Violation{
						Namespace:   img.Namespace,
						ImageRef:    img.ImageRef,
						DockerImage: img.DockerImage,
						RuleType:    "UNREFERENCED",
						Severity:    "HIGH",
						AgeDays:     img.AgeDays,
					})
				}
			}
		}
	}
	return violations
}

func ageSeverity(ageDays, threshold int) string {
	ratio := ageDays / threshold
	switch {
	case ratio >= 3:
		return "HIGH"
	case ratio >= 2:
		return "MEDIUM"
	default:
		return "LOW"
	}
}
