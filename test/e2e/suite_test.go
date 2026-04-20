//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	imagev1 "github.com/openshift/api/image/v1"
	imageclient "github.com/openshift/client-go/image/clientset/versioned"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// suite holds shared test state.
type suite struct {
	ctx         context.Context
	restCfg     *rest.Config
	kube        kubernetes.Interface
	imageClient imageclient.Interface
	dynamic     dynamic.Interface

	// mock external services
	jiraSrv     *httptest.Server
	chesSrv     *httptest.Server
	snowSrv     *httptest.Server

	// captured calls for assertions
	jiraTickets []map[string]any
	chesEmails  []map[string]any
}

func newSuite(t *testing.T) *suite {
	t.Helper()

	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		t.Skip("KUBECONFIG not set — skipping e2e tests")
	}

	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatalf("failed to build kubeconfig: %v", err)
	}

	kube, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("failed to create kube client: %v", err)
	}

	imgClient, err := imageclient.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("failed to create image client: %v", err)
	}

	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("failed to create dynamic client: %v", err)
	}

	s := &suite{
		ctx:         context.Background(),
		restCfg:     cfg,
		kube:        kube,
		imageClient: imgClient,
		dynamic:     dyn,
	}
	s.startMockServers(t)
	return s
}

func (s *suite) startMockServers(t *testing.T) {
	t.Helper()

	// Mock JIRA
	s.jiraSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rest/api/3/user/search" || containsStr(r.URL.Path, "user/search"):
			json.NewEncoder(w).Encode([]map[string]any{
				{"accountId": "test-account-id", "emailAddress": "owner@example.com", "active": true},
			})
		default:
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			s.jiraTickets = append(s.jiraTickets, body)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"key": "PLAT-" + itoa(len(s.jiraTickets))})
		}
	}))
	t.Cleanup(s.jiraSrv.Close)

	// Mock CHES (token + email endpoints)
	s.chesSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if containsStr(r.URL.Path, "token") {
			json.NewEncoder(w).Encode(map[string]string{"access_token": "test-token"})
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		s.chesEmails = append(s.chesEmails, body)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"txId": "tx-123"})
	}))
	t.Cleanup(s.chesSrv.Close)

	// Mock ServiceNow CMDB
	s.snowSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"result": []map[string]any{
				{
					"name":        "pruner-e2e-ns",
					"cluster_url": "https://kind-cluster",
					"owner_email": "owner@example.com",
					"environment": "test",
				},
			},
		})
	}))
	t.Cleanup(s.snowSrv.Close)
}

// createNamespace creates a test namespace and registers cleanup.
func (s *suite) createNamespace(t *testing.T, name string) {
	t.Helper()
	_, err := s.kube.CoreV1().Namespaces().Create(s.ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create namespace %q: %v", name, err)
	}
	t.Cleanup(func() {
		s.kube.CoreV1().Namespaces().Delete(s.ctx, name, metav1.DeleteOptions{})
	})
}

// createImageStream creates an ImageStream with tags via dynamic client.
func (s *suite) createImageStream(t *testing.T, ns, name string, tags []imagev1.NamedTagEventList) {
	t.Helper()

	gvr := schema.GroupVersionResource{
		Group:    "image.openshift.io",
		Version:  "v1",
		Resource: "imagestreams",
	}

	obj := map[string]any{
		"apiVersion": "image.openshift.io/v1",
		"kind":       "ImageStream",
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"status":     map[string]any{"tags": marshalTags(tags)},
	}

	raw, _ := json.Marshal(obj)
	unstructured := &unstructuredObj{}
	json.Unmarshal(raw, unstructured)

	_, err := s.dynamic.Resource(gvr).Namespace(ns).Create(
		s.ctx,
		toUnstructured(obj),
		metav1.CreateOptions{},
	)
	if err != nil {
		t.Fatalf("create ImageStream %q/%q: %v", ns, name, err)
	}
	_ = unstructured
}

// createPod creates a pod referencing a specific image.
func (s *suite) createPod(t *testing.T, ns, name string, imageRef string, phase corev1.PodPhase) {
	t.Helper()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app", Image: imageRef}},
		},
		Status: corev1.PodStatus{
			Phase: phase,
			ContainerStatuses: []corev1.ContainerStatus{{
				Image:   imageRef,
				ImageID: imageRef,
			}},
		},
	}
	_, err := s.kube.CoreV1().Pods(ns).Create(s.ctx, pod, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create pod %q/%q: %v", ns, name, err)
	}
	// update status separately (Kind doesn't set status on create)
	s.kube.CoreV1().Pods(ns).UpdateStatus(s.ctx, pod, metav1.UpdateOptions{})
}

// setEnv sets all environment variables required by pruner packages.
func (s *suite) setEnv(t *testing.T, ns string) {
	t.Helper()
	vars := map[string]string{
		"SERVICENOW_URL":   s.snowSrv.URL,
		"SERVICENOW_USER":  "user",
		"SERVICENOW_PASS":  "pass",
		"SERVICENOW_TABLE": "cmdb_test",
		"CLUSTER_URL":      "https://kind-cluster",
		"JIRA_URL":         s.jiraSrv.URL,
		"JIRA_EMAIL":       "bot@example.com",
		"JIRA_TOKEN":       "test",
		"JIRA_PROJECT":     "PLAT",
		"CHES_CLIENT_ID":   "client",
		"CHES_CLIENT_SECRET": "secret",
		"CHES_FROM":        "pruner@example.com",
		"CHES_BASE_URL":    s.chesSrv.URL,
		"CHES_TOKEN_URL":   s.chesSrv.URL + "/token",
	}
	for k, v := range vars {
		os.Setenv(k, v)
	}
	t.Cleanup(func() {
		for k := range vars {
			os.Unsetenv(k)
		}
	})
}

// tagEvent builds a NamedTagEventList with a single item.
func tagEvent(tag, dockerRef string, ageDays int) imagev1.NamedTagEventList {
	return imagev1.NamedTagEventList{
		Tag: tag,
		Items: []imagev1.TagEvent{{
			DockerImageReference: dockerRef,
			Created:              metav1.NewTime(time.Now().AddDate(0, 0, -ageDays)),
		}},
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}
