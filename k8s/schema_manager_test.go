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
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/homedir"
)

var _ = Describe("SchemaManager", func() {
	It("Returns int32 for Deployment. pec.replicas", func() {
		schemaManager, err := k8s.NewSchemaManager(clientset())
		Expect(err).To(BeNil())
		gvk := schema.GroupVersionKind{Group: "apps", Version: "v1beta1", Kind: "Deployment"}
		kind, err := schemaManager.FindTypeForKey(gvk, "spec.replicas")
		Expect(err).To(BeNil())
		Expect(*kind).To(Equal(k8s.TypedField{Types: []string{"integer"}, Format: "int32"}))
	})

	It("Returns string for Pod spec.dnsPolicy", func() {
		schemaManager, err := k8s.NewSchemaManager(clientset())
		Expect(err).To(BeNil())
		gvk := schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"}
		kind, err := schemaManager.FindTypeForKey(gvk, "spec.dnsPolicy")
		Expect(err).To(BeNil())
		Expect(*kind).To(Equal(k8s.TypedField{Types: []string{"string"}, Format: ""}))
	})

	It("Returns boolean for Pod spec.enableServiceLinks", func() {
		schemaManager, err := k8s.NewSchemaManager(clientset())
		Expect(err).To(BeNil())
		gvk := schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"}
		kind, err := schemaManager.FindTypeForKey(gvk, "spec.enableServiceLinks")
		Expect(err).To(BeNil())
		Expect(*kind).To(Equal(k8s.TypedField{Types: []string{"boolean"}, Format: ""}))
	})

	It("Returns string for Ingress spec.rules.0.host", func() {
		schemaManager, err := k8s.NewSchemaManager(clientset())
		Expect(err).To(BeNil())
		gvk := schema.GroupVersionKind{Group: "extensions", Version: "v1beta1", Kind: "Ingress"}
		kind, err := schemaManager.FindTypeForKey(gvk, "spec.rules.0.host")
		Expect(err).To(BeNil())
		Expect(*kind).To(Equal(k8s.TypedField{Types: []string{"string"}, Format: ""}))
	})

	It("Returns string for Ingress spec.rules.0.http.paths.0.backend.serviceName", func() {
		schemaManager, err := k8s.NewSchemaManager(clientset())
		Expect(err).To(BeNil())
		gvk := schema.GroupVersionKind{Group: "extensions", Version: "v1beta1", Kind: "Ingress"}
		kind, err := schemaManager.FindTypeForKey(gvk, "spec.rules.0.http.paths.0.backend.serviceName")
		Expect(err).To(BeNil())
		Expect(*kind).To(Equal(k8s.TypedField{Types: []string{"string"}, Format: ""}))
	})

	It("Returns int32 for Ingress spec.rules.0.http.paths.0.backend.servicePort", func() {
		schemaManager, err := k8s.NewSchemaManager(clientset())
		Expect(err).To(BeNil())
		gvk := schema.GroupVersionKind{Group: "extensions", Version: "v1beta1", Kind: "Ingress"}
		kind, err := schemaManager.FindTypeForKey(gvk, "spec.rules.0.http.paths.0.backend.servicePort")
		Expect(err).To(BeNil())
		Expect(*kind).To(Equal(k8s.TypedField{Types: []string{"string"}, Format: "int-or-string"}))
	})
})

func clientset() *kubernetes.Clientset {
	client := kommonsClient()
	clientset, err := client.GetClientset()
	if err != nil {
		logger.Fatalf("failed to get clientset: %v", err)
	}
	return clientset
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
