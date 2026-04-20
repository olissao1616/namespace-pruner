package scanner_test

import (
	"context"
	"testing"
	"time"

	imagev1 "github.com/openshift/api/image/v1"
	fakeimage "github.com/openshift/client-go/image/clientset/versioned/fake"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/ag/pruner/internal/scanner"
)

func makeStream(ns, name string, tags []imagev1.NamedTagEventList) imagev1.ImageStream {
	return imagev1.ImageStream{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Status:     imagev1.ImageStreamStatus{Tags: tags},
	}
}

func makeTag(tag, dockerRef string, created time.Time) imagev1.NamedTagEventList {
	return imagev1.NamedTagEventList{
		Tag: tag,
		Items: []imagev1.TagEvent{
			{DockerImageReference: dockerRef, Created: metav1.NewTime(created)},
		},
	}
}

func makePod(ns, name string, phase corev1.PodPhase, images ...string) corev1.Pod {
	var statuses []corev1.ContainerStatus
	for _, img := range images {
		statuses = append(statuses, corev1.ContainerStatus{Image: img, ImageID: img})
	}
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Status:     corev1.PodStatus{Phase: phase, ContainerStatuses: statuses},
	}
}

func TestScanNamespace_ReturnsImages(t *testing.T) {
	ns := "vendor-ns"
	old := time.Now().AddDate(0, 0, -90)

	imageClient := fakeimage.NewClientset(
		&imagev1.ImageStreamList{Items: []imagev1.ImageStream{
			makeStream(ns, "myapp", []imagev1.NamedTagEventList{
				makeTag("v1", "registry/myapp:v1", old),
				makeTag("v2", "registry/myapp:v2", time.Now().AddDate(0, 0, -5)),
			}),
		}},
	)
	kubeClient := fake.NewSimpleClientset()

	sc := scanner.NewWithClients(kubeClient, imageClient)
	results, err := sc.ScanNamespace(context.Background(), ns)
	if err != nil {
		t.Fatalf("ScanNamespace() error = %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}

	for _, r := range results {
		if r.Namespace != ns {
			t.Errorf("Namespace = %q, want %q", r.Namespace, ns)
		}
	}
}

func TestScanNamespace_ReferencedDetection(t *testing.T) {
	ns := "vendor-ns"
	activeDockerRef := "registry/myapp:active"
	oldTime := time.Now().AddDate(0, 0, -45)

	imageClient := fakeimage.NewClientset(
		&imagev1.ImageStreamList{Items: []imagev1.ImageStream{
			makeStream(ns, "myapp", []imagev1.NamedTagEventList{
				makeTag("active", activeDockerRef, oldTime),
				makeTag("unused", "registry/myapp:unused", oldTime),
			}),
		}},
	)
	kubeClient := fake.NewSimpleClientset(
		&corev1.PodList{Items: []corev1.Pod{
			makePod(ns, "running-pod", corev1.PodRunning, activeDockerRef),
		}},
	)

	sc := scanner.NewWithClients(kubeClient, imageClient)
	results, err := sc.ScanNamespace(context.Background(), ns)
	if err != nil {
		t.Fatalf("ScanNamespace() error = %v", err)
	}

	refMap := make(map[string]bool)
	for _, r := range results {
		refMap[r.DockerImage] = r.Referenced
	}

	if !refMap[activeDockerRef] {
		t.Error("active image should be referenced")
	}
	if refMap["registry/myapp:unused"] {
		t.Error("unused image should not be referenced")
	}
}

func TestScanNamespace_AgeCalculation(t *testing.T) {
	ns := "vendor-ns"
	created := time.Now().AddDate(0, 0, -30)

	imageClient := fakeimage.NewClientset(
		&imagev1.ImageStreamList{Items: []imagev1.ImageStream{
			makeStream(ns, "app", []imagev1.NamedTagEventList{
				makeTag("v1", "registry/app:v1", created),
			}),
		}},
	)
	kubeClient := fake.NewSimpleClientset()

	sc := scanner.NewWithClients(kubeClient, imageClient)
	results, err := sc.ScanNamespace(context.Background(), ns)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results")
	}
	if results[0].AgeDays < 29 || results[0].AgeDays > 31 {
		t.Errorf("AgeDays = %d, want ~30", results[0].AgeDays)
	}
}

func TestScanNamespace_EmptyNamespace(t *testing.T) {
	imageClient := fakeimage.NewClientset()
	kubeClient := fake.NewSimpleClientset()

	sc := scanner.NewWithClients(kubeClient, imageClient)
	results, err := sc.ScanNamespace(context.Background(), "empty-ns")
	if err != nil {
		t.Fatalf("ScanNamespace() error = %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestScanNamespace_TerminatedPodsNotActive(t *testing.T) {
	ns := "vendor-ns"
	dockerRef := "registry/app:v1"
	oldTime := time.Now().AddDate(0, 0, -45)

	imageClient := fakeimage.NewClientset(
		&imagev1.ImageStreamList{Items: []imagev1.ImageStream{
			makeStream(ns, "app", []imagev1.NamedTagEventList{
				makeTag("v1", dockerRef, oldTime),
			}),
		}},
	)
	kubeClient := fake.NewSimpleClientset(
		&corev1.PodList{Items: []corev1.Pod{
			makePod(ns, "dead-pod", corev1.PodSucceeded, dockerRef),
		}},
	)

	sc := scanner.NewWithClients(kubeClient, imageClient)
	results, err := sc.ScanNamespace(context.Background(), ns)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results")
	}
	if results[0].Referenced {
		t.Error("succeeded pod should not mark image as referenced")
	}
}
