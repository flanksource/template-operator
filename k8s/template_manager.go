package k8s

import (
	"context"
	"fmt"
	"regexp"

	"github.com/flanksource/commons/text"
	templatev1 "github.com/flanksource/template-operator/api/v1"
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

var (
	stripTemplateRegexp      = regexp.MustCompile(`(\{\{(.*)}})+`)
	alreadyAppliedAnnotation = "platform.flanksource.com/template-operator_%s_%s"
)

type TemplateManager struct {
	Client *Client
	kubernetes.Interface
	Log          logr.Logger
	PatchApplier *PatchApplier
}

type ResourcePatch struct {
	Resource   *unstructured.Unstructured
	Patch      string
	Kind       string
	APIVersion string
	PatchType  PatchType
}

func NewTemplateManager(c *Client, log logr.Logger) *TemplateManager {
	clientset, _ := c.GetClientset()
	tm := &TemplateManager{
		Client:       c,
		Log:          log,
		PatchApplier: NewPatchApplier(clientset, log),
	}
	return tm
}

func (tm *TemplateManager) selectResources(ctx context.Context, selector *templatev1.ResourceSelector) ([]unstructured.Unstructured, error) {
	if selector.Kind == "" || selector.APIVersion == "" {
		return nil, errors.New("must specify a kind and apiVersion")
	}
	var sources []unstructured.Unstructured

	var namespaceNames []string
	if len(selector.NamespaceSelector.MatchExpressions) == 0 && len(selector.NamespaceSelector.MatchLabels) == 0 {
		namespaceNames = []string{v1.NamespaceAll}
	} else {

		labelSelector, err := labelSelectorToString(selector.NamespaceSelector)
		if err != nil {
			return nil, err
		}
		listOptions := metav1.ListOptions{
			LabelSelector: labelSelector,
		}
		namespaces, err := tm.CoreV1().Namespaces().List(ctx, listOptions)
		if err != nil {
			return nil, errors.Wrap(err, "failed to list namespaces")
		}
		for _, namespace := range namespaces.Items {
			namespaceNames = append(namespaceNames, namespace.Name)
		}
	}

	// first iterate over selected namsespaces
	for _, namespace := range namespaceNames {
		client, err := tm.Client.GetClientByKind(selector.Kind)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to get dynamic client for kind %s", selector.Kind)
		}

		labelSelector, err := labelSelectorToString(selector.LabelSelector)
		if err != nil {
			return nil, err
		}
		options := metav1.ListOptions{
			FieldSelector: selector.FieldSelector,
			LabelSelector: labelSelector,
		}
		resources, err := client.Namespace(namespace).List(ctx, options)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to list resources for kind %s", selector.Kind)
		}
		for _, item := range resources.Items {
			sources = append(sources, item)
		}
	}
	return sources, nil
}

func (tm *TemplateManager) Run(ctx context.Context, template *templatev1.Template) error {
	sources, err := tm.selectResources(ctx, &template.Spec.Source)
	if err != nil {
		return err
	}

	for _, source := range sources {
		target := &source
		if !template.Spec.Onceoff || !alreadyApplied(template, *target) {

			for _, patch := range template.Spec.Patches {
				target, err = tm.PatchApplier.Apply(target, patch, PatchTypeYaml)
				if err != nil {
					return err
				}
			}
			for _, patch := range template.Spec.JsonPatches {
				target, err = tm.PatchApplier.Apply(target, patch.Patch, PatchTypeJSON)
				if err != nil {
					return err
				}
			}
			if len(template.Spec.JsonPatches) > 0 || len(template.Spec.Patches) > 0 {
				target = markApplied(template, target)
				if err := tm.Client.ApplyUnstructured(source.GetNamespace(), target); err != nil {
					return err
				}
			}
		}
		for _, item := range template.Spec.Resources {
			data, err := text.Template(string(item.Raw), target.Object)
			if err != nil {
				return err
			}
			objs, err := GetUnstructuredObjects([]byte(data))
			if err != nil {
				return nil
			}
			for _, obj := range objs {
				obj.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: source.GetAPIVersion(), Kind: source.GetKind(), Name: source.GetName(), UID: source.GetUID()}})

				if tm.Log.V(2).Enabled() {
					tm.Log.V(2).Info("Applying", "kind", obj.GetKind(), "namespace", obj.GetNamespace(), "name", obj.GetName(), "obj", obj)
				} else {
					tm.Log.Info("Applying", "kind", obj.GetKind(), "namespace", obj.GetNamespace(), "name", obj.GetName())
				}
				if err := tm.Client.ApplyUnstructured(obj.GetNamespace(), &obj); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func labelSelectorToString(l metav1.LabelSelector) (string, error) {
	labelMap, err := metav1.LabelSelectorAsMap(&l)
	if err != nil {
		return "", errors.Wrap(err, "failed to transform LabelSelector to map")
	}
	return labels.SelectorFromSet(labelMap).String(), nil
}

func markApplied(template *templatev1.Template, item *unstructured.Unstructured) *unstructured.Unstructured {
	annotations := item.GetAnnotations()
	annotations[mkAnnotation(template)] = "true"
	item.SetAnnotations(annotations)
	return item
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
