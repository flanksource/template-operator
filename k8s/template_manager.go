package k8s

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"
	"text/template"

	"github.com/flanksource/kommons"
	"github.com/flanksource/kommons/ktemplate"
	templatev1 "github.com/flanksource/template-operator/api/v1"
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	extapi "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"
)

var (
	stripTemplateRegexp      = regexp.MustCompile(`(\{\{(.*)}})+`)
	alreadyAppliedAnnotation = "platform.flanksource.com/template-operator_%s_%s"
)

type TemplateManager struct {
	Client *kommons.Client
	kubernetes.Interface
	Log           logr.Logger
	PatchApplier  *PatchApplier
	SchemaManager *SchemaManager
	FuncMap       template.FuncMap
}

type ResourcePatch struct {
	Resource   *unstructured.Unstructured
	Patch      string
	Kind       string
	APIVersion string
	PatchType  PatchType
}

func NewTemplateManager(c *kommons.Client, log logr.Logger, cache *SchemaCache) (*TemplateManager, error) {
	clientset, _ := c.GetClientset()

	restConfig, err := c.GetRESTConfig()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get rest config")
	}
	crdClient, err := extapi.NewForConfig(restConfig)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create crd client")
	}

	schemaManager, err := NewSchemaManagerWithCache(clientset, crdClient, cache)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create schema manager")
	}

	patchApplier, err := NewPatchApplier(clientset, schemaManager, log)
	if err != nil {
		return nil, errors.Wrap(err, "faile to create patch applier")
	}

	functions := ktemplate.NewFunctions(clientset)

	tm := &TemplateManager{
		Client:        c,
		Interface:     clientset,
		Log:           log,
		PatchApplier:  patchApplier,
		SchemaManager: schemaManager,
		FuncMap:       functions.FuncMap(),
	}
	return tm, nil
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
	tm.Log.Info("Reconciling", "template", template.Name)
	sources, err := tm.selectResources(ctx, &template.Spec.Source)
	if err != nil {
		return err
	}
	tm.Log.Info("Found resources for template", "template", template.Name, "count", len(sources))

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
				stripAnnotations(target)
				if err := tm.Client.ApplyUnstructured(source.GetNamespace(), target); err != nil {
					return err
				}
			}
		}

		for _, item := range template.Spec.Resources {
			data, err := tm.Template(item.Raw, target.Object)
			if err != nil {
				return err
			}

			objs, err := kommons.GetUnstructuredObjects(data)
			if err != nil {
				return nil
			}
			for _, obj := range objs {
				// cross-namespace owner references are not allowed, so we create an annotation for tracking purposes only
				if source.GetNamespace() == obj.GetNamespace() {
					obj.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: source.GetAPIVersion(), Kind: source.GetKind(), Name: source.GetName(), UID: source.GetUID()}})
				} else {
					crossNamespaceOwner(&obj, source)
				}

				stripAnnotations(&obj)

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

		if template.Spec.CopyToNamespaces != nil {
			for _, namespace := range template.Spec.CopyToNamespaces.Namespaces {
				newResource := source.DeepCopy()
				newResource.SetNamespace(namespace)
				stripAnnotations(newResource)

				crossNamespaceOwner(newResource, source)

				if tm.Log.V(2).Enabled() {
					tm.Log.V(2).Info("Applying", "kind", newResource.GetKind(), "namespace", newResource.GetNamespace(), "name", newResource.GetName(), "obj", newResource)
				} else {
					tm.Log.Info("Applying", "kind", newResource.GetKind(), "namespace", newResource.GetNamespace(), "name", newResource.GetName())
				}

				if err := tm.Client.ApplyUnstructured(newResource.GetNamespace(), newResource); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (tm *TemplateManager) Template(data []byte, vars interface{}) ([]byte, error) {
	convertedYAML, err := yaml.JSONToYAML(data)
	if err != nil {
		return nil, err
	}

	tpl, err := template.New("").Funcs(tm.FuncMap).Parse(string(convertedYAML))

	if err != nil {
		return nil, fmt.Errorf("invalid template %s: %v", strings.Split(string(data), "\n")[0], err)
	}

	rawVars, _ := yaml.Marshal(vars)
	source := make(map[string]interface{})
	if err := yaml.Unmarshal(rawVars, &source); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := tpl.Execute(&buf, source); err != nil {
		return nil, fmt.Errorf("error executing template %s: %v", strings.Split(string(data), "\n")[0], err)
	}

	return tm.duckTypeTemplateResult(buf.Bytes())
}

func (tm *TemplateManager) duckTypeTemplateResult(objYaml []byte) ([]byte, error) {
	obj := &unstructured.Unstructured{}
	if err := yaml.Unmarshal(objYaml, &obj.Object); err != nil {
		return nil, fmt.Errorf("error parsing template result: %v", err)
	}

	version := obj.GetAPIVersion()
	parts := strings.Split(version, "/")
	var apiVersion, apiGroup string
	if len(parts) == 1 {
		apiGroup = ""
		apiVersion = parts[0]
	} else {
		apiGroup = parts[0]
		apiVersion = parts[1]
	}
	groupVersionKind := schema.GroupVersionKind{Group: apiGroup, Version: apiVersion, Kind: obj.GetKind()}

	if err := tm.SchemaManager.DuckType(groupVersionKind, obj); err != nil {
		tm.Log.Error(err, "failed to ducktype object")
	}

	return yaml.Marshal(&obj.Object)
}

func labelSelectorToString(l metav1.LabelSelector) (string, error) {
	labelMap, err := metav1.LabelSelectorAsMap(&l)
	if err != nil {
		return "", errors.Wrap(err, "failed to transform LabelSelector to map")
	}
	return labels.SelectorFromSet(labelMap).String(), nil
}

func crossNamespaceOwner(item *unstructured.Unstructured, owner unstructured.Unstructured) {
	annotations := item.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations["template-operator-owner-ref"] = owner.GetNamespace() + "/" + owner.GetName()
	item.SetAnnotations(annotations)
}

func markApplied(template *templatev1.Template, item *unstructured.Unstructured) *unstructured.Unstructured {
	annotations := item.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}

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
