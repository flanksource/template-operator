package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/go-openapi/jsonpointer"
	"github.com/go-openapi/spec"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
)

type SchemaManager struct {
	swagger *spec.Swagger
}

type TypedField struct {
	Types  []string
	Format string
}

func NewSchemaManager(clientset *kubernetes.Clientset) (*SchemaManager, error) {
	bs, err := clientset.RESTClient().Get().AbsPath("openapi", "v2").DoRaw(context.TODO())
	if err != nil {
		return nil, err
	}
	s := &spec.Swagger{}
	if err := json.Unmarshal(bs, &s); err != nil {
		return nil, err
	}

	mgr := &SchemaManager{swagger: s}
	return mgr, nil
}

func (m *SchemaManager) FindTypeForKey(gvk schema.GroupVersionKind, key string) (*TypedField, error) {
	schema, found, err := m.findSchemaForKind(gvk)
	if err != nil {
		return nil, errors.Wrapf(err, "error finding kind %v", gvk)
	}
	if !found {
		return nil, errors.Errorf("kind %v not found", gvk)
	}

	fieldType, err := m.findTypeForKey(schema, key)
	if err != nil {
		return nil, errors.Wrap(err, "failed to find type")
	}

	typedField := &TypedField{Types: fieldType.Type, Format: fieldType.Format}

	return typedField, nil
}

func (m *SchemaManager) findTypeForKey(schema *spec.Schema, key string) (*spec.Schema, error) {
	parts := strings.SplitN(key, ".", 2)
	fieldName := parts[0]

	fmt.Printf("key: %s\n", key)
	fmt.Printf("type: %s\n", schema.Type)

	for k, _ := range schema.Properties {
		fmt.Printf("Property: %s\n", k)
	}
	fmt.Println("=====")

	fieldSchema, found := schema.Properties[fieldName]
	if !found {
		return nil, errors.Errorf("failed to find property %s", fieldName)
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

func (m *SchemaManager) findSchemaForKind(gvk schema.GroupVersionKind) (*spec.Schema, bool, error) {
	group := gvk.Group
	version := gvk.Version
	kind := gvk.Kind
	if group == "" {
		group = "core"
	}

	definition := fmt.Sprintf("io.k8s.api.%s.%s.%s", group, version, kind)
	if m.swagger.SwaggerProps.Definitions == nil {
		return nil, false, fmt.Errorf("unexpected empty definitions")
	}
	value, ok := m.swagger.SwaggerProps.Definitions[definition]
	return &value, ok, nil
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
