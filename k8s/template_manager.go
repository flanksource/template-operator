package k8s

import (
	"context"
	"fmt"
	"regexp"

	templatev1 "github.com/flanksource/template-operator/api/v1"
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/yaml"
)

var (
	stripTemplateRegexp      = regexp.MustCompile(`(\{\{(.*)}})+`)
	alreadyAppliedAnnotation = "platform.flanksource.com/template-operator_%s_%s"
)

type TemplateManager struct {
	DynamicClient *DynamicClient
	Log           logr.Logger
	PatchApplier  *PatchApplier
}

type ResourcePatch struct {
	Resource   *unstructured.Unstructured
	Patch      string
	Kind       string
	APIVersion string
	PatchType  PatchType
}

func NewTemplateManager(c *DynamicClient, log logr.Logger) *TemplateManager {
	tm := &TemplateManager{
		DynamicClient: c,
		Log:           log,
		PatchApplier:  NewPatchApplier(c.Clientset, log),
	}
	return tm
}

func (tm *TemplateManager) Run(ctx context.Context, template *templatev1.Template) error {
	labelSelector, err := labelSelectorToString(template.Spec.Source.NamespaceSelector)
	if err != nil {
		return err
	}
	listOptions := metav1.ListOptions{
		LabelSelector: labelSelector,
	}
	namespaces, err := tm.DynamicClient.Clientset.CoreV1().Namespaces().List(ctx, listOptions)
	if err != nil {
		tm.Log.Error(err, "failed to list namespaces")
		return errors.Wrap(err, "failed to list namespaces")
	}

	modifiedResources := []ResourcePatch{}
	errorsCount := 0

	for _, namespace := range namespaces.Items {
		for _, patch := range template.Spec.Patches {
			// Remove templating from string, YAML is failing to parse it otherwise
			strippedPatch := stripTemplateRegexp.ReplaceAllString(patch, "")
			typemeta := metav1.TypeMeta{}

			if err := yaml.Unmarshal([]byte(strippedPatch), &typemeta); err != nil {
				tm.Log.Error(err, "failed to parse patch type meta")
				continue
			}

			resources, err := tm.getForTypeMeta(ctx, typemeta, namespace.Name, template)
			if err != nil {
				tm.Log.Error(err, "failed to list resources")
				continue
			}

			fmt.Printf("Found %d resources\n", len(resources.Items))

			for _, r := range resources.Items {
				fmt.Println("Found resource", "kind", r.GetKind(), "name", r.GetName(), "namespace", r.GetNamespace())
			}

			for _, item := range resources.Items {
				if template.Spec.Onceoff && alreadyApplied(template, item) {
					continue
				}
				resourcePatch := ResourcePatch{
					Resource:   &item,
					Patch:      patch,
					Kind:       typemeta.Kind,
					APIVersion: typemeta.APIVersion,
					PatchType:  PatchTypeYaml,
				}

				if err := tm.handleResource(ctx, template, resourcePatch); err != nil {
					tm.Log.Error(err, "failed to apply callback")
					errorsCount++
					continue
				}

				modifiedResources = append(modifiedResources, resourcePatch)
			}
		}

		for _, patch := range template.Spec.JsonPatches {
			resources, err := tm.getForTypeMeta(ctx, patch.Object, namespace.Name, template)
			if err != nil {
				tm.Log.Error(err, "failed to list resources")
				continue
			}

			fmt.Printf("Found %d resources\n", len(resources.Items))

			for _, r := range resources.Items {
				fmt.Println("Found resource", "kind", r.GetKind(), "name", r.GetName(), "namespace", r.GetNamespace())
			}

			for _, item := range resources.Items {
				if template.Spec.Onceoff && alreadyApplied(template, item) {
					continue
				}
				resourcePatch := ResourcePatch{
					Resource:   &item,
					Patch:      patch.Patch,
					Kind:       patch.Object.Kind,
					APIVersion: patch.Object.APIVersion,
					PatchType:  PatchTypeJSON,
				}

				if err := tm.handleResource(ctx, template, resourcePatch); err != nil {
					tm.Log.Error(err, "failed to apply callback")
					errorsCount++
					continue
				}

				modifiedResources = append(modifiedResources, resourcePatch)
			}
		}
	}

	for _, rp := range modifiedResources {
		client, err := tm.DynamicClient.GetClientByKind(rp.Kind, rp.APIVersion)
		if err != nil {
			tm.Log.Error(err, "failed to get dynamic client", "kind", rp.Kind)
			continue
		}

		resource, err := client.Namespace(rp.Resource.GetNamespace()).Get(ctx, rp.Resource.GetName(), metav1.GetOptions{})
		if err != nil {
			tm.Log.Error(err, "failed to get", "kind", rp.Kind, "name", rp.Resource.GetName(), "namespace", rp.Resource.GetNamespace())
			continue
		}
		annotations := resource.GetAnnotations()
		annotations[mkAnnotation(template)] = "true"
		resource.SetAnnotations(annotations)

		if _, err := client.Namespace(resource.GetNamespace()).Update(ctx, resource, metav1.UpdateOptions{}); err != nil {
			tm.Log.Error(err, "failed to update annotation on resource", "kind", rp.Kind, "name", resource.GetName(), "namespace", resource.GetNamespace())
		}
	}

	if errorsCount > 0 {
		return errors.Errorf("Multiple errors occured patching resources")
	}
	return nil
}

func (tm *TemplateManager) handleResource(ctx context.Context, template *templatev1.Template, rs ResourcePatch) error {
	yml, _ := yaml.Marshal(rs.Resource)
	fmt.Printf("=================\nResource before:\n%s\n---\n", yml)

	newResource, err := tm.PatchApplier.Apply(rs.Resource, rs.Patch, rs.PatchType)
	if err != nil {
		tm.Log.Error(err, "failed to apply patch to resource", "kind", rs.Kind, "name", rs.Resource.GetName(), "namespace", rs.Resource.GetNamespace())
		return err
	}
	resource := newResource
	yml, _ = yaml.Marshal(resource.Object)
	fmt.Printf("=================\nResource after:\n%s\n---\n", yml)

	client, err := tm.DynamicClient.GetClientByKind(rs.Kind, rs.APIVersion)
	if err != nil {
		return errors.Wrapf(err, "failed to get dynamic client for kind %s", rs.Kind)
	}

	fmt.Printf("Updating resource: %s in namespace %s\n", resource.GetName(), resource.GetNamespace())

	if _, err := client.Namespace(resource.GetNamespace()).Update(ctx, resource, metav1.UpdateOptions{}); err != nil {
		return errors.Wrap(err, "failed to update resource")
	}
	return nil
}

func (tm *TemplateManager) getForTypeMeta(ctx context.Context, typemeta metav1.TypeMeta, namespace string, template *templatev1.Template) (*unstructured.UnstructuredList, error) {
	client, err := tm.DynamicClient.GetClientByKind(typemeta.Kind, typemeta.APIVersion)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get dynamic client for kind %s", typemeta.Kind)
	}

	labelSelector, err := labelSelectorToString(template.Spec.Source.LabelSelector)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get label selector")
	}

	options := metav1.ListOptions{
		FieldSelector: template.Spec.Source.FieldSelector,
		LabelSelector: labelSelector,
	}
	resources, err := client.Namespace(namespace).List(ctx, options)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to list resources for kind %s", typemeta.Kind)
	}
	return resources, nil
}

func labelSelectorToString(l metav1.LabelSelector) (string, error) {
	labelMap, err := metav1.LabelSelectorAsMap(&l)
	if err != nil {
		return "", errors.Wrap(err, "failed to transform LabelSelector to map")
	}
	return labels.SelectorFromSet(labelMap).String(), nil
}

func alreadyApplied(template *templatev1.Template, item unstructured.Unstructured) bool {
	annotation := mkAnnotation(template)
	value, found := item.GetAnnotations()[annotation]
	if found && value == "true" {
		return true
	}
	return false
}

func mkAnnotation(template *templatev1.Template) string {
	return fmt.Sprintf(alreadyAppliedAnnotation, template.Namespace, template.Name)
}
