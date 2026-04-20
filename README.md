# Pruner — AG Image Hygiene Automation

Pruner is an ISB-owned automated service that enforces container image hygiene across vendor OpenShift namespaces. It detects stale and unreferenced images, raises JIRA tickets, notifies namespace owners, and safely removes eligible images.

---

## Table of Contents

- [Background](#background)
- [Architecture](#architecture)
- [Workflow](#workflow)
- [Components](#components)
- [Policy Configuration](#policy-configuration)
- [Environment Variables](#environment-variables)
- [Deployment](#deployment)
- [Local Development](#local-development)

---

## Background

OCIO does not provide enterprise-level enforcement for container image hygiene across vendor-managed OpenShift namespaces. Pruner fills this gap by:

- Pulling the authoritative namespace list from **ServiceNow CMDB**
- Scanning **OpenShift ImageStreams** for stale and unreferenced images
- Evaluating findings against a **YAML policy** stored in Git
- Raising **JIRA tickets** for violations
- Sending **HTML email reports** to namespace owners via CHES
- Safely **deleting unreferenced images** (dry-run by default)

One instance of Pruner is deployed per OpenShift cluster. All instances share the same container image — behaviour is controlled by environment variables and policy config.

---

## Architecture

```mermaid
graph TB
    subgraph External["External Systems"]
        SN[("ServiceNow CMDB\nNamespace Registry")]
        JIRA[("JIRA\nPLAT Project")]
        CHES["CHES\nEmail API"]
        OIDC["CHES OIDC\nloginproxy.gov.bc.ca"]
    end

    subgraph Cluster["OpenShift Cluster (per cluster)"]
        subgraph PrunerNS["pruner-system namespace"]
            CJ["CronJob\n(daily 2am)"]
            POD["Pruner Pod\n(Go binary)"]
            SA["ServiceAccount\npruner"]
            SEC["Secret\npruner-secrets"]
            CM["ConfigMap\npruner-policy"]
        end

        subgraph VendorNS["Vendor Namespaces (from CMDB)"]
            IS["ImageStreams"]
            PD["Pods"]
        end
    end

    CJ -->|spawns| POD
    POD -->|reads| SA
    POD -->|reads| SEC
    POD -->|reads| CM
    SA -->|RoleBinding| VendorNS

    POD -->|"GET namespaces\n(filtered by cluster)"| SN
    POD -->|"GET imagestreams\nGET pods"| IS
    POD -->|"GET imagestreams\nGET pods"| PD
    POD -->|"POST /rest/api/3/issue"| JIRA
    POD -->|"POST /email"| CHES
    POD -->|"client_credentials\ntoken fetch"| OIDC
    OIDC -->|Bearer token| POD
```

---

## Workflow

```mermaid
flowchart TD
    A([CronJob fires\ndaily at 2am]) --> B

    B["1. Fetch namespaces\nfrom ServiceNow CMDB\nfiltered by CLUSTER_URL"]
    B --> C{Namespaces\nreturned?}
    C -- No --> Z([Exit])
    C -- Yes --> D

    D["2. For each namespace\nQuery OpenShift API via ServiceAccount"]
    D --> E["Collect ImageStreams\n→ image refs, tags, created dates"]
    D --> F["Collect Pods\n→ active image refs"]
    E & F --> G

    G["3. Calculate per image\n• Age in days\n• Referenced = used by running pod"]
    G --> H

    H["4. Evaluate against Policy\n• AGE rule: age > threshold\n• UNREFERENCED rule: not referenced + age > threshold\n• Skip whitelisted namespaces & images"]
    H --> I{Violations\nfound?}
    I -- No --> D
    I -- Yes --> J

    J["5. Raise JIRA tickets\nOne ticket per violation\nLabels: pruner, image-hygiene, rule-type"]
    J --> K

    K["6. Cleanup Engine"]
    K --> L{dryRun\nenabled?}
    L -- Yes --> M["Log 'would delete'\nNo API calls made"]
    L -- No --> N["Delete UNREFERENCED images\nvia OpenShift API\nAGE violations → JIRA only\nStops at maxDeletionsPerRun"]

    M & N --> O

    O["7. Render HTML Report\n• Summary stats\n• Violations table\n• JIRA keys\n• Cleanup results"]
    O --> P["8. Fetch CHES token\nclient_credentials grant\nloginproxy.gov.bc.ca"]
    P --> Q["9. Send email report\nvia CHES API\nto: owner_email from CMDB"]
    Q --> D
```

---

## Sequence Diagram

```mermaid
sequenceDiagram
    participant CJ as CronJob
    participant P as Pruner Pod
    participant SN as ServiceNow CMDB
    participant OCP as OpenShift API
    participant JR as JIRA
    participant OIDC as CHES OIDC
    participant CHES as CHES Email API

    CJ->>P: spawn pod

    P->>SN: GET /api/now/table/{table}?cluster_url={url}
    SN-->>P: [ {name, owner_email, environment}, ... ]

    loop For each namespace
        P->>OCP: GET /apis/image.openshift.io/v1/namespaces/{ns}/imagestreams
        OCP-->>P: ImageStream list

        P->>OCP: GET /api/v1/namespaces/{ns}/pods
        OCP-->>P: Pod list

        Note over P: Calculate age + referenced status
        Note over P: Evaluate against policy rules
        Note over P: Apply whitelist

        alt Violations found
            loop Per violation
                P->>JR: POST /rest/api/3/issue
                JR-->>P: { key: "PLAT-1234" }
            end

            alt dryRun: false
                P->>OCP: DELETE imageStreamTag (UNREFERENCED only)
                OCP-->>P: 200 OK
            end

            P->>OIDC: POST /token (client_credentials)
            OIDC-->>P: { access_token }

            P->>CHES: POST /api/v1/email (HTML report)
            CHES-->>P: { txId, messages }
        end
    end

    Note over P: Run complete
```

---

## Components

### Policy Definition Layer
Centralised YAML config stored in a Kubernetes ConfigMap (Git-versioned).
Defines age thresholds, cleanup rules, and namespace/image whitelists.
**File:** [`config/policy.yaml`](config/policy.yaml)

### Image Hygiene Scanner
Queries OpenShift `ImageStreams` and `Pods` per namespace via the Kubernetes API.
Calculates image age and determines if each image is actively referenced by a running pod.
**File:** [`internal/scanner/scanner.go`](internal/scanner/scanner.go)

### ServiceNow CMDB Client
Fetches the authoritative list of AG-owned namespaces from ServiceNow, filtered by the current cluster URL. Provides namespace name, owner email, and environment.
**File:** [`internal/cmdb/servicenow.go`](internal/cmdb/servicenow.go)

### Compliance Evaluator
Evaluates scan results against policy rules. Produces typed violations with severity levels (`HIGH`, `MEDIUM`, `LOW`). Respects whitelist configuration.
**File:** [`internal/compliance/evaluator.go`](internal/compliance/evaluator.go)

### Cleanup Engine
Safely deletes unreferenced images via the OpenShift `ImageStreamTag` API.
- Dry-run is the default — no deletions without explicit opt-in
- Only `UNREFERENCED` images are auto-deleted; `AGE` violations go to JIRA for human review
- Circuit breaker halts after `maxDeletionsPerRun` deletions

**File:** [`internal/cleanup/engine.go`](internal/cleanup/engine.go)

### JIRA Integration
Creates one JIRA ticket per violation in the configured project. Tickets include namespace, image ref, rule type, severity, age, and remediation instructions.
**File:** [`internal/jira/client.go`](internal/jira/client.go)

### Report Generator
Renders a per-namespace HTML email report showing violation summary, violations table, JIRA tickets raised, and cleanup results.
**File:** [`internal/report/report.go`](internal/report/report.go)

### CHES Email Notifier
Sends the HTML report to the namespace owner email (sourced from ServiceNow CMDB) via the BC Government Common Hosted Email Service (CHES). Fetches a fresh Bearer token via client credentials on every run.
**File:** [`internal/notify/email.go`](internal/notify/email.go)

---

## Policy Configuration

```yaml
# config/policy.yaml

maxImageAgeDays: 60

rules:
  - type: AGE
    threshold: 60        # flag images older than 60 days

  - type: UNREFERENCED
    threshold: 30        # flag unreferenced images older than 30 days

whitelist:
  namespaces:
    - legacy-system      # never scan this namespace
  images:
    - registry.example.com/critical/base:latest  # never flag this image

jira:
  project: PLAT
  issuetype: Bug
  slaDays: 14            # SLA breach after 14 days unresolved

cleanup:
  dryRun: true           # set to false to enable actual deletion
  maxDeletionsPerRun: 50 # circuit breaker
```

### Severity Matrix

| Rule | Condition | Severity |
|------|-----------|----------|
| AGE | age ≥ 3× threshold | HIGH |
| AGE | age ≥ 2× threshold | MEDIUM |
| AGE | age > threshold | LOW |
| UNREFERENCED | not referenced + age > threshold | HIGH |

---

## Environment Variables

| Variable | Required | Description |
|----------|----------|-------------|
| `POLICY_PATH` | No | Path to policy.yaml (default: `/config/policy.yaml`) |
| `CLUSTER_URL` | Yes | This cluster's API URL — used to filter CMDB namespaces |
| `SERVICENOW_URL` | Yes | ServiceNow instance base URL |
| `SERVICENOW_USER` | Yes | ServiceNow API username |
| `SERVICENOW_PASS` | Yes | ServiceNow API password |
| `SERVICENOW_TABLE` | Yes | CMDB table name for namespaces |
| `JIRA_URL` | Yes | Atlassian JIRA base URL |
| `JIRA_EMAIL` | Yes | JIRA bot account email |
| `JIRA_TOKEN` | Yes | JIRA API token |
| `JIRA_PROJECT` | Yes | JIRA project key (e.g. `PLAT`) |
| `CHES_CLIENT_ID` | Yes | CHES OIDC client ID |
| `CHES_CLIENT_SECRET` | Yes | CHES OIDC client secret |
| `CHES_FROM` | Yes | Sender email address (authorised in CHES) |

All secrets are stored in an OpenShift Secret (`pruner-secrets`) — never committed to Git.

---

## Deployment

### Prerequisites
- OpenShift namespace `pruner-system` exists
- Kubernetes ServiceAccount `pruner` created in `pruner-system`
- `pruner` ServiceAccount has `admin` RoleBinding in all target AG namespaces
- CHES client credentials obtained from the CHES service team
- JIRA API token created for the bot account

### Deploy

```bash
# 1. Create secrets (run once)
bash deploy/secrets.sh

# 2. Apply manifests
oc apply -f deploy/serviceaccount.yaml -n pruner-system
oc apply -f deploy/configmap.yaml      -n pruner-system
oc apply -f deploy/cronjob.yaml        -n pruner-system

# 3. Trigger a manual test run
oc create job pruner-test \
  --from=cronjob/pruner \
  -n pruner-system

# 4. Watch logs
oc logs -f job/pruner-test -n pruner-system
```

### Enable Live Cleanup (after dry-run validation)

Edit the ConfigMap and set `dryRun: false`:

```bash
oc edit configmap pruner-policy -n pruner-system
```

---

## Local Development

To run locally (outside a cluster), swap `rest.InClusterConfig()` for a kubeconfig:

```bash
export KUBECONFIG=~/.kube/config
export CLUSTER_URL=https://api.your-cluster.example.com:6443
export SERVICENOW_URL=...
# set all other env vars

go run ./cmd/pruner
```

You will need to update `cmd/pruner/main.go` to use `clientcmd.BuildConfigFromFlags("", os.Getenv("KUBECONFIG"))` instead of `rest.InClusterConfig()` for local runs.

---

## Project Structure

```
pruner/
├── cmd/
│   └── pruner/
│       └── main.go               # Entry point and orchestrator
├── internal/
│   ├── cmdb/
│   │   └── servicenow.go         # ServiceNow CMDB client
│   ├── scanner/
│   │   └── scanner.go            # OpenShift image + pod scanner
│   ├── compliance/
│   │   └── evaluator.go          # Policy evaluation engine
│   ├── cleanup/
│   │   └── engine.go             # Image deletion engine
│   ├── jira/
│   │   └── client.go             # JIRA REST API client
│   ├── report/
│   │   └── report.go             # HTML + text report renderer
│   └── notify/
│       └── email.go              # CHES email notification client
├── config/
│   └── policy.yaml               # Default policy configuration
├── deploy/
│   ├── serviceaccount.yaml       # ServiceAccount manifest
│   ├── cronjob.yaml              # CronJob manifest
│   ├── configmap.yaml            # Policy ConfigMap
│   └── secrets.sh                # Secret creation script
├── Dockerfile
├── go.mod
└── go.sum
```
