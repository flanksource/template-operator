package k8s_test

import (
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"reflect"
	"strings"

	"github.com/flanksource/commons/logger"
	"github.com/flanksource/kommons"
	"github.com/flanksource/template-operator/k8s"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/pkg/errors"
	extapi "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1beta1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/homedir"
	"sigs.k8s.io/yaml"
)

var _ = Describe("SchemaManager", func() {
	Describe("FindTypeForKey", func() {
		It("Returns int32 for Deployment. pec.replicas", func() {
			schemaManager := newSchemaManager()
			gvk := schema.GroupVersionKind{Group: "apps", Version: "v1beta1", Kind: "Deployment"}
			kind, err := schemaManager.FindTypeForKey(gvk, "spec.replicas")
			Expect(err).To(BeNil())
			Expect(*kind).To(Equal(k8s.TypedField{Types: []string{"integer"}, Format: "int32"}))
		})

		It("Returns string for Pod spec.dnsPolicy", func() {
			schemaManager := newSchemaManager()
			gvk := schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"}
			kind, err := schemaManager.FindTypeForKey(gvk, "spec.dnsPolicy")
			Expect(err).To(BeNil())
			Expect(*kind).To(Equal(k8s.TypedField{Types: []string{"string"}, Format: ""}))
		})

		It("Returns boolean for Pod spec.enableServiceLinks", func() {
			schemaManager := newSchemaManager()
			gvk := schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"}
			kind, err := schemaManager.FindTypeForKey(gvk, "spec.enableServiceLinks")
			Expect(err).To(BeNil())
			Expect(*kind).To(Equal(k8s.TypedField{Types: []string{"boolean"}, Format: ""}))
		})

		It("Returns string for Ingress spec.rules.0.host", func() {
			schemaManager := newSchemaManager()
			gvk := schema.GroupVersionKind{Group: "extensions", Version: "v1beta1", Kind: "Ingress"}
			kind, err := schemaManager.FindTypeForKey(gvk, "spec.rules.0.host")
			Expect(err).To(BeNil())
			Expect(*kind).To(Equal(k8s.TypedField{Types: []string{"string"}, Format: ""}))
		})

		It("Returns string for Ingress spec.rules.0.http.paths.0.backend.serviceName", func() {
			schemaManager := newSchemaManager()
			gvk := schema.GroupVersionKind{Group: "extensions", Version: "v1beta1", Kind: "Ingress"}
			kind, err := schemaManager.FindTypeForKey(gvk, "spec.rules.0.http.paths.0.backend.serviceName")
			Expect(err).To(BeNil())
			Expect(*kind).To(Equal(k8s.TypedField{Types: []string{"string"}, Format: ""}))
		})

		It("Returns int32 for Ingress spec.rules.0.http.paths.0.backend.servicePort", func() {
			schemaManager := newSchemaManager()
			gvk := schema.GroupVersionKind{Group: "extensions", Version: "v1beta1", Kind: "Ingress"}
			kind, err := schemaManager.FindTypeForKey(gvk, "spec.rules.0.http.paths.0.backend.servicePort")
			Expect(err).To(BeNil())
			Expect(*kind).To(Equal(k8s.TypedField{Types: []string{"string"}, Format: "int-or-string"}))
		})

		It("Returns integer for PostgresqlDB spec.replicas", func() {
			schemaManager := newSchemaManager()
			gvk := schema.GroupVersionKind{Group: "db.flanksource.com", Version: "v1", Kind: "PostgresqlDB"}
			kind, err := schemaManager.FindTypeForKey(gvk, "spec.replicas")
			Expect(err).To(BeNil())
			Expect(*kind).To(Equal(k8s.TypedField{Types: []string{"integer"}, Format: ""}))
		})

		It("Returns integer/int64 for Canary spec.interval", func() {
			schemaManager := newSchemaManager()
			gvk := schema.GroupVersionKind{Group: "canaries.flanksource.com", Version: "v1", Kind: "Canary"}
			kind, err := schemaManager.FindTypeForKey(gvk, "spec.interval")
			Expect(err).To(BeNil())
			Expect(*kind).To(Equal(k8s.TypedField{Types: []string{"integer"}, Format: "int64"}))
		})

		It("Returns integer/int64 for Canary spec.docker.0.expectedSize", func() {
			schemaManager := newSchemaManager()
			gvk := schema.GroupVersionKind{Group: "canaries.flanksource.com", Version: "v1", Kind: "Canary"}
			kind, err := schemaManager.FindTypeForKey(gvk, "spec.docker.0.expectedSize")
			Expect(err).To(BeNil())
			Expect(*kind).To(Equal(k8s.TypedField{Types: []string{"integer"}, Format: "int64"}))
		})

		It("Returns string for Secret stringData.DATABASE_PORT", func() {
			schemaManager := newSchemaManager()
			gvk := schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Secret"}
			kind, err := schemaManager.FindTypeForKey(gvk, "stringData.DATABASE_PORT")
			Expect(err).To(BeNil())
			Expect(*kind).To(Equal(k8s.TypedField{Types: []string{"string"}, Format: ""}))
		})

		It("Returns string/byte for Secret data.PASSWORD", func() {
			schemaManager := newSchemaManager()
			gvk := schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Secret"}
			kind, err := schemaManager.FindTypeForKey(gvk, "data.PASSWORD")
			Expect(err).To(BeNil())
			Expect(*kind).To(Equal(k8s.TypedField{Types: []string{"string"}, Format: "byte"}))
		})

		It("Returns map for PostgresqlDB spec.parameters", func() {
			schemaManager := newSchemaManager()
			gvk := schema.GroupVersionKind{Group: "db.flanksource.com", Version: "v1", Kind: "PostgresqlDB"}
			kind, err := schemaManager.FindTypeForKey(gvk, "spec.parameters")
			Expect(err).To(BeNil())
			Expect(*kind).To(Equal(k8s.TypedField{Types: []string{"object"}, Format: ""}))
		})
	})

	Describe("DuckType", func() {
		It("Encodes secret data if int correctly", func() {
			value := `
apiVersion: v1
kind: Secret
metadata:
  name: foo
  namespace: bar
stringData:
  Host: "localhost"
  Port: 8080
`
			resource, err := duckTypeWithValue(value)
			Expect(err).To(BeNil())

			data, ok := resource.Object["stringData"].(map[string]interface{})
			Expect(ok).To(Equal(true))

			Expect(reflect.TypeOf(data["Host"]).Kind()).To(Equal(reflect.String))
			Expect(reflect.TypeOf(data["Port"]).Kind()).To(Equal(reflect.String))
		})

		It("Encodes Deployment replicas to int correctly", func() {
			value := `
apiVersion: apps/v1beta1
kind: Deployment
metadata:
  name: foo
  namespace: bar
spec:
  replicas: "3"
`
			resource, err := duckTypeWithValue(value)
			Expect(err).ToNot(HaveOccurred())

			expectedYaml := `
apiVersion: apps/v1beta1
kind: Deployment
metadata:
  name: foo
  namespace: bar
spec:
  replicas: 3      
`
			yml, err := yaml.Marshal(resource.Object)
			Expect(err).ToNot(HaveOccurred())

			// fmt.Printf("Expected:\n%v\n=======Actual:\n%v\n==========", expectedYaml, string(yml))
			Expect(strings.TrimSpace(string(yml))).To(Equal(strings.TrimSpace(expectedYaml)))
		})

		It("Encodes PostgresqlDB spec.parameters to map[string]interface{} correctly", func() {
			value := `
apiVersion: acid.zalan.do/v1
kind: postgresql
metadata:
  name: test
  namespace: postgres-operator
spec:
  replicas: 2
  postgresql:
    parameters: "{\"max_connections\":\"1024\",\"shared_buffers\":\"4759MB\",\"work_mem\":\"475MB\",\"maintenance_work_mem\":\"634M\"}"
`
			resource, err := duckTypeWithValue(value)
			Expect(err).ToNot(HaveOccurred())

			expectedYaml := `
apiVersion: acid.zalan.do/v1
kind: postgresql
metadata:
  name: test
  namespace: postgres-operator
spec:
  postgresql:
    parameters:
      maintenance_work_mem: 634M
      max_connections: "1024"
      shared_buffers: 4759MB
      work_mem: 475MB
  replicas: 2
`
			yml, err := yaml.Marshal(resource.Object)
			Expect(err).ToNot(HaveOccurred())

			// fmt.Printf("Expected:\n%v\n=======Actual:\n%v\n==========", expectedYaml, string(yml))
			Expect(strings.TrimSpace(string(yml))).To(Equal(strings.TrimSpace(expectedYaml)))
		})
	})
})

func duckTypeWithValue(template string) (*unstructured.Unstructured, error) {
	resource := &unstructured.Unstructured{}
	if err := yaml.Unmarshal([]byte(template), &resource.Object); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal yaml")
	}

	fmt.Printf("object:\n%v\n", resource.Object)

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

	mgr := newSchemaManager()
	err := mgr.DuckType(groupVersionKind, resource)
	return resource, err
}

func newSchemaManager() *k8s.SchemaManager {
	sm, err := k8s.NewSchemaManager(clientset(), crdClient())
	if err != nil {
		logger.Fatalf("failed to get schema manager: %v", err)
	}
	return sm
}

func clientset() *kubernetes.Clientset {
	client := kommonsClient()
	clientset, err := client.GetClientset()
	if err != nil {
		logger.Fatalf("failed to get clientset: %v", err)
	}
	return clientset
}

func crdClient() extapi.ApiextensionsV1beta1Interface {
	client := kommonsClient()
	restConfig, err := client.GetRESTConfig()
	if err != nil {
		logger.Fatalf("failed to get rest config: %v", err)
	}
	crdClient, err := extapi.NewForConfig(restConfig)
	if err != nil {
		logger.Fatalf("failed to get extapi client: %v", err)
	}
	return crdClient
}

func kommonsClient() *kommons.Client {
	bytes, err := getKubeConfig()
	if err != nil {
		logger.Fatalf("failed to get kubeconfig: %v", err)
	}
	client, err := kommons.NewClientFromBytes(bytes)
	if err != nil {
		logger.Fatalf("failed to create kommons.Client: %v", err)
	}
	return client
}

func getKubeConfig() ([]byte, error) {
	if env := os.Getenv("KUBECONFIG"); env != "" {
		return ioutil.ReadFile(env)
	}

	if home := homedir.HomeDir(); home != "" {
		return ioutil.ReadFile(path.Join(home, ".kube", "config"))
	}

	return nil, errors.Errorf("failed to find kube config")
}
