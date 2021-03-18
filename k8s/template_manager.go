package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"github.com/flanksource/kommons"
	"github.com/flanksource/kommons/ktemplate"
	templatev1 "github.com/flanksource/template-operator/api/v1"
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	"github.com/tidwall/gjson"
	v1 "k8s.io/api/core/v1"
	extapi "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
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
	Events        record.EventRecorder
}

type ResourcePatch struct {
	Resource   *unstructured.Unstructured
	Patch      string
	Kind       string
	APIVersion string
	PatchType  PatchType
}

type Conditionals struct {
	When string `json:"when"`
}

type ForEachResource struct {
	ForEach string `json:"forEach"`
}

type ForEach struct {
	IsArray bool
	IsMap   bool
	Array   []interface{}
	Map     map[string]interface{}
}

func NewTemplateManager(c *kommons.Client, log logr.Logger, cache *SchemaCache, events record.EventRecorder) (*TemplateManager, error) {
	clientset, _ := c.GetClientset()

	restConfig, err := c.GetRESTConfig()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get rest config")
	}
	crdClient, err := extapi.NewForConfig(restConfig)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create crd client")
	}

	schemaManager, err := NewSchemaManagerWithCache(clientset, crdClient, cache, log)
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
		Events:        events,
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
					tm.Events.Eventf(&source, v1.EventTypeWarning, "Failed", "Failed to apply patch")
					return err
				}
			}
			for _, patch := range template.Spec.JsonPatches {
				target, err = tm.PatchApplier.Apply(target, patch.Patch, PatchTypeJSON)
				if err != nil {
					tm.Events.Eventf(&source, v1.EventTypeWarning, "Failed", "Failed to apply patch")
					return err
				}
			}
			if len(template.Spec.JsonPatches) > 0 || len(template.Spec.Patches) > 0 {
				target = markApplied(template, target)
				stripAnnotations(target)
				if err := tm.Client.ApplyUnstructured(source.GetNamespace(), target); err != nil {
					tm.Events.Eventf(&source, v1.EventTypeWarning, "Failed", "Failed to apply object")
					return err
				}
			}
		}

		isSourceReady := true

		for _, item := range template.Spec.Resources {
			objs, err := tm.getObjects(item.Raw, target.Object)
			if err != nil {
				tm.Events.Eventf(&source, v1.EventTypeWarning, "Failed", "Failed to get objects")
				return err
			}

			for _, obj := range objs {
				// cross-namespace owner references are not allowed, so we create an annotation for tracking purposes only
				if source.GetNamespace() == obj.GetNamespace() {
					obj.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: source.GetAPIVersion(), Kind: source.GetKind(), Name: source.GetName(), UID: source.GetUID()}})
				} else {
					crossNamespaceOwner(obj, source)
				}

				stripAnnotations(obj)

				if tm.Log.V(2).Enabled() {
					tm.Log.V(2).Info("Applying", "kind", obj.GetKind(), "namespace", obj.GetNamespace(), "name", obj.GetName(), "obj", obj)
				} else {
					tm.Log.Info("Applying", "kind", obj.GetKind(), "namespace", obj.GetNamespace(), "name", obj.GetName())
				}
				if err := tm.Client.ApplyUnstructured(obj.GetNamespace(), obj); err != nil {
					tm.Events.Eventf(&source, v1.EventTypeWarning, "Failed", "Failed to apply new resource kind=%s name=%s", obj.GetKind(), obj.GetName())
					return err
				}

				if isReady, msg, err := tm.isResourceReady(obj); err != nil {
					return errors.Wrap(err, "failed to check if resource is ready")
				} else if !isReady {
					tm.Log.Info("resource is not ready", "kind", obj.GetKind(), "name", obj.GetName(), "namespace", obj.GetNamespace(), "message", msg)
					isSourceReady = false
				} else {
					tm.Log.V(2).Info("resource is ready", "kind", obj.GetKind(), "name", obj.GetName(), "namespace", obj.GetNamespace(), "message", msg)
				}
			}
		}

		if template.Spec.CopyToNamespaces != nil {
			namespaces, err := tm.getNamespaces(ctx, *template.Spec.CopyToNamespaces)
			if err != nil {
				tm.Events.Eventf(&source, v1.EventTypeWarning, "Failed", "Failed to get namespaces")
				return errors.Wrap(err, "failed to get namespaces")
			}

			for _, namespace := range namespaces {
				newResource := source.DeepCopy()
				newResource.SetNamespace(namespace)
				stripAnnotations(newResource)
				kommons.StripIdentifiers(newResource)

				crossNamespaceOwner(newResource, source)

				if tm.Log.V(2).Enabled() {
					tm.Log.V(2).Info("Applying", "kind", newResource.GetKind(), "namespace", newResource.GetNamespace(), "name", newResource.GetName(), "obj", newResource)
				} else {
					tm.Log.Info("Applying", "kind", newResource.GetKind(), "namespace", newResource.GetNamespace(), "name", newResource.GetName())
				}

				if err := tm.Client.ApplyUnstructured(newResource.GetNamespace(), newResource); err != nil {
					tm.Events.Eventf(&source, v1.EventTypeWarning, "Failed", "Failed to copy to namespace %s", namespace)
					return err
				}

				if isReady, msg, err := tm.isResourceReady(newResource); err != nil {
					return errors.Wrap(err, "failed to check if resource is ready")
				} else if !isReady {
					tm.Log.Info("resource is not ready", "kind", newResource.GetKind(), "name", newResource.GetName(), "namespace", newResource.GetNamespace(), "message", msg)
					isSourceReady = false
				}
			}
		}

		conditionName := fmt.Sprintf("template-%s", template.GetName())
		conditionValue := "NotReady"
		if isSourceReady {
			conditionValue = "Ready"
		}
		tm.Log.V(2).Info("setting condition on item", "condition", conditionName, "status", conditionValue, "name", source.GetName(), "namespace", source.GetName(), "kind", source.GetKind())
		if err := tm.Client.SetCondition(&source, conditionName, conditionValue); err != nil {
			tm.Log.Error(err, "failed to set condition on resource", "kind", source.GetKind(), "name", source.GetName(), "namespace", source.GetNamespace(), "conditionValue", conditionValue)
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

func (tm *TemplateManager) isResourceReady(item *unstructured.Unstructured) (bool, string, error) {
	if tm.Client.IsTrivialType(item) {
		return true, "", nil
	}

	refreshed, err := tm.Client.Refresh(item)
	if err != nil {
		return false, "", errors.Wrap(err, "failed to refresh object")
	}
	isReady, msg := tm.Client.IsReady(refreshed)
	return isReady, msg, nil
}

func (tm *TemplateManager) getObjects(rawItem []byte, target map[string]interface{}) ([]*unstructured.Unstructured, error) {
	j, err := json.Marshal(target)
	if err != nil {
		return nil, errors.Wrap(err, "failed to marshal target")
	}
	targetCopy := map[string]interface{}{}
	if err := json.Unmarshal(j, &targetCopy); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal target copy")
	}

	conditional, err := tm.conditional(rawItem, target)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get conditional")
	}
	if !conditional {
		return []*unstructured.Unstructured{}, nil
	}

	forEach, err := tm.getForEach(rawItem, target)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get forEach")
	}

	if !forEach.IsArray && !forEach.IsMap {
		data, err := tm.Template(rawItem, targetCopy)
		if err != nil {
			return nil, errors.Wrap(err, "failed to template resources")
		}

		objs, err := kommons.GetUnstructuredObjects(data)
		if err != nil {
			return nil, errors.Wrap(err, "failed to get unstructured objects")
		}
		return objs, nil
	}

	objs := []*unstructured.Unstructured{}

	if forEach.IsArray {
		for _, e := range forEach.Array {
			targetCopy["each"] = e

			data, err := tm.Template(rawItem, targetCopy)
			if err != nil {
				return nil, errors.Wrap(err, "failed to template resources")
			}

			o, err := kommons.GetUnstructuredObjects(data)
			if err != nil {
				return nil, errors.Wrap(err, "failed to get unstructured objects")
			}

			objs = append(objs, o...)
		}
	} else {
		for k, v := range forEach.Map {
			targetCopy["each"] = map[string]interface{}{
				"key":   k,
				"value": v,
			}

			data, err := tm.Template(rawItem, targetCopy)
			if err != nil {
				return nil, errors.Wrap(err, "failed to template resources")
			}

			o, err := kommons.GetUnstructuredObjects(data)
			if err != nil {
				return nil, errors.Wrap(err, "failed to get unstructured objects")
			}

			objs = append(objs, o...)
		}
	}

	return objs, nil
}

func (tm *TemplateManager) getForEach(rawItem []byte, target map[string]interface{}) (*ForEach, error) {
	fer := &ForEachResource{}
	if err := json.Unmarshal(rawItem, fer); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal rawItem into ForEachResource")
	}

	return tm.JSONPath(target, fer.ForEach)
}

func (tm *TemplateManager) conditional(rawItem []byte, target map[string]interface{}) (bool, error) {
	conditional := &Conditionals{}
	if err := json.Unmarshal(rawItem, conditional); err != nil {
		return false, errors.Wrap(err, "failed to unmarshal rawItem into Conditionals")
	}

	if conditional.When == "" {
		return true, nil
	}

	return tm.GetBool(target, conditional.When)
}

func (tm *TemplateManager) getNamespaces(ctx context.Context, copyToNamespaces templatev1.CopyToNamespaces) ([]string, error) {
	namespaceMap := map[string]bool{}
	namespaces := []string{}

	for _, ns := range copyToNamespaces.Namespaces {
		namespaceMap[ns] = true
	}

	if copyToNamespaces.NamespaceSelector != nil {
		labelSelector, err := labelSelectorToString(*copyToNamespaces.NamespaceSelector)
		if err != nil {
			return nil, errors.Wrap(err, "failed to create label selector string")
		}
		options := metav1.ListOptions{
			LabelSelector: labelSelector,
		}
		namespaceList, err := tm.CoreV1().Namespaces().List(ctx, options)
		if err != nil {
			return nil, errors.Wrap(err, "failed to list namespaces with label selector")
		}

		for _, ns := range namespaceList.Items {
			namespaceMap[ns.Name] = true
		}
	}

	for ns := range namespaceMap {
		namespaces = append(namespaces, ns)
	}

	sort.Strings(namespaces)

	return namespaces, nil
}

func (tm *TemplateManager) JSONPath(object interface{}, jsonpath string) (*ForEach, error) {
	jsonpath = strings.TrimPrefix(jsonpath, "{{")
	jsonpath = strings.TrimSuffix(jsonpath, "}}")
	jsonpath = strings.TrimPrefix(jsonpath, ".")
	jsonObject, err := json.Marshal(object)
	if err != nil {
		return nil, errors.Wrap(err, "failed to marshal json")
	}

	value := gjson.Get(string(jsonObject), jsonpath)

	if !value.Exists() {
		return &ForEach{}, nil
	}

	if value.IsArray() {
		arrayValue := value.Array()
		array := make([]interface{}, len(arrayValue))
		for i := range value.Array() {
			array[i] = arrayValue[i].Value()
		}
		return &ForEach{IsArray: true, Array: array}, nil
	} else if value.IsObject() {
		mapValue := value.Map()
		object := make(map[string]interface{})
		for k, v := range mapValue {
			object[k] = v.Value()
		}
		return &ForEach{IsMap: true, Map: object}, nil
	}

	return nil, errors.Errorf("field %s is not map or array", jsonpath)
}

func (tm *TemplateManager) GetBool(object interface{}, jsonpath string) (bool, error) {
	jsonpath = strings.TrimPrefix(jsonpath, "{{")
	jsonpath = strings.TrimSuffix(jsonpath, "}}")
	jsonpath = strings.TrimPrefix(jsonpath, ".")
	jsonObject, err := json.Marshal(object)
	if err != nil {
		return false, errors.Wrap(err, "failed to marshal json")
	}

	value := gjson.Get(string(jsonObject), jsonpath)

	if !value.Exists() {
		return false, errors.Wrapf(err, "failed to find path %s", jsonpath)
	}

	return value.Bool(), nil
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
