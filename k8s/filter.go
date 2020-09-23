package k8s

import (
	"context"
	"fmt"

	templatev1 "github.com/flanksource/template-operator/api/v1"
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/yaml"
)

type Filter struct {
	DynamicClient *DynamicClient
	Log           logr.Logger
}

type ResourcePatch struct {
	Resource   unstructured.Unstructured
	Patch      string
	Kind       string
	APIVersion string
}

func (f *Filter) ResourcesForTemplate(ctx context.Context, template *templatev1.Template) ([]ResourcePatch, error) {
	labelSelector, err := labelSelectorToString(template.Spec.Source.NamespaceSelector)
	if err != nil {
		return nil, err
	}
	listOptions := metav1.ListOptions{
		LabelSelector: labelSelector,
	}
	fmt.Printf("Label selector: %s\n", labelSelector)
	namespaces, err := f.DynamicClient.Clientset.CoreV1().Namespaces().List(ctx, listOptions)
	if err != nil {
		f.Log.Error(err, "failed to list namespaces")
		return nil, errors.Wrap(err, "failed to list namespaces")
	}

	results := []ResourcePatch{}

	for _, namespace := range namespaces.Items {
		for _, patch := range template.Spec.Patches {

			typemeta := metav1.TypeMeta{}
			if err := yaml.Unmarshal([]byte(patch), &typemeta); err != nil {
				f.Log.Error(err, "failed to parse patch type meta")
				continue
			}

			client, err := f.DynamicClient.GetClientByKind(typemeta.Kind)
			if err != nil {
				f.Log.Error(err, "failed to get dynamic client for", "kind", typemeta.Kind)
				continue
			}

			labelSelector, err := labelSelectorToString(template.Spec.Source.LabelSelector)
			if err != nil {
				continue
			}

			options := metav1.ListOptions{
				FieldSelector: template.Spec.Source.FieldSelector,
				LabelSelector: labelSelector,
			}
			resources, err := client.Namespace(namespace.Name).List(ctx, options)
			if err != nil {
				f.Log.Error(err, "failed to list resources for", "kind", typemeta.Kind)
				continue
			}

			fmt.Printf("Found %d resources\n", len(resources.Items))

			for _, r := range resources.Items {
				fmt.Println("Found resource", "kind", r.GetKind(), "name", r.GetName(), "namespace", r.GetNamespace())
			}

			for _, item := range resources.Items {
				resourcePatch := ResourcePatch{
					Resource:   item,
					Patch:      patch,
					Kind:       typemeta.Kind,
					APIVersion: typemeta.APIVersion,
				}
				results = append(results, resourcePatch)
			}
		}
	}

	return results, nil
}

func labelSelectorToString(l metav1.LabelSelector) (string, error) {
	labelMap, err := metav1.LabelSelectorAsMap(&l)
	if err != nil {
		return "", errors.Wrap(err, "failed to transform LabelSelector to map")
	}
	return labels.SelectorFromSet(labelMap).String(), nil
}
