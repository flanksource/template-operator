package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/go-openapi/jsonpointer"
	"github.com/go-openapi/spec"
	"github.com/pkg/errors"
	extv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	extapi "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
)

type SchemaManager struct {
	swagger         *spec.Swagger
	serverResources []*metav1.APIResourceList
	clientset       *kubernetes.Clientset
	crdClient       extapi.ApiextensionsV1beta1Interface
}

type TypedField struct {
	Types  []string
	Format string
}

func NewSchemaManager(clientset *kubernetes.Clientset, crdClient extapi.ApiextensionsV1beta1Interface) (*SchemaManager, error) {
	bs, err := clientset.RESTClient().Get().AbsPath("openapi", "v2").DoRaw(context.TODO())
	if err != nil {
		return nil, err
	}
	s := &spec.Swagger{}

	if err := json.Unmarshal(bs, &s); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal openapi")
	}

	serverResources, err := clientset.ServerResources()
	if err != nil {
		return nil, errors.Wrap(err, "failed to list server resources")
	}

	mgr := &SchemaManager{
		swagger:         s,
		serverResources: serverResources,
		clientset:       clientset,
		crdClient:       crdClient,
	}
	return mgr, nil
}

func (m *SchemaManager) FindTypeForKey(gvk schema.GroupVersionKind, key string) (*TypedField, error) {
	schema, found, err := m.FindSchemaForKind(gvk)
	if err != nil {
		return nil, errors.Wrapf(err, "error finding kind %v", gvk)
	}
	if !found {
		return nil, errors.Errorf("kind %v not found", gvk)
	}

	return m.FindTypeForKeyFromSchema(schema, key)
}

func (m *SchemaManager) FindSchemaForKind(gvk schema.GroupVersionKind) (*spec.Schema, bool, error) {
	definition := m.getDefinitionName(gvk)

	if m.swagger.SwaggerProps.Definitions == nil {
		return nil, false, fmt.Errorf("unexpected empty definitions")
	}
	value, found := m.swagger.SwaggerProps.Definitions[definition]

	// CRD Types are present in server resources but the schema is empty
	if !found || value.Properties == nil || len(value.Properties) == 0 {
		return m.findSchemaForCrd(gvk)
	}

	return &value, found, nil
}

func (m *SchemaManager) FindTypeForKeyFromSchema(schema *spec.Schema, key string) (*TypedField, error) {
	fieldSchema, err := m.findTypeForKey(schema, key)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to find type for key %s", key)
	}

	typedField := &TypedField{Types: fieldSchema.Type, Format: fieldSchema.Format}
	return typedField, nil
}

func (m *SchemaManager) findTypeForKey(schema *spec.Schema, key string) (*spec.Schema, error) {
	parts := strings.SplitN(key, ".", 2)
	fieldName := parts[0]

	fieldSchema, found := schema.Properties[fieldName]
	if !found {
		if schema.Type.Contains("object") && schema.AdditionalProperties.Schema != nil {
			fieldSchema = *schema.AdditionalProperties.Schema
		} else {
			return nil, errors.Errorf("failed to find property %s", fieldName)
		}
	}

	if len(parts) == 1 {
		pointer := fieldSchema.Ref.GetPointer()
		if pointer != nil && len(pointer.DecodedTokens()) > 0 {
			f, err := m.resolvePointer(pointer)
			if err != nil {
				return nil, errors.Wrapf(err, "failed to resolve pointer %s", pointer)
			}
			return f, nil
		}
		return &fieldSchema, nil
	}

	nextKey := parts[1]
	var pointer *jsonpointer.Pointer

	// For array we need to remove index. Also schema is different.
	if fieldSchema.Type.Contains("array") {
		pointer = fieldSchema.Items.Schema.Ref.GetPointer()
		parts := strings.SplitN(nextKey, ".", 2)
		if len(parts) == 1 {
			return nil, errors.Errorf("found array index as last element of key")
		}
		if fieldSchema.Items.Schema != nil {
			fieldSchema = *fieldSchema.Items.Schema
		}
		nextKey = parts[1]
	} else {
		pointer = fieldSchema.Ref.GetPointer()
	}
	if pointer != nil && len(pointer.DecodedTokens()) > 0 {
		f, err := m.resolvePointer(pointer)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to resolve pointer %s", pointer)
		}
		fieldSchema = *f
	}

	return m.findTypeForKey(&fieldSchema, nextKey)
}

func (m *SchemaManager) findSchemaForCrd(gvk schema.GroupVersionKind) (*spec.Schema, bool, error) {
	crds, err := m.crdClient.CustomResourceDefinitions().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		fmt.Printf("ERROR: %v\n", err)
		return nil, false, errors.Wrap(err, "failed to list customresourcedefinitions")
	}

	for _, crd := range crds.Items {
		if crd.Spec.Group == gvk.Group && crd.Spec.Names.Kind == gvk.Kind && crd.Spec.Version == gvk.Version {
			schema, err := m.parseCrdSchema(crd)
			if err != nil {
				return nil, false, errors.Wrap(err, "failed to parse crd schema")
			}
			return schema, true, nil
		}
	}

	return nil, false, errors.Errorf("schema for group=%s version=%s kind=%s not found", gvk.Group, gvk.Version, gvk.Kind)
}

func (m *SchemaManager) parseCrdSchema(crd extv1beta1.CustomResourceDefinition) (*spec.Schema, error) {
	bytes, err := json.Marshal(crd.Spec.Validation.OpenAPIV3Schema)
	if err != nil {
		return nil, errors.Wrap(err, "failed to encode crd schema to json")
	}

	schema := &spec.Schema{}
	if err := json.Unmarshal(bytes, schema); err != nil {
		return nil, errors.Wrap(err, "failed to decode json into spec.Schema")
	}

	return schema, nil
}

func (m *SchemaManager) resolvePointer(pointer *jsonpointer.Pointer) (*spec.Schema, error) {
	tokens := pointer.DecodedTokens()
	if len(tokens) != 2 {
		return nil, errors.Errorf("could not resolve pointer: %v", tokens)
	}

	if tokens[0] != "definitions" {
		return nil, errors.Errorf("resolving pointer %v not supported", tokens)
	}

	def, found := m.swagger.Definitions[tokens[1]]
	if !found {
		return nil, errors.Errorf("definition %s not found", tokens[1])
	}

	return &def, nil
}

func (m *SchemaManager) getDefinitionName(gvk schema.GroupVersionKind) string {
	group := gvk.Group
	version := gvk.Version
	kind := gvk.Kind
	if group == "" {
		group = "core"
	}

	return fmt.Sprintf("io.k8s.api.%s.%s.%s", group, version, kind)
}
