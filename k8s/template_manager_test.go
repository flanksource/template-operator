package k8s_test

import (
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/template-operator/k8s"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/yaml"
)

var testLog = ctrl.Log.WithName("test")

var _ = Describe("TemplateManager", func() {
	Describe("Template", func() {
		It("converts PostgresqlDb to zalando Postgresql", func() {

			db := map[string]interface{}{
				"apiVersion": "db.flanksource.com/v1",
				"kind":       "PostgresqlDB",
				"metadata": map[string]interface{}{
					"name":      "test1",
					"namespace": "postgres-operator",
				},
				"spec": map[string]interface{}{
					"replicas": 2,
					"parameters": map[string]interface{}{
						"max_connections":      "1024",
						"shared_buffers":       "4759MB",
						"work_mem":             "475MB",
						"maintenance_work_mem": "634MB",
					},
				},
			}

			template := `
apiVersion: acid.zalan.do/v1
kind: postgresql
metadata:
  name: postgres-{{.metadata.name}}
  namespace: postgres-operator
spec:
  numberOfInstances: "{{ .spec.replicas }}"
  clone: null
  postgresql:
    parameters: "{{ .spec.parameters | data.ToJSON }}"
  synchronous_mode: false
`
			templateJSON, err := yaml.YAMLToJSON([]byte(template))
			Expect(err).ToNot(HaveOccurred())

			expectedYaml := `
apiVersion: acid.zalan.do/v1
kind: postgresql
metadata:
  name: postgres-test1
  namespace: postgres-operator
spec:
  clone: null
  numberOfInstances: 2
  postgresql:
    parameters:
      maintenance_work_mem: 634MB
      max_connections: "1024"
      shared_buffers: 4759MB
      work_mem: 475MB
  synchronous_mode: false
`

			eventsRecorder := &TestEventRecorder{}
			cache := k8s.NewSchemaCache(clientset(), 5*time.Minute, testLog)
			templateManager, err := k8s.NewTemplateManager(kommonsClient(), testLog, cache, eventsRecorder, &k8s.NullWatcher{})
			Expect(err).ToNot(HaveOccurred())

			result, err := templateManager.Template([]byte(templateJSON), db)
			Expect(err).ToNot(HaveOccurred())

			yml := string(result)

			fmt.Printf("Expected:\n%v\n=======Actual:\n%v\n==========", expectedYaml, string(yml))
			Expect(strings.TrimSpace(string(yml))).To(Equal(strings.TrimSpace(expectedYaml)))
		})
	})
})
