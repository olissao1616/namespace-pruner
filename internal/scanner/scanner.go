package scanner

import (
	"context"
	"time"

	imagev1client "github.com/openshift/client-go/image/clientset/versioned"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type ImageResult struct {
	Namespace   string
	ImageRef    string
	Tag         string
	CreatedAt   time.Time
	AgeDays     int
	Referenced  bool
	DockerImage string
}

type Scanner struct {
	kube  kubernetes.Interface
	image imagev1client.Interface
}

func New(cfg *rest.Config) (*Scanner, error) {
	kube, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	image, err := imagev1client.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Scanner{kube: kube, image: image}, nil
}

func (s *Scanner) ScanNamespace(ctx context.Context, namespace string) ([]ImageResult, error) {
	streams, err := s.image.ImageV1().ImageStreams(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	pods, err := s.kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	active := activeImages(pods)

	now := time.Now()
	var results []ImageResult

	for _, stream := range streams.Items {
		for _, tag := range stream.Status.Tags {
			for _, item := range tag.Items {
				age := int(now.Sub(item.Created.Time).Hours() / 24)
				ref := stream.Namespace + "/" + stream.Name + ":" + tag.Tag

				results = append(results, ImageResult{
					Namespace:   namespace,
					ImageRef:    ref,
					Tag:         tag.Tag,
					CreatedAt:   item.Created.Time,
					AgeDays:     age,
					Referenced:  active[item.DockerImageReference],
					DockerImage: item.DockerImageReference,
				})
			}
		}
	}
	return results, nil
}

func activeImages(pods *corev1.PodList) map[string]bool {
	active := make(map[string]bool)
	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodRunning && pod.Status.Phase != corev1.PodPending {
			continue
		}
		for _, cs := range pod.Status.ContainerStatuses {
			active[cs.Image] = true
			active[cs.ImageID] = true
		}
	}
	return active
}
