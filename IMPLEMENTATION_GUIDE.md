# Namespace Audit Automation — Implementation Guide

## Context & Constraints

| Item | Detail |
|------|--------|
| Language | Go |
| Deployment | Single pod in its own OpenShift namespace (`pruner-system`) |
| Namespace source | ServiceNow CMDB API |
| OpenShift auth | ServiceAccount token + per-namespace RoleBindings |
| JIRA | REST API with API token |
| Database | PostgreSQL |
| No cluster admin | Cross-namespace access via RoleBindings only |

---

## Architectural Decision: Single Deployment (Hub Model)

One pruner instance runs in `pruner-system`. It:
1. Fetches the namespace list from ServiceNow CMDB
2. Uses its ServiceAccount token to access each target namespace (via pre-created RoleBindings)
3. Scans, evaluates, reports, and cleans from a single control point

This avoids per-namespace deployments and gives centralized policy enforcement.

```
ServiceNow CMDB
      │
      ▼ (namespace list)
┌─────────────────────────────────────┐
│         pruner-system namespace      │
│                                     │
│  CronJob → Scanner → Compliance DB  │
│                 └──→ Cleanup Engine  │
│                 └──→ JIRA Reporter   │
│                 └──→ Metrics         │
└──────────────┬──────────────────────┘
               │ RoleBinding (per namespace)
    ┌──────────┼──────────┐
    ▼          ▼          ▼
 vendor-ns-1  vendor-ns-2  vendor-ns-N
```

---

## Project Structure

```
pruner/
├── cmd/
│   └── pruner/
│       └── main.go               # Entry point
├── internal/
│   ├── cmdb/
│   │   └── servicenow.go         # ServiceNow CMDB client
│   ├── scanner/
│   │   └── scanner.go            # OpenShift namespace scanner
│   ├── compliance/
│   │   ├── evaluator.go          # Policy evaluation engine
│   │   └── store.go              # PostgreSQL persistence
│   ├── cleanup/
│   │   └── engine.go             # Image deletion logic
│   ├── jira/
│   │   └── client.go             # JIRA REST API integration
│   ├── policy/
│   │   └── config.go             # YAML policy loader
│   └── metrics/
│       └── prometheus.go         # Metrics exporter
├── config/
│   └── policy.yaml               # Default policy config
├── deploy/
│   ├── cronjob.yaml              # Kubernetes CronJob manifest
│   ├── serviceaccount.yaml       # SA + ClusterRole manifest
│   ├── rolebinding-template.yaml # Template for target namespace RoleBindings
│   └── postgres.yaml             # PostgreSQL StatefulSet
├── go.mod
└── go.sum
```

---

## Implementation Steps

---

### Step 1 — Bootstrap Go Project

```bash
mkdir -p pruner && cd pruner
go mod init github.com/ag/pruner
go get k8s.io/client-go@latest
go get github.com/openshift/client-go@latest
go get github.com/lib/pq                      # PostgreSQL driver
go get gopkg.in/yaml.v3                       # Policy config parsing
go get github.com/prometheus/client_golang     # Metrics
go get go.uber.org/zap                        # Structured logging
```

---

### Step 2 — Policy Configuration

File: [config/policy.yaml](config/policy.yaml)

```yaml
maxImageAgeDays: 60
rules:
  - type: AGE
    threshold: 60        # days — flag images older than this
  - type: UNREFERENCED
    threshold: 30        # days — flag unreferenced images older than this
whitelist:
  namespaces:
    - legacy-system
  images:
    - "registry.example.com/critical/base:latest"
jira:
  project: PLAT
  issuetype: Bug
  slaDays: 14            # days before SLA breach alert
cleanup:
  dryRun: true           # MUST be explicitly set to false to delete
  maxDeletionsPerRun: 50 # circuit breaker
```

File: [internal/policy/config.go](internal/policy/config.go)

```go
package policy

import (
    "os"
    "gopkg.in/yaml.v3"
)

type Config struct {
    MaxImageAgeDays int     `yaml:"maxImageAgeDays"`
    Rules           []Rule  `yaml:"rules"`
    Whitelist       Whitelist `yaml:"whitelist"`
    JIRA            JIRAConfig `yaml:"jira"`
    Cleanup         CleanupConfig `yaml:"cleanup"`
}

type Rule struct {
    Type      string `yaml:"type"`      // AGE | UNREFERENCED
    Threshold int    `yaml:"threshold"` // days
}

type Whitelist struct {
    Namespaces []string `yaml:"namespaces"`
    Images     []string `yaml:"images"`
}

type JIRAConfig struct {
    Project   string `yaml:"project"`
    IssueType string `yaml:"issuetype"`
    SLADays   int    `yaml:"slaDays"`
}

type CleanupConfig struct {
    DryRun            bool `yaml:"dryRun"`
    MaxDeletionsPerRun int  `yaml:"maxDeletionsPerRun"`
}

func Load(path string) (*Config, error) {
    data, err := os.ReadFile(path)
    if err != nil {
        return nil, err
    }
    var cfg Config
    return &cfg, yaml.Unmarshal(data, &cfg)
}
```

---

### Step 3 — ServiceNow CMDB Client

File: [internal/cmdb/servicenow.go](internal/cmdb/servicenow.go)

```go
package cmdb

import (
    "encoding/json"
    "fmt"
    "net/http"
)

type Client struct {
    BaseURL  string
    Username string
    Password string
    Table    string // e.g. "cmdb_ci_kubernetes_namespace"
}

type Namespace struct {
    Name        string `json:"name"`
    Cluster     string `json:"cluster_url"`
    Owner       string `json:"owner_email"`
    Environment string `json:"environment"` // prod, dev, staging
}

func (c *Client) GetNamespaces() ([]Namespace, error) {
    url := fmt.Sprintf("%s/api/now/table/%s?sysparm_fields=name,cluster_url,owner_email,environment", c.BaseURL, c.Table)
    req, _ := http.NewRequest("GET", url, nil)
    req.SetBasicAuth(c.Username, c.Password)
    req.Header.Set("Accept", "application/json")

    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    var result struct {
        Result []Namespace `json:"result"`
    }
    if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
        return nil, err
    }
    return result.Result, nil
}
```

> **Note:** Replace basic auth with OAuth token if ServiceNow instance requires it. Store credentials in a Kubernetes Secret, not in config.

---

### Step 4 — OpenShift Scanner

File: [internal/scanner/scanner.go](internal/scanner/scanner.go)

```go
package scanner

import (
    "context"
    "time"

    imagev1 "github.com/openshift/api/image/v1"
    corev1 "k8s.io/api/core/v1"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/client-go/kubernetes"
    imageclient "github.com/openshift/client-go/image/clientset/versioned"
)

type ImageResult struct {
    Namespace    string
    ImageRef     string
    Tag          string
    CreatedAt    time.Time
    Agedays      int
    Referenced   bool   // true if used by an active Pod
    DockerImage  string
}

type Scanner struct {
    KubeClient  kubernetes.Interface
    ImageClient imageclient.Interface
}

func (s *Scanner) ScanNamespace(ctx context.Context, namespace string) ([]ImageResult, error) {
    // 1. Get all ImageStreamTags
    streams, err := s.ImageClient.ImageV1().ImageStreams(namespace).List(ctx, metav1.ListOptions{})
    if err != nil {
        return nil, err
    }

    // 2. Get all active pod image refs in this namespace
    pods, err := s.KubeClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
    if err != nil {
        return nil, err
    }
    activeImages := activeImageSet(pods)

    var results []ImageResult
    now := time.Now()

    for _, stream := range streams.Items {
        for _, tag := range stream.Status.Tags {
            for _, item := range tag.Items {
                created := item.Created.Time
                age := int(now.Sub(created).Hours() / 24)
                ref := stream.Namespace + "/" + stream.Name + ":" + tag.Tag

                results = append(results, ImageResult{
                    Namespace:   namespace,
                    ImageRef:    ref,
                    Tag:         tag.Tag,
                    CreatedAt:   created,
                    Agedays:     age,
                    Referenced:  activeImages[item.DockerImageReference],
                    DockerImage: item.DockerImageReference,
                })
            }
        }
    }
    return results, nil
}

func activeImageSet(pods *corev1.PodList) map[string]bool {
    set := make(map[string]bool)
    for _, pod := range pods.Items {
        if pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == corev1.PodPending {
            for _, cs := range pod.Status.ContainerStatuses {
                set[cs.ImageID] = true
                set[cs.Image] = true
            }
        }
    }
    return set
}
```

---

### Step 5 — Compliance Service (DB + Evaluator)

#### Database Schema

```sql
CREATE TABLE scans (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    namespace   TEXT NOT NULL,
    scanned_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE images (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    scan_id      UUID REFERENCES scans(id),
    namespace    TEXT NOT NULL,
    image_ref    TEXT NOT NULL,
    tag          TEXT,
    created_at   TIMESTAMPTZ,
    age_days     INT,
    referenced   BOOLEAN,
    docker_image TEXT
);

CREATE TABLE violations (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    image_id     UUID REFERENCES images(id),
    namespace    TEXT NOT NULL,
    image_ref    TEXT NOT NULL,
    rule_type    TEXT NOT NULL,   -- AGE | UNREFERENCED
    severity     TEXT NOT NULL,   -- HIGH | MEDIUM | LOW
    opened_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    resolved_at  TIMESTAMPTZ,
    jira_key     TEXT,            -- e.g. PLAT-1234
    status       TEXT NOT NULL DEFAULT 'OPEN'  -- OPEN | RESOLVED | WHITELISTED
);
```

#### Policy Evaluator

File: [internal/compliance/evaluator.go](internal/compliance/evaluator.go)

```go
package compliance

import (
    "github.com/ag/pruner/internal/policy"
    "github.com/ag/pruner/internal/scanner"
)

type Violation struct {
    ImageRef  string
    Namespace string
    RuleType  string
    Severity  string
}

func Evaluate(results []scanner.ImageResult, cfg *policy.Config) []Violation {
    var violations []Violation

    whitelistedNS := toSet(cfg.Whitelist.Namespaces)
    whitelistedImg := toSet(cfg.Whitelist.Images)

    for _, img := range results {
        if whitelistedNS[img.Namespace] || whitelistedImg[img.DockerImage] {
            continue
        }
        for _, rule := range cfg.Rules {
            switch rule.Type {
            case "AGE":
                if img.AgeDay s> rule.Threshold {
                    violations = append(violations, Violation{
                        ImageRef:  img.ImageRef,
                        Namespace: img.Namespace,
                        RuleType:  "AGE",
                        Severity:  severity(img.AgeDay s, rule.Threshold),
                    })
                }
            case "UNREFERENCED":
                if !img.Referenced && img.AgeDay s> rule.Threshold {
                    violations = append(violations, Violation{
                        ImageRef:  img.ImageRef,
                        Namespace: img.Namespace,
                        RuleType:  "UNREFERENCED",
                        Severity:  "HIGH",
                    })
                }
            }
        }
    }
    return violations
}

func severity(ageDays, threshold int) string {
    ratio := ageDays / threshold
    if ratio >= 3 {
        return "HIGH"
    } else if ratio >= 2 {
        return "MEDIUM"
    }
    return "LOW"
}

func toSet(items []string) map[string]bool {
    s := make(map[string]bool, len(items))
    for _, v := range items {
        s[v] = true
    }
    return s
}
```

---

### Step 6 — Cleanup Engine

File: [internal/cleanup/engine.go](internal/cleanup/engine.go)

```go
package cleanup

import (
    "context"
    "fmt"

    imageclient "github.com/openshift/client-go/image/clientset/versioned"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "github.com/ag/pruner/internal/compliance"
    "github.com/ag/pruner/internal/policy"
    "go.uber.org/zap"
)

type Engine struct {
    ImageClient imageclient.Interface
    Policy      *policy.Config
    Log         *zap.Logger
}

func (e *Engine) Run(ctx context.Context, violations []compliance.Violation) error {
    if e.Policy.Cleanup.DryRun {
        for _, v := range violations {
            e.Log.Info("DRY RUN: would delete", zap.String("image", v.ImageRef))
        }
        return nil
    }

    deleted := 0
    for _, v := range violations {
        if deleted >= e.Policy.Cleanup.MaxDeletionsPerRun {
            e.Log.Warn("max deletions reached, stopping", zap.Int("limit", e.Policy.Cleanup.MaxDeletionsPerRun))
            break
        }
        if v.RuleType != "UNREFERENCED" {
            continue // only auto-delete unreferenced images
        }
        if err := e.deleteImage(ctx, v); err != nil {
            e.Log.Error("delete failed", zap.String("image", v.ImageRef), zap.Error(err))
            continue
        }
        deleted++
        e.Log.Info("deleted image", zap.String("image", v.ImageRef))
    }
    return nil
}

func (e *Engine) deleteImage(ctx context.Context, v compliance.Violation) error {
    // ImageStreamTag name format: "streamname:tag"
    return e.ImageClient.ImageV1().ImageStreamTags(v.Namespace).Delete(ctx, v.ImageRef, metav1.DeleteOptions{})
}
```

---

### Step 7 — JIRA Integration

File: [internal/jira/client.go](internal/jira/client.go)

```go
package jira

import (
    "bytes"
    "encoding/json"
    "fmt"
    "net/http"
)

type Client struct {
    BaseURL string
    Email   string
    Token   string
    Project string
}

type IssuePayload struct {
    Fields struct {
        Project   struct{ Key string } `json:"project"`
        Summary   string              `json:"summary"`
        IssueType struct{ Name string } `json:"issuetype"`
        Description string            `json:"description"`
    } `json:"fields"`
}

func (c *Client) CreateTicket(namespace, imageRef, ruleType, severity string) (string, error) {
    var payload IssuePayload
    payload.Fields.Project.Key = c.Project
    payload.Fields.IssueType.Name = "Bug"
    payload.Fields.Summary = fmt.Sprintf("[Pruner] %s violation: %s in %s", ruleType, imageRef, namespace)
    payload.Fields.Description = fmt.Sprintf(
        "Severity: %s\nNamespace: %s\nImage: %s\nRule: %s\n\nPlease remediate within SLA.",
        severity, namespace, imageRef, ruleType,
    )

    body, _ := json.Marshal(payload)
    req, _ := http.NewRequest("POST", c.BaseURL+"/rest/api/3/issue", bytes.NewReader(body))
    req.SetBasicAuth(c.Email, c.Token)
    req.Header.Set("Content-Type", "application/json")

    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        return "", err
    }
    defer resp.Body.Close()

    var result struct {
        Key string `json:"key"`
    }
    json.NewDecoder(resp.Body).Decode(&result)
    return result.Key, nil
}
```

---

### Step 8 — Main Orchestrator

File: [cmd/pruner/main.go](cmd/pruner/main.go)

```go
package main

import (
    "context"
    "log"
    "os"

    "github.com/ag/pruner/internal/cleanup"
    "github.com/ag/pruner/internal/cmdb"
    "github.com/ag/pruner/internal/compliance"
    "github.com/ag/pruner/internal/jira"
    "github.com/ag/pruner/internal/policy"
    "github.com/ag/pruner/internal/scanner"
    "go.uber.org/zap"
    "k8s.io/client-go/rest"
)

func main() {
    logger, _ := zap.NewProduction()
    ctx := context.Background()

    // Load policy
    cfg, err := policy.Load(os.Getenv("POLICY_PATH"))
    if err != nil {
        log.Fatal("failed to load policy", err)
    }

    // In-cluster config (uses ServiceAccount token automatically)
    k8sCfg, err := rest.InClusterConfig()
    if err != nil {
        log.Fatal("failed to get in-cluster config", err)
    }

    // Init clients (scanner, CMDB, JIRA)
    sc := scanner.NewScanner(k8sCfg)
    cmdbClient := &cmdb.Client{
        BaseURL:  os.Getenv("SERVICENOW_URL"),
        Username: os.Getenv("SERVICENOW_USER"),
        Password: os.Getenv("SERVICENOW_PASS"),
        Table:    os.Getenv("SERVICENOW_TABLE"),
    }
    jiraClient := &jira.Client{
        BaseURL: os.Getenv("JIRA_URL"),
        Email:   os.Getenv("JIRA_EMAIL"),
        Token:   os.Getenv("JIRA_TOKEN"),
        Project: os.Getenv("JIRA_PROJECT"),
    }
    cleanupEngine := &cleanup.Engine{Policy: cfg, Log: logger}

    // Pull namespace list from ServiceNow
    namespaces, err := cmdbClient.GetNamespaces()
    if err != nil {
        log.Fatal("failed to fetch namespaces from CMDB", err)
    }

    for _, ns := range namespaces {
        logger.Info("scanning namespace", zap.String("namespace", ns.Name))

        images, err := sc.ScanNamespace(ctx, ns.Name)
        if err != nil {
            logger.Error("scan failed", zap.String("namespace", ns.Name), zap.Error(err))
            continue
        }

        violations := compliance.Evaluate(images, cfg)
        logger.Info("violations found", zap.String("namespace", ns.Name), zap.Int("count", len(violations)))

        for _, v := range violations {
            key, err := jiraClient.CreateTicket(v.Namespace, v.ImageRef, v.RuleType, v.Severity)
            if err != nil {
                logger.Error("jira ticket failed", zap.Error(err))
            } else {
                logger.Info("jira ticket created", zap.String("key", key))
            }
        }

        if err := cleanupEngine.Run(ctx, violations); err != nil {
            logger.Error("cleanup failed", zap.String("namespace", ns.Name), zap.Error(err))
        }
    }
}
```

---

### Step 9 — OpenShift Deployment Manifests

#### ServiceAccount + Role

File: [deploy/serviceaccount.yaml](deploy/serviceaccount.yaml)

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: pruner
  namespace: pruner-system
```

#### RoleBinding Template (applied in each target namespace by namespace owner)

File: [deploy/rolebinding-template.yaml](deploy/rolebinding-template.yaml)

```yaml
# Apply this in each target namespace:
# oc apply -f rolebinding-template.yaml -n <vendor-namespace>
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: pruner-reader
  namespace: <TARGET_NAMESPACE>   # replace this
subjects:
  - kind: ServiceAccount
    name: pruner
    namespace: pruner-system
roleRef:
  kind: ClusterRole
  name: pruner-reader
  apiGroup: rbac.authorization.k8s.io
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: pruner-reader
rules:
  - apiGroups: ["image.openshift.io"]
    resources: ["imagestreams", "imagestreamtags"]
    verbs: ["get", "list", "delete"]
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "list"]
  - apiGroups: ["apps"]
    resources: ["deployments", "replicasets"]
    verbs: ["get", "list"]
```

#### CronJob

File: [deploy/cronjob.yaml](deploy/cronjob.yaml)

```yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: pruner
  namespace: pruner-system
spec:
  schedule: "0 2 * * *"   # runs daily at 2am
  jobTemplate:
    spec:
      template:
        spec:
          serviceAccountName: pruner
          restartPolicy: OnFailure
          containers:
            - name: pruner
              image: registry.example.com/pruner:latest
              envFrom:
                - secretRef:
                    name: pruner-secrets
              env:
                - name: POLICY_PATH
                  value: /config/policy.yaml
              volumeMounts:
                - name: policy
                  mountPath: /config
          volumes:
            - name: policy
              configMap:
                name: pruner-policy
```

#### Secrets (create via CLI, never commit to Git)

```bash
oc create secret generic pruner-secrets \
  --from-literal=SERVICENOW_URL=https://your-instance.service-now.com \
  --from-literal=SERVICENOW_USER=svc-pruner \
  --from-literal=SERVICENOW_PASS=<password> \
  --from-literal=SERVICENOW_TABLE=cmdb_ci_kubernetes_namespace \
  --from-literal=JIRA_URL=https://your-org.atlassian.net \
  --from-literal=JIRA_EMAIL=pruner-bot@ag.com \
  --from-literal=JIRA_TOKEN=<token> \
  --from-literal=JIRA_PROJECT=PLAT \
  -n pruner-system
```

---

## Onboarding a New Namespace

When a new namespace appears in ServiceNow CMDB, the namespace owner must run:

```bash
# 1. Apply the ClusterRole (one-time, if not already done)
oc apply -f deploy/rolebinding-template.yaml

# 2. Create the RoleBinding in their namespace
oc adm policy add-role-to-user pruner-reader \
  system:serviceaccount:pruner-system:pruner \
  -n <their-namespace>
```

The pruner will automatically pick it up on the next CronJob run.

---

## Environment Variables Reference

| Variable | Description |
|----------|-------------|
| `POLICY_PATH` | Path to policy.yaml inside the container |
| `SERVICENOW_URL` | ServiceNow instance base URL |
| `SERVICENOW_USER` | ServiceNow API username |
| `SERVICENOW_PASS` | ServiceNow API password |
| `SERVICENOW_TABLE` | CMDB table name for namespaces |
| `JIRA_URL` | Atlassian JIRA base URL |
| `JIRA_EMAIL` | JIRA bot account email |
| `JIRA_TOKEN` | JIRA API token |
| `JIRA_PROJECT` | JIRA project key (e.g. `PLAT`) |

---

## Rollout Phases

| Phase | Scope | Duration |
|-------|-------|----------|
| 1 | Policy config + CMDB client | Week 1 |
| 2 | Scanner service (read-only) | Week 2 |
| 3 | Compliance DB + evaluator | Week 3 |
| 4 | JIRA integration + reporting | Week 4 |
| 5 | Cleanup engine (dry-run) | Week 5 |
| 6 | Cleanup engine (live, opt-in) | Week 6+ |

---

## Open Questions to Confirm

- [ ] What is the exact ServiceNow CMDB table name for namespaces?
- [ ] Does ServiceNow use basic auth or OAuth tokens?
- [ ] Is there one OpenShift cluster or multiple (multi-cluster support needed)?
- [ ] What JIRA project key should violations be raised under?
- [ ] Who owns the namespace onboarding process (applying RoleBindings)?
- [ ] Is PostgreSQL available in the cluster or do we use an external managed DB?
