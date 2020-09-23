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
	It("Merges ingress struct", func() {
		resource := unstructured.Unstructured{
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
								"podinfo",
							},
							"secretName": "podinfo-tls",
						},
					},
				},
			},
		}
		fmt.Printf("Resource name x %s\n", resource.GetName())
		val, found, err := unstructured.NestedString(resource.Object, "metadata", "name")
		if !found || err != nil {
			fmt.Printf("Found: %t, err: %s val |%s|\n", found, err, val)
		}

		patch := `
apiVersion: extensions/v1beta1
kind: Ingress
spec:
  rules:
    - host: "{{.source.metadata.name}}.{{- kget "cm/quack/quack-config" "data.domain" -}}"
  tls:
    - hosts:
      - "{{.source.metadata.name}}.{{- kget "cm/quack/quack-config" "data.domain" }}"
`

		log := ctrl.Log.WithName("test")
		patchApplier := k8s.NewPatchApplier(nil, log)
		patchApplier.FuncMap["kget"] = func(path, jsonPath string) string {
			return "1.2.3.4.nip.io"
		}

		newResource, err := patchApplier.Apply(resource, patch)
		Expect(err).To(BeNil())

		specYaml, err := yaml.Marshal(newResource.Object["spec"])
		Expect(err).To(BeNil())

		expectedYaml := `
rules:
- host: podinfo.1.2.3.4.nip.io
  http:
    paths:
    - backend:
        serviceName: podinfo
        servicePort: 9898
tls:
- hosts:
  - podinfo.1.2.3.4.nip.io
  secretName: podinfo-tls
`
		fmt.Printf("Found:\n%s\n", string(specYaml))
		fmt.Printf("Expected:\n%s\n", strings.TrimSpace(expectedYaml))
		Expect(string(specYaml)).To(Equal(strings.TrimSpace(expectedYaml)))
	})
})
