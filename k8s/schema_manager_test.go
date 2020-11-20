package k8s_test

import (
	"io/ioutil"
	"os"
	"path"

	"github.com/flanksource/commons/logger"
	"github.com/flanksource/kommons"
	"github.com/flanksource/template-operator/k8s"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/pkg/errors"
	extapi "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1beta1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/homedir"
)

var _ = Describe("SchemaManager", func() {
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
})

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
