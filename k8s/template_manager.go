package k8s

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/flanksource/kommons"
	"github.com/flanksource/kommons/ktemplate"
	templatev1 "github.com/flanksource/template-operator/api/v1"
	"github.com/go-logr/logr"
	"github.com/gobwas/glob"
	"github.com/pkg/errors"
	"github.com/tidwall/gjson"
	v1 "k8s.io/api/core/v1"
	extapi "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
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
	SchemaCache   *SchemaCache
	FuncMap       template.FuncMap
	Events        record.EventRecorder
	Watcher       WatcherInterface
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

func NewTemplateManager(c *kommons.Client, log logr.Logger, cache *SchemaCache, events record.EventRecorder, watcher WatcherInterface) (*TemplateManager, error) {
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
		SchemaCache:   cache,
		Watcher:       watcher,
		FuncMap:       functions.FuncMap(),
	}
	return tm, nil
}

func (tm *TemplateManager) GetSourceNamespaces(ctx context.Context, template *templatev1.Template) ([]string, error) {
	selector := template.Spec.Source

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

	return namespaceNames, nil
}

func (tm *TemplateManager) selectResources(ctx context.Context, template *templatev1.Template, cb CallbackFunc) ([]unstructured.Unstructured, error) {
	selector := template.Spec.Source

	if selector.Kind == "" || selector.APIVersion == "" {
		return nil, errors.New("must specify a kind and apiVersion")
	}
	var sources []unstructured.Unstructured

	namespaceNames, err := tm.GetSourceNamespaces(ctx, template)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get namespaces")
	}

	// first iterate over selected namespaces
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

	if len(sources) > 0 {
		obj := sources[0]
		tm.Watcher.Watch(&obj, template, cb)
	}

	return sources, nil
}

func (tm *TemplateManager) Run(ctx context.Context, template *templatev1.Template, cb CallbackFunc) (result ctrl.Result, err error) {
	tm.Log.Info("Reconciling", "template", template.Name)
	if template.Spec.Source.GitRepository != nil {
		result, err := tm.handleGitRepository(ctx, template)
		if err != nil {
			return result, err
		}
		tm.Log.V(3).Info("Reconcile Complete", "template", template.Name)
		return result, nil
	}

	sources, err := tm.selectResources(ctx, template, cb)
	if err != nil {
		return
	}
	tm.Log.Info("Found resources for template", "template", template.Name, "count", len(sources))

	for _, source := range sources {
		result, err = tm.HandleSource(ctx, template, source)
		if err != nil {
			return result, err
		}
	}

	tm.Log.V(3).Info("Reconcile Complete", "template", template.Name)
	return
}

func (tm *TemplateManager) HandleSource(ctx context.Context, template *templatev1.Template, source unstructured.Unstructured) (result ctrl.Result, err error) {
	target := &source

	if !template.Spec.Onceoff || !alreadyApplied(template, *target) {
		for _, patch := range template.Spec.Patches {
			target, err = tm.PatchApplier.Apply(target, patch, PatchTypeYaml)
			if err != nil {
				tm.Events.Eventf(&source, v1.EventTypeWarning, "Failed", "Failed to apply patch")
				return
			}
		}
		for _, patch := range template.Spec.JsonPatches {
			target, err = tm.PatchApplier.Apply(target, patch.Patch, PatchTypeJSON)
			if err != nil {
				tm.Events.Eventf(&source, v1.EventTypeWarning, "Failed", "Failed to apply patch")
				return
			}
		}
		if len(template.Spec.JsonPatches) > 0 || len(template.Spec.Patches) > 0 {
			target = markApplied(template, target)
			stripAnnotations(target)
			if err := tm.Client.ApplyUnstructured(source.GetNamespace(), target); err != nil {
				tm.Events.Eventf(&source, v1.EventTypeWarning, "Failed", "Failed to apply object")
				return result, err
			}
		}
	}

	isSourceReady := true

	objs, err := tm.getObjectsFromResources(template.Spec.Resources, *target)
	if err != nil {
		return result, err
	}
	tobjs, err := tm.getObjectsFromResourcesTemplate(template.Spec.ResourcesTemplate, *target)
	if err != nil {
		return result, err
	}
	objs = append(objs, tobjs...)

	bytes := []byte{}
	for _, o := range objs {
		b, _ := yaml.Marshal(o.Object)
		bytes = append(bytes, b...)
		bytes = append(bytes, []byte("\n---\n")...)
	}

	for _, obj := range objs {
		ready, msg, err, rslt := tm.checkDependentObjects(&obj, objs)
		if err != nil {
			tm.Events.Eventf(&source, v1.EventTypeWarning, "Failed", "Failed to check dependent objects")
			return result, err
		}
		if !ready {
			result = rslt
			tm.Log.V(2).Info("Skipping object", "kind", obj.GetKind(), "namespace", obj.GetNamespace(), "name", obj.GetName(), "obj", obj)
			tm.Log.V(2).Info("Dependent object not ready", "message", msg)
			continue
		}

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
			tm.Events.Eventf(&source, v1.EventTypeWarning, "Failed", "Failed to apply new resource kind=%s name=%s err=%v", obj.GetKind(), obj.GetName(), err)
			return result, err
		}

		if isReady, msg, err := tm.isResourceReady(&obj); err != nil {
			return result, errors.Wrap(err, "failed to check if resource is ready")
		} else if !isReady {
			tm.Log.V(2).Info("resource is not ready", "kind", obj.GetKind(), "name", obj.GetName(), "namespace", obj.GetNamespace(), "message", msg)
			isSourceReady = false
		} else {
			tm.Log.V(2).Info("resource is ready", "kind", obj.GetKind(), "name", obj.GetName(), "namespace", obj.GetNamespace(), "message", msg)
		}
	}

	if template.Spec.CopyToNamespaces != nil {
		namespaces, err := tm.getNamespaces(ctx, *template.Spec.CopyToNamespaces)
		if err != nil {
			tm.Events.Eventf(&source, v1.EventTypeWarning, "Failed", "Failed to get namespaces")
			return result, errors.Wrap(err, "failed to get namespaces")
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
				return result, err
			}

			if isReady, msg, err := tm.isResourceReady(newResource); err != nil {
				return result, errors.Wrap(err, "failed to check if resource is ready")
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

	return
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
	return tm.duckTypeTemplateResultObject(obj)
}

func (tm *TemplateManager) duckTypeTemplateResultObject(obj *unstructured.Unstructured) ([]byte, error) {
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

	tpl, err := template.New("").Funcs(tm.FuncMap).Parse(conditional.When)
	if err != nil {
		return false, fmt.Errorf("invalid template %s: %v", conditional.When, err)
	}

	var buf bytes.Buffer
	if err := tpl.Execute(&buf, target); err != nil {
		return false, fmt.Errorf("error executing template %s: %v", conditional.When, err)
	}

	return strconv.ParseBool(buf.String())
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

func (tm *TemplateManager) handleGitRepository(ctx context.Context, template *templatev1.Template) (result ctrl.Result, err error) {
	source := template.Spec.Source.GitRepository

	gitRepository, err := tm.getGitRepository(ctx, source.Name, source.Namespace)
	if err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to get git repository")
	}

	status, ok := gitRepository.Object["status"].(map[string]interface{})
	if !ok {
		return ctrl.Result{}, errors.Errorf("failed to convert gitRepository.status to map")
	}

	artifact, ok := status["artifact"].(map[string]interface{})
	if !ok {
		return ctrl.Result{}, errors.Errorf("failed to get gitRepository.status.artifact")
	}

	url, found := artifact["url"].(string)
	if !found {
		return ctrl.Result{}, errors.Errorf("could not find url in gitRepository.status.artifact")
	}

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: tr}
	resp, err := client.Get(url)
	if resp != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		return ctrl.Result{}, errors.Errorf("failed to download gitRepository archive")
	}

	files, err := tm.getGitRepositoryFiles(ctx, source, resp.Body)
	if err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to get gitRepository files")
	}

	for filename, content := range files {
		unstructuredTemplate, err := kommons.ToUnstructured(&unstructured.Unstructured{}, template)
		if err != nil {
			return ctrl.Result{}, errors.Wrap(err, "failed to convert template to unstructured")
		}
		unstructuredTemplate.Object["filename"] = filename
		unstructuredTemplate.Object["content"] = content

		result, err := tm.HandleSource(ctx, template, *unstructuredTemplate)
		if err != nil {
			return result, err
		}
	}

	return ctrl.Result{}, nil
}

func (tm *TemplateManager) getGitRepositoryFiles(ctx context.Context, source *templatev1.GitRepository, archive io.ReadCloser) (map[string]string, error) {
	files := map[string]string{}
	g, err := glob.Compile(source.Glob, '/')
	if err != nil {
		return nil, errors.Wrapf(err, "failed to compile glob pattern %s", source.Glob)
	}

	gzf, err := gzip.NewReader(archive)
	if err != nil {
		return nil, errors.Wrap(err, "failed to read gzip archive")
	}

	tarReader := tar.NewReader(gzf)
	i := 0
	for {
		header, err := tarReader.Next()

		if err == io.EOF {
			break
		}

		if err != nil {
			return nil, errors.Wrap(err, "failed to advance to next file in tar archive")
		}

		// name := "/" + header.Name
		name := header.Name

		switch header.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg:
			if g.Match("/" + name) {
				data, err := io.ReadAll(tarReader)
				if err != nil {
					return nil, errors.Wrapf(err, "failed to read file %s from tar archive", name)
				}
				files[name] = string(data)
			}
		default:
			return nil, errors.Wrapf(err, "failed to find type of file %s", name)
		}

		i++
	}

	return files, nil
}

func (tm *TemplateManager) getGitRepository(ctx context.Context, name, namespace string) (*unstructured.Unstructured, error) {
	dynamicClient, err := tm.Client.GetDynamicClient()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get dynamic client")
	}
	rm, _ := tm.Client.GetRestMapper()
	gvk, err := rm.KindFor(schema.GroupVersionResource{
		Group:    "source.toolkit.fluxcd.io",
		Version:  "v1beta1",
		Resource: "GitRepository",
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to get kind for GitRepository")
	}
	gk := schema.GroupKind{Group: gvk.Group, Kind: gvk.Kind}
	mapping, err := rm.RESTMapping(gk, gvk.Version)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get rest mapping for GitRepository")
	}
	resourceInterface, err := dynamicClient.Resource(mapping.Resource), nil
	if err != nil {
		return nil, errors.Wrap(err, "failed to get dynamic resource interface")
	}

	return resourceInterface.Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
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

// Returns object from the list of objects if the id is found and nil in case id is not found
func getObjectFromId(id string, objs []unstructured.Unstructured) (*unstructured.Unstructured, error) {
	for _, obj := range objs {
		if obj.Object["id"] != nil {
			if obj.Object["id"].(string) == id {
				return &obj, nil
			}
		}
	}
	return nil, fmt.Errorf("No object found with id: %v", id)
}

// Returns []string with IDs in case a object depends on any other object
func getDependsOnIds(obj *unstructured.Unstructured) []string {
	if obj.Object["depends"] != nil {
		depends := obj.Object["depends"].([]interface{})
		s := make([]string, len(depends))
		for i, v := range depends {
			s[i] = fmt.Sprint(v)
		}
		return s
	}
	return nil
}

// checks if all the dependent obj are ready
func (tm *TemplateManager) checkDependentObjects(obj *unstructured.Unstructured, objs []unstructured.Unstructured) (bool, string, error, ctrl.Result) {
	if obj.Object["depends"] != nil {
		ids := getDependsOnIds(obj)
		for _, id := range ids {
			dependObj, err := getObjectFromId(id, objs)
			if err != nil {
				return false, "", err, ctrl.Result{}
			}
			if ready, msg, err := tm.isResourceReady(dependObj); !ready || err != nil {
				return false, msg, err, ctrl.Result{Requeue: true, RequeueAfter: 2 * time.Minute}
			}
		}
	}
	return true, "object is ready", nil, ctrl.Result{}
}

func (tm *TemplateManager) getObjectsFromResources(resources []runtime.RawExtension, target unstructured.Unstructured) ([]unstructured.Unstructured, error) {
	var objs []unstructured.Unstructured
	for _, item := range resources {
		obj, err := tm.getObjects(item.Raw, target.Object)
		if err != nil {
			return nil, err
		}
		for _, ob := range obj {
			objs = append(objs, *ob)
		}
	}
	return objs, nil
}

func (tm *TemplateManager) getObjectsFromResourcesTemplate(template string, target unstructured.Unstructured) ([]unstructured.Unstructured, error) {
	var objs []unstructured.Unstructured

	targetCopy := target.DeepCopy()

	data, err := tm.processTemplate([]byte(template), targetCopy)
	if err != nil {
		return nil, errors.Wrap(err, "failed to template resources")
	}
	uobjs, err := kommons.GetUnstructuredObjects(data)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get unstructured objects")
	}

	for _, u := range uobjs {
		objs = append(objs, *u)
	}

	return objs, nil
}

func (tm *TemplateManager) processTemplate(data []byte, vars interface{}) ([]byte, error) {
	tpl, err := template.New("").Funcs(tm.FuncMap).Parse(string(data))

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

	objs, err := kommons.GetUnstructuredObjects(buf.Bytes())
	if err != nil {
		return nil, errors.Wrap(err, "failed to get unstructured objects")
	}

	fmt.Printf("Got objects count=%d\n", len(objs))

	result := ""
	for _, o := range objs {
		tm.Log.Info("Duck typing object", "kind", o.GetKind(), "name", o.GetName())
		duckTyped, err := tm.duckTypeTemplateResultObject(o)
		if err != nil {
			return nil, errors.Wrap(err, "failed to duck type object")
		}
		result += string(duckTyped) + "\n---\n"
	}

	return []byte(result), nil
}
