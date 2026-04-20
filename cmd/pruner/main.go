package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	imagev1client "github.com/openshift/client-go/image/clientset/versioned"
	"go.uber.org/zap"
	"k8s.io/client-go/rest"

	"github.com/ag/pruner/internal/cleanup"
	"github.com/ag/pruner/internal/cmdb"
	"github.com/ag/pruner/internal/compliance"
	"github.com/ag/pruner/internal/jira"
	"github.com/ag/pruner/internal/notify"
	"github.com/ag/pruner/internal/policy"
	"github.com/ag/pruner/internal/report"
	"github.com/ag/pruner/internal/scanner"
)

func main() {
	log, _ := zap.NewProduction()
	defer log.Sync()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	cfg, err := policy.Load(envOrDefault("POLICY_PATH", "/config/policy.yaml"))
	if err != nil {
		log.Fatal("failed to load policy", zap.Error(err))
	}

	k8sCfg, err := rest.InClusterConfig()
	if err != nil {
		log.Fatal("failed to get in-cluster config", zap.Error(err))
	}

	sc, err := scanner.New(k8sCfg)
	if err != nil {
		log.Fatal("failed to create scanner", zap.Error(err))
	}

	imageClient, err := imagev1client.NewForConfig(k8sCfg)
	if err != nil {
		log.Fatal("failed to create image client", zap.Error(err))
	}

	cmdbClient := cmdb.NewClient()
	jiraClient := jira.NewClient()
	notifyClient := notify.NewClient()
	cleanupEngine := cleanup.New(imageClient, cfg, log)

	namespaces, err := cmdbClient.GetNamespaces()
	if err != nil {
		log.Fatal("failed to fetch namespaces from ServiceNow", zap.Error(err))
	}

	log.Info("starting scan", zap.Int("namespaces", len(namespaces)))

	for _, ns := range namespaces {
		select {
		case <-ctx.Done():
			log.Info("shutting down")
			return
		default:
		}

		nsLog := log.With(zap.String("namespace", ns.Name))

		images, err := sc.ScanNamespace(ctx, ns.Name)
		if err != nil {
			nsLog.Error("scan failed", zap.Error(err))
			continue
		}
		nsLog.Info("scan complete", zap.Int("images", len(images)))

		violations := compliance.Evaluate(images, cfg)
		nsLog.Info("violations found", zap.Int("count", len(violations)))

		// Raise JIRA tickets and collect keys for the report
		var jiraKeys []string
		for _, v := range violations {
			key, err := jiraClient.CreateTicket(v.Namespace, v.ImageRef, v.RuleType, v.Severity, ns.OwnerEmail, v.AgeDays)
			if err != nil {
				nsLog.Error("jira ticket failed", zap.String("image", v.ImageRef), zap.Error(err))
				continue
			}
			nsLog.Info("jira ticket created", zap.String("key", key))
			jiraKeys = append(jiraKeys, key)
		}

		cleanupResult := cleanupEngine.Run(ctx, violations)
		nsLog.Info("cleanup complete",
			zap.Int("deleted", len(cleanupResult.Deleted)),
			zap.Int("skipped", len(cleanupResult.Skipped)),
			zap.Int("errors", len(cleanupResult.Errors)),
		)

		// Build and send report only when there are violations
		if len(violations) == 0 {
			continue
		}

		r := &report.NamespaceReport{
			Namespace:   ns.Name,
			ClusterURL:  os.Getenv("CLUSTER_URL"),
			OwnerEmail:  ns.OwnerEmail,
			ScannedAt:   time.Now().UTC(),
			TotalImages: len(images),
			Violations:  violations,
			JIRAKeys:    jiraKeys,
			Cleanup:     cleanupResult,
		}

		html, err := report.RenderHTML(r)
		if err != nil {
			nsLog.Error("report render failed", zap.Error(err))
			continue
		}

		subject := "[Pruner] Image hygiene violations in " + ns.Name
		if err := notifyClient.Send(ns.OwnerEmail, subject, html); err != nil {
			nsLog.Error("notification failed", zap.String("to", ns.OwnerEmail), zap.Error(err))
		} else {
			nsLog.Info("report sent", zap.String("to", ns.OwnerEmail))
		}
	}

	log.Info("pruner run complete")
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
