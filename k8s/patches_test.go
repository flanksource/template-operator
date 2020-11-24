package k8s_test

import (
	"fmt"
	"strings"

	"github.com/flanksource/template-operator/k8s"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/yaml"
)

var _ = Describe("Patches", func() {
	It("Merges json patch Ingress", func() {
		resource := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"kind":       "Ingress",
				"apiVersion": "extensions/v1beta1",
				"metadata": map[string]interface{}{
					"name":      "podinfo",
					"namespace": "example",
				},
				"spec": map[string]interface{}{
					"rules": []map[string]interface{}{
						{
							"host": "pod-info",
							"http": map[string]interface{}{
								"paths": []map[string]interface{}{
									{
										"backend": map[string]interface{}{
											"serviceName": "podinfo",
											"servicePort": 9898,
										},
									},
								},
							},
						},
					},
					"tls": []map[string]interface{}{
						{
							"hosts": []string{
								"pod-info",
							},
							"secretName": "podinfo-tls",
						},
					},
				},
			},
		}

		patch := `
[
  {
    "op": "replace",
    "path": "/spec/rules/0/host",
		"value": "{{ jsonPath .source "spec.rules.0.host" }}.{{- kget "cm/quack/quack-config" "data.domain" -}}"
  },
  {
    "op": "replace",
    "path": "/spec/tls/0/hosts/0",
		"value": "{{ jsonPath .source "spec.tls.0.hosts.0" }}.{{- kget "cm/quack/quack-config" "data.domain" -}}"
  }
]
`
		log := ctrl.Log.WithName("test")
		patchApplier, err := k8s.NewPatchApplier(clientset(), crdClient(), log)
		Expect(err).ToNot(HaveOccurred())
		patchApplier.FuncMap["kget"] = func(path, jsonPath string) string {
			return "1.2.3.4.nip.io"
		}

		newResource, err := patchApplier.Apply(resource, patch, k8s.PatchTypeJSON)
		Expect(err).To(BeNil())

		specYaml, err := yaml.Marshal(newResource.Object)
		Expect(err).To(BeNil())

		foundYaml := strings.TrimSpace(string(specYaml))

		expectedYaml := strings.TrimSpace(`
apiVersion: extensions/v1beta1
kind: Ingress
metadata:
  name: podinfo
  namespace: example
spec:
  rules:
  - host: pod-info.1.2.3.4.nip.io
    http:
      paths:
      - backend:
          serviceName: podinfo
          servicePort: 9898
  tls:
  - hosts:
    - pod-info.1.2.3.4.nip.io
    secretName: podinfo-tls
`)
		// fmt.Printf("Found:\n%s\n", foundYaml)
		// fmt.Printf("Expected:\n%s\n", expectedYaml)
		Expect(foundYaml).To(Equal(expectedYaml))
	})

	It("Merges json patch Service", func() {
		resource := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"kind":       "Service",
				"apiVersion": "v1",
				"metadata": map[string]interface{}{
					"name":      "podinfo",
					"namespace": "example",
				},
				"spec": map[string]interface{}{
					"ports": []interface{}{
						map[string]interface{}{
							"protocol":   "TCP",
							"port":       "80",
							"targetPort": "9376",
						},
					},
				},
			},
		}

		patch := `
[
  {
    "op": "replace",
    "path": "/spec/ports/0/port",
    "value": 443
  }
]
`
		log := ctrl.Log.WithName("test")
		patchApplier, err := k8s.NewPatchApplier(clientset(), crdClient(), log)
		Expect(err).ToNot(HaveOccurred())
		patchApplier.FuncMap["kget"] = func(path, jsonPath string) string {
			return "1.2.3.4.nip.io"
		}

		newResource, err := patchApplier.Apply(resource, patch, k8s.PatchTypeJSON)
		Expect(err).ToNot(HaveOccurred())

		specYaml, err := yaml.Marshal(newResource.Object)
		Expect(err).ToNot(HaveOccurred())

		foundYaml := strings.TrimSpace(string(specYaml))

		expectedYaml := strings.TrimSpace(`
apiVersion: v1
kind: Service
metadata:
  name: podinfo
  namespace: example
spec:
  ports:
  - port: 443
    protocol: TCP
    targetPort: "9376"
`)
		// fmt.Printf("Found:\n%s\n", foundYaml)
		// fmt.Printf("Expected:\n%s\n", expectedYaml)
		Expect(foundYaml).To(Equal(expectedYaml))
	})

	It("Merges annotations and labels", func() {
		resource := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"kind":       "Ingress",
				"apiVersion": "extensions/v1beta1",
				"metadata": map[string]interface{}{
					"name":      "podinfo",
					"namespace": "example",
					"annotations": map[string]interface{}{
						"annotation1.example.com": "value1",
						"annotation2.example.com": "value2",
					},
					"labels": map[string]interface{}{
						"label1": "value1",
						"label2": "value2",
					},
				},
				"spec": map[string]interface{}{},
			},
		}

		patch := `
apiVersion: extensions/v1beta1
kind: Ingress
metadata:
  labels:
    label2: value22
    label3: value33
  annotations:
    annotation2.example.com: value22
    annotation3.example.com: foo.{{- kget "cm/quack/quack-config" "data.domain" -}}
`

		log := ctrl.Log.WithName("test")
		patchApplier, err := k8s.NewPatchApplier(clientset(), crdClient(), log)
		Expect(err).ToNot(HaveOccurred())
		patchApplier.FuncMap["kget"] = func(path, jsonPath string) string {
			return "1.2.3.4.nip.io"
		}

		newResource, err := patchApplier.Apply(resource, patch, k8s.PatchTypeYaml)
		Expect(err).ToNot(HaveOccurred())

		specYaml, err := yaml.Marshal(newResource.Object)
		Expect(err).ToNot(HaveOccurred())
		foundYaml := strings.TrimSpace(string(specYaml))

		expectedYaml := strings.TrimSpace(`
apiVersion: extensions/v1beta1
kind: Ingress
metadata:
  annotations:
    annotation1.example.com: value1
    annotation2.example.com: value22
    annotation3.example.com: foo.1.2.3.4.nip.io
  labels:
    label1: value1
    label2: value22
    label3: value33
  name: podinfo
  namespace: example
spec: {}
`)
		fmt.Printf("Found:\n%s\n", foundYaml)
		fmt.Printf("Expected:\n%s\n", expectedYaml)
		Expect(foundYaml).To(Equal(expectedYaml))
	})
})
