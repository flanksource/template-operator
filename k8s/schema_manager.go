package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/flanksource/commons/logger"
	"github.com/go-openapi/jsonpointer"
	"github.com/go-openapi/spec"
	"github.com/pkg/errors"
	extv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	extapi "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

type MapInterface map[string]interface{}

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

func (m *SchemaManager) DuckType(gvk schema.GroupVersionKind, object *unstructured.Unstructured) error {
	schema, found, err := m.FindSchemaForKind(gvk)
	if err != nil {
		return errors.Wrapf(err, "error finding kind %v", gvk)
	}
	if !found {
		return errors.Errorf("kind %v does not exist", gvk)
	}

	newObject, err := m.duckType(schema, object.Object, "")
	if err != nil {
		return errors.Wrap(err, "failed to duck type object")
	}
	object.Object = newObject.(map[string]interface{})
	return nil
}

func (m *SchemaManager) duckType(schema *spec.Schema, object interface{}, prefix string) (interface{}, error) {
	// fmt.Printf("Prefix: %s\n", prefix)

	v := reflect.ValueOf(object)
	switch v.Kind() {
	case reflect.Slice:
		bytes, ok := object.([]byte)
		if ok {
			fieldType, err := m.FindTypeForKeyFromSchema(schema, prefix)
			if err != nil {
				logger.Errorf("failed to find type for key %s: %v", prefix, err)
				return bytes, nil
			}
			return transformBytesToType(bytes, fieldType)
		}

		array := object.([]interface{})
		newArray := make([]interface{}, len(array))
		for i, e := range array {
			newPrefix := prefix + "." + strconv.Itoa(i)
			res, err := m.duckType(schema, e, newPrefix)
			if err != nil {
				return nil, err
			}
			newArray[i] = res
		}
		return newArray, nil
	case reflect.Map:
		oldMap := object.(map[string]interface{})
		newMap := make(map[string]interface{})
		for k, v := range oldMap {
			newPrefix := prefix + "." + escapeDot(k)
			if prefix == "" {
				newPrefix = escapeDot(k)
			}
			res, err := m.duckType(schema, v, newPrefix)
			if err != nil {
				return nil, err
			}
			newMap[k] = res
		}
		return newMap, nil
	case reflect.String:
		value := object.(string)
		fieldType, err := m.FindTypeForKeyFromSchema(schema, prefix)
		if err != nil {
			logger.Errorf("failed to find type for key %s: %v", prefix, err)
			return value, nil
		}
		newValue, err := transformStringToType(value, fieldType)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to transform string to type %v", fieldType)
		}
		return newValue, nil
	case reflect.Int, reflect.Int8, reflect.Int32, reflect.Int64:
		value := v.Int()
		fieldType, err := m.FindTypeForKeyFromSchema(schema, prefix)
		if err != nil {
			logger.Errorf("failed to find type for key %s: %v", prefix, err)
			return value, nil
		}
		newValue, err := transformInt64ToType(value, fieldType)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to transform string to type %v", fieldType)
		}
		return newValue, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint32, reflect.Uint64:
		value := v.Uint()
		fieldType, err := m.FindTypeForKeyFromSchema(schema, prefix)
		if err != nil {
			logger.Errorf("failed to find type for key %s: %v", prefix, err)
			return value, nil
		}
		newValue, err := transformUint64ToType(value, fieldType)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to transform string to type %v", fieldType)
		}
		return newValue, nil
	case reflect.Float32, reflect.Float64:
		value := int64(v.Float())
		fieldType, err := m.FindTypeForKeyFromSchema(schema, prefix)
		if err != nil {
			logger.Errorf("failed to find type for key %s: %v", prefix, err)
			return value, nil
		}
		newValue, err := transformInt64ToType(value, fieldType)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to transform string to type %v", fieldType)
		}
		return newValue, nil
	default:
		return nil, errors.Errorf("Value for field %s for type %T could not be transformed: %d", prefix, v, v.Kind())
	}
}

func (m *SchemaManager) FindTypeForKey(gvk schema.GroupVersionKind, key string) (*TypedField, error) {
	schema, found, err := m.FindSchemaForKind(gvk)
	if err != nil {
		return nil, errors.Wrapf(err, "error finding kind %v", gvk)
	}
	if !found {
		return nil, errors.Errorf("kind %v does not exiting", gvk)
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
		if schema.Type.Contains("object") && schema.AdditionalProperties != nil && schema.AdditionalProperties.Schema != nil {
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
	if crd.Spec.Validation == nil || crd.Spec.Validation.OpenAPIV3Schema == nil {
		return nil, errors.Errorf("crd %s is missing openapi schema validation", crd.Name)
	}

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

func transformStringToType(value string, fieldType *TypedField) (interface{}, error) {
	switch fieldType.Format {
	case "int8":
		return strconv.ParseInt(value, 10, 8)
	case "int32":
		return strconv.Atoi(value)
	case "int64":
		return strconv.ParseInt(value, 10, 64)
	case "uint8":
		return strconv.ParseUint(value, 10, 8)
	case "uint32":
		return strconv.ParseUint(value, 10, 32)
	case "uint64":
		return strconv.ParseUint(value, 10, 64)
	case "double":
		return strconv.ParseFloat(value, 64)
	case "byte":
		return []byte(value), nil
	}

	if contains(fieldType.Types, "string") {
		return value, nil
	}

	if contains(fieldType.Types, "integer") {
		return strconv.Atoi(value)
	}

	if contains(fieldType.Types, "boolean") {
		return strconv.ParseBool(value)
	}

	if contains(fieldType.Types, "object") {
		obj := map[string]interface{}{}
		err := json.Unmarshal([]byte(value), &obj)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to transform string value to object: %v", err)
		}
		return obj, err
	}

	return nil, errors.Errorf("could not transform string value to types %v format %s", fieldType.Types, fieldType.Format)
}

func transformFloatToType(value float64, fieldType *TypedField) (interface{}, error) {
	switch fieldType.Format {
	case "int8":
		return strconv.ParseInt(strconv.FormatInt(int64(value), 10), 10, 8)
	case "int32":
		return strconv.ParseInt(strconv.FormatInt(int64(value), 10), 10, 32)
	case "int64":
		return value, nil
	case "uint8":
		return strconv.ParseUint(strconv.FormatInt(int64(value), 10), 10, 8)
	case "uint32":
		return strconv.ParseUint(strconv.FormatInt(int64(value), 10), 10, 32)
	case "uint64":
		return strconv.ParseUint(strconv.FormatInt(int64(value), 10), 10, 64)
	}

	if contains(fieldType.Types, "integer") {
		return strconv.Atoi(strconv.FormatInt(int64(value), 10))
	}

	if contains(fieldType.Types, "string") {
		return strconv.FormatFloat(value, 'f', 6, 64), nil
	}

	if contains(fieldType.Types, "boolean") {
		return value != 0, nil
	}

	return nil, errors.Errorf("could not transform float64 value to types %v format %s", fieldType.Types, fieldType.Format)
}

func transformInt64ToType(value int64, fieldType *TypedField) (interface{}, error) {
	switch fieldType.Format {
	case "int8":
		return strconv.ParseInt(strconv.FormatInt(value, 10), 10, 8)
	case "int32":
		return strconv.ParseInt(strconv.FormatInt(value, 10), 10, 32)
	case "int64":
		return value, nil
	case "uint8":
		return strconv.ParseUint(strconv.FormatInt(value, 10), 10, 8)
	case "uint32":
		return strconv.ParseUint(strconv.FormatInt(value, 10), 10, 32)
	case "uint64":
		return strconv.ParseUint(strconv.FormatInt(value, 10), 10, 64)
	}

	if contains(fieldType.Types, "integer") {
		return strconv.Atoi(strconv.FormatInt(value, 10))
	}

	if contains(fieldType.Types, "string") {
		return strconv.FormatInt(value, 10), nil
	}

	if contains(fieldType.Types, "boolean") {
		return value != 0, nil
	}

	return nil, errors.Errorf("could not transform int64 value to types %v format %s", fieldType.Types, fieldType.Format)
}

func transformUint64ToType(value uint64, fieldType *TypedField) (interface{}, error) {
	switch fieldType.Format {
	case "int8":
		return strconv.ParseInt(strconv.FormatUint(value, 10), 10, 8)
	case "int32":
		return strconv.ParseInt(strconv.FormatUint(value, 10), 10, 32)
	case "int64":
		return strconv.ParseUint(strconv.FormatUint(value, 10), 10, 64)
	case "uint8":
		return strconv.ParseUint(strconv.FormatUint(value, 10), 10, 8)
	case "uint32":
		return strconv.ParseUint(strconv.FormatUint(value, 10), 10, 32)
	case "uint64":
		return value, nil
	}

	if contains(fieldType.Types, "integer") {
		return strconv.Atoi(strconv.FormatUint(value, 10))
	}

	if contains(fieldType.Types, "string") {
		return strconv.FormatUint(value, 10), nil
	}

	if contains(fieldType.Types, "boolean") {
		return value != 0, nil
	}

	return nil, errors.Errorf("could not transform uint64 value to types %v format %s", fieldType.Types, fieldType.Format)
}

func transformBytesToType(value []byte, fieldType *TypedField) (interface{}, error) {
	if fieldType.Format == "byte" {
		return value, nil
	}

	if contains(fieldType.Types, "string") {
		return string(value), nil
	}

	return nil, errors.Errorf("could not transform []byte value to types %v format %s", fieldType.Types, fieldType.Format)
}

func contains(slice []string, value string) bool {
	for _, k := range slice {
		if k == value {
			return true
		}
	}
	return false
}

func escapeDot(s string) string {
	return strings.ReplaceAll(s, ".", "_")
}
