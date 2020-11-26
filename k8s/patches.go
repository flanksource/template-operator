package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	osruntime "runtime"
	"strings"
	"text/template"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	"github.com/tidwall/gjson"
	fyaml "gopkg.in/flanksource/yaml.v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/kustomize"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/kustomize/pkg/fs"
	"sigs.k8s.io/kustomize/pkg/gvk"
	"sigs.k8s.io/kustomize/pkg/patch"
	"sigs.k8s.io/kustomize/pkg/types"
	"sigs.k8s.io/yaml"
)

type PatchType string

var (
	PatchTypeYaml PatchType = "yaml"
	PatchTypeJSON PatchType = "json"
)

type PatchApplier struct {
	Clientset     *kubernetes.Clientset
	Log           logr.Logger
	FuncMap       template.FuncMap
	SchemaManager *SchemaManager
}

func NewPatchApplier(clientset *kubernetes.Clientset, schemaManager *SchemaManager, log logr.Logger) (*PatchApplier, error) {
	p := &PatchApplier{
		Clientset:     clientset,
		Log:           log,
		SchemaManager: schemaManager,
	}

	p.FuncMap = template.FuncMap{
		"kget":     p.KGet,
		"jsonPath": p.JSONPath,
	}
	return p, nil
}

func (p *PatchApplier) Apply(resource *unstructured.Unstructured, patchStr string, patchType PatchType) (*unstructured.Unstructured, error) {
	// fmt.Printf("Template patch:\n%s\n====\n", patchStr)
	t, err := template.New("patch").Funcs(p.FuncMap).Parse(patchStr)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create template from patch")
	}

	var tpl bytes.Buffer
	var data = map[string]interface{}{
		"source": resource.Object,
	}
	if err := t.Execute(&tpl, data); err != nil {
		return nil, errors.Wrap(err, "failed to execute template")
	}

	// create an in memory fs to use for the kustomization
	memFS := fs.MakeFakeFS()

	fakeDir := "/"
	// for Windows we need this to be a drive because kustomize uses filepath.Abs()
	// which will add a drive letter if there is none. which drive letter is
	// unimportant as the path is on the fake filesystem anyhow
	if osruntime.GOOS == "windows" {
		fakeDir = `C:\`
	}

	// writes the resource to a file in the temp file system
	b, err := yaml.Marshal(resource.Object)
	if err != nil {
		return nil, errors.Wrap(err, "failed to marshal resource object")
	}
	name := "resource.yaml"
	memFS.WriteFile(filepath.Join(fakeDir, name), b) // nolint: errcheck

	kustomizationFile := &types.Kustomization{Resources: []string{name}}

	version := resource.GetAPIVersion()
	parts := strings.Split(version, "/")
	var apiVersion, apiGroup string
	if len(parts) == 1 {
		apiGroup = ""
		apiVersion = parts[0]
	} else {
		apiGroup = parts[0]
		apiVersion = parts[1]
	}
	groupVersionKind := schema.GroupVersionKind{Group: apiGroup, Version: apiVersion, Kind: resource.GetKind()}

	if patchType == PatchTypeYaml {
		finalPatch := map[string]interface{}{}
		templateBytes := tpl.Bytes()
		if err := fyaml.Unmarshal(templateBytes, &finalPatch); err != nil {
			return nil, errors.Wrap(err, "failed to unmarshal template yaml")
		}
		patchObject := &unstructured.Unstructured{Object: finalPatch}
		if patchObject.GetName() == "" {
			patchObject.SetName(resource.GetName())
		}
		if patchObject.GetNamespace() == "" {
			patchObject.SetNamespace(resource.GetNamespace())
		}

		if err := p.SchemaManager.DuckType(groupVersionKind, patchObject); err != nil {
			return nil, errors.Wrap(err, "failed to duck type object")
		}

		// writes strategic merge patches to files in the temp file system
		kustomizationFile.PatchesStrategicMerge = []patch.StrategicMerge{}
		b, err = yaml.Marshal(patchObject.Object)
		if err != nil {
			return nil, errors.Wrap(err, "failed to marshal patch object")
		}

		name = fmt.Sprintf("patch-0.yaml")
		memFS.WriteFile(filepath.Join(fakeDir, name), b) // nolint: errcheck
		kustomizationFile.PatchesStrategicMerge = []patch.StrategicMerge{patch.StrategicMerge(name)}

	} else if patchType == PatchTypeJSON {
		name = fmt.Sprintf("patch-0.json")
		templateBytes := tpl.Bytes()
		memFS.WriteFile(filepath.Join(fakeDir, name), templateBytes) // nolint: errcheck
		// writes json patches to files in the temp file system

		kustomizationFile.PatchesJson6902 = []patch.Json6902{
			{
				Target: &patch.Target{
					Gvk: gvk.Gvk{
						Group:   apiGroup,
						Version: apiVersion,
						Kind:    resource.GetKind(),
					},
					Name:      resource.GetName(),
					Namespace: resource.GetNamespace(),
				},
				Path: name,
			},
		}

	} else {
		return nil, errors.Errorf("Invalid patch type %s", patchType)
	}

	// writes the kustomization file to the temp file system
	kbytes, err := yaml.Marshal(kustomizationFile)
	if err != nil {
		return nil, errors.Wrap(err, "failed to marshal kustomization file")
	}
	memFS.WriteFile(filepath.Join(fakeDir, "kustomization.yaml"), kbytes) // nolint: errcheck

	// Finally customize the target resource
	var out bytes.Buffer
	if err := kustomize.RunKustomizeBuild(&out, memFS, fakeDir); err != nil {
		return nil, errors.Wrap(err, "failed to run kustomize build")
	}

	kustomizeBytes := out.Bytes()
	// fmt.Printf("Kustomize bytes: %s\n", kustomizeBytes)

	if err := yaml.Unmarshal(kustomizeBytes, &resource); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal kustomize output into resource")
	}

	return resource, nil
}

func (p *PatchApplier) KGet(path, jsonpath string) string {
	parts := strings.Split(path, "/")
	if len(parts) != 3 {
		p.Log.Error(errors.New("expected path to contain kind/namespace/name"), "invalid call to kget")
		return ""
	}

	kind := parts[0]
	namespace := parts[1]
	name := parts[2]

	if kind == "configmap" || kind == "cm" {
		cm, err := p.Clientset.CoreV1().ConfigMaps(namespace).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			p.Log.Error(err, "failed to read configmap", "name", name, "namespace", namespace)
			return ""
		}

		encodedJSON, err := json.Marshal(cm)
		if err != nil {
			p.Log.Error(err, "failed to encode json", "name", name, "namespace", namespace)
			return ""
		}
		value := gjson.Get(string(encodedJSON), jsonpath)
		return value.String()
	} else if kind == "secret" {
		secret, err := p.Clientset.CoreV1().Secrets(namespace).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			p.Log.Error(err, "failed to read secret", "name", name, "namespace", namespace)
			return ""
		}
		return string(secret.Data[jsonpath])
	}

	return ""
}

func (p *PatchApplier) JSONPath(object interface{}, jsonpath string) string {
	jsonObject, err := json.Marshal(object)
	if err != nil {
		p.Log.Error(err, "failed to encode json", "object", object)
		return ""
	}
	value := gjson.Get(string(jsonObject), jsonpath)
	return value.String()
}
