//go:build e2e

package e2e_test

import (
	"encoding/json"

	imagev1 "github.com/openshift/api/image/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type unstructuredObj = unstructured.Unstructured

func toUnstructured(obj map[string]any) *unstructured.Unstructured {
	raw, _ := json.Marshal(obj)
	u := &unstructured.Unstructured{}
	json.Unmarshal(raw, &u.Object)
	return u
}

func marshalTags(tags []imagev1.NamedTagEventList) []map[string]any {
	var result []map[string]any
	for _, t := range tags {
		var items []map[string]any
		for _, item := range t.Items {
			items = append(items, map[string]any{
				"created":              item.Created.UTC().Format("2006-01-02T15:04:05Z"),
				"dockerImageReference": item.DockerImageReference,
				"image":                item.Image,
				"generation":           item.Generation,
			})
		}
		result = append(result, map[string]any{
			"tag":   t.Tag,
			"items": items,
		})
	}
	return result
}
