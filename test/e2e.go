package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/commons/console"
	"github.com/flanksource/commons/logger"
	"github.com/flanksource/commons/utils"
	"github.com/flanksource/kommons"
	templatev1 "github.com/flanksource/template-operator/api/v1"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	postgresv1 "github.com/zalando/postgres-operator/pkg/apis/acid.zalan.do/v1"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	crdclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/yaml"
)

const (
	namespace  = "template-operator"
	deployment = "template-operator-controller-manager"
)

var (
	k8s    *kubernetes.Clientset
	crdK8s crdclient.Client
	client *kommons.Client
	tests  = map[string]Test{
		"template-operator-is-running": TestTemplateOperatorIsRunning,
		"deployment-replicas":          TestDeploymentReplicas,
		"copy-to-namespace":            TestCopyToNamespace,
		"awx-operator":                 TestAwxOperator,
		"for-each-array":               TestForEachWithArray,
		"for-each-map":                 TestForEachWithMap,
		"when-true":                    TestWhenConditional,
		"when-false":                   TestWhenConditionalFalse,
		"depends-on":                   TestDependsOnAttribute,
		"gitrepository-source":         TestGitRepositorySource,
		"rest-template":                TestRestTemplate,
	}
	scheme              = runtime.NewScheme()
	restConfig          *rest.Config
	pullRequestUsername string
)

type Test func(context.Context, *console.TestResults) error
type DeferFunc func()
type deploymentFn func(*appsv1.Deployment) bool

type MockserverRequest struct {
	ContentLength int                 `json:"content_length"`
	ContentType   string              `json:"content_type"`
	Time          int                 `json:"time"`
	Method        string              `json:"method"`
	Path          string              `json:"path"`
	Body          string              `json:"body"`
	Headers       map[string][]string `json:"headers"`
	QueryString   map[string][]string `json:"query_string"`
}

type MockserverExpectation struct {
	ID string `json:"id"`
}

func main() {
	var kubeconfig *string
	var timeout *time.Duration
	var debug *bool
	var err error
	if home := homeDir(); home != "" {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}
	timeout = flag.Duration("timeout", 10*time.Minute, "Global timeout for all tests")
	debug = flag.Bool("debug", false, "Debug mode")
	flag.Parse()

	if debug != nil && *debug {
		logger.StandardLogger().SetLogLevel(4)
	}

	_ = clientgoscheme.AddToScheme(scheme)

	_ = templatev1.AddToScheme(scheme)

	// use the current context in kubeconfig
	restConfig, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		log.Fatalf("failed to create k8s config: %v", err)
	}

	k8s, err = kubernetes.NewForConfig(restConfig)
	if err != nil {
		log.Fatalf("failed to create clientset: %v", err)
	}

	mapper, err := apiutil.NewDynamicRESTMapper(restConfig)
	if err != nil {
		log.Fatalf("failed to create mapper: %v", err)
	}

	client = kommons.NewClient(restConfig, logger.StandardLogger())

	crdK8s, err = crdclient.New(restConfig, crdclient.Options{Scheme: scheme, Mapper: mapper})
	if err != nil {
		log.Fatalf("failed to create crd client: %v", err)
	}

	test := &console.TestResults{
		Writer: os.Stdout,
	}

	errors := map[string]error{}
	deadline, cancelFunc := context.WithTimeout(context.Background(), *timeout)
	defer cancelFunc()

	for name, t := range tests {
		err := t(deadline, test)
		if err != nil {
			errors[name] = err
		}
	}

	if len(errors) > 0 {
		for name, err := range errors {
			log.Errorf("test %s failed: %v", name, err)
		}
		os.Exit(1)
	}

	log.Infof("All tests passed !!!")
}

func TestTemplateOperatorIsRunning(ctx context.Context, test *console.TestResults) error {
	pods, err := k8s.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: "control-plane=template-operator"})
	if err != nil {
		test.Failf("TestTemplateOperatorIsRunning", "failed to list template-operator pods: %v", err)
		return err
	}
	if len(pods.Items) != 1 {
		test.Failf("TestTemplateOperatorIsRunning", "expected 1 pod got %d", len(pods.Items))
		return errors.Errorf("Expected 1 pod got %d", len(pods.Items))
	}
	test.Passf("TestTemplateOperatorIsRunning", "%s pod is running", pods.Items[0].Name)
	return nil
}

func TestDeploymentReplicas(ctx context.Context, test *console.TestResults) error {
	ns := fmt.Sprintf("template-operator-e2e-%s", utils.RandomString(6))
	if err := client.CreateOrUpdateNamespace(ns, nil, nil); err != nil {
		test.Failf("TestDeploymentReplicas", "failed to create namespace %s", ns)
		return err
	}

	defer func() {
		if err := k8s.CoreV1().Namespaces().Delete(context.Background(), ns, metav1.DeleteOptions{}); err != nil {
			log.Errorf("failed to delete namespace %s: %v", ns, err)
		}
	}()

	if err := client.CreateOrUpdateConfigMap("nginx-config", ns, map[string]string{"replicas": "3"}); err != nil {
		test.Failf("TestDeploymentReplicas", "failed to create nginx config")
		return err
	}

	deployment := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nginx-deployment",
			Namespace: ns,
			Labels:    map[string]string{"app": "nginx"},
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "nginx"},
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "nginx"},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  "nginx",
							Image: "nginx:1.14.2",
							Ports: []v1.ContainerPort{{ContainerPort: 80}},
						},
					},
				},
			},
		},
	}

	deployment, err := k8s.AppsV1().Deployments(ns).Create(ctx, deployment, metav1.CreateOptions{})
	if err != nil {
		test.Failf("TestDeploymentReplicas", "failed to create deployment")
		return err
	}

	patchTemplate := `
apiVersion: apps/v1
kind: Deployment
spec:
  replicas: "{{ kget "cm/%s/nginx-config" "data.replicas" }}"
`
	patch := fmt.Sprintf(patchTemplate, ns)

	template := &templatev1.Template{
		TypeMeta:   metav1.TypeMeta{APIVersion: "templating.flanksource.com/v1", Kind: "Template"},
		ObjectMeta: metav1.ObjectMeta{Name: ns},
		Spec: templatev1.TemplateSpec{
			Source: templatev1.ResourceSelector{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				LabelSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{"app": "nginx"},
				},
			},
			Patches: []string{patch},
		},
	}
	if err := client.Apply("", template); err != nil {
		test.Failf("TestDeploymentReplicas", "failed to create template: %v", err)
		return err
	}
	defer func() {
		if err := crdK8s.Delete(context.Background(), template); err != nil {
			log.Errorf("failed to delete template: %v", err)
		}
	}()

	_, err = waitForDeploymentChanged(ctx, deployment, func(d *appsv1.Deployment) bool {
		return d.Spec.Replicas != nil && *d.Spec.Replicas == 3
	})
	if err != nil {
		test.Failf("TestDeploymentReplicas", "deployment was not updated by template: %v", err)
		return err
	}

	test.Passf("TestDeploymentReplicas", "Deployment updated by template to 3 replicas")

	return nil
}

func TestCopyToNamespace(ctx context.Context, test *console.TestResults) error {
	hash := utils.RandomString(6)
	sourceLabels := map[string]string{"e2e-namespace-role": "copy-to-namespace-source"}
	ns := fmt.Sprintf("template-operator-e2e-source-%s", hash)
	if err := client.CreateOrUpdateNamespace(ns, sourceLabels, nil); err != nil {
		test.Failf("TestCopyToNamespace", "failed to create namespace %s", ns)
		return err
	}
	destLabels := map[string]string{"e2e-namespace-role": "copy-to-namespace-dest"}
	namespaces := []string{
		"template-operator-e2e-dest-1",
		"template-operator-e2e-dest-2",
	}
	for _, n := range namespaces {
		if err := client.CreateOrUpdateNamespace(n, destLabels, nil); err != nil {
			test.Failf("TestCopyToNamespace", "failed to create namespace %s", n)
			return err
		}
	}

	defer func() {
		if err := k8s.CoreV1().Namespaces().Delete(context.Background(), ns, metav1.DeleteOptions{}); err != nil {
			log.Errorf("failed to delete namespace %s: %v", ns, err)
		}
		for _, n := range namespaces {
			if err := k8s.CoreV1().Namespaces().Delete(context.Background(), n, metav1.DeleteOptions{}); err != nil {
				log.Errorf("failed to delete namespace %s: %v", n, err)
			}
		}
	}()

	template, err := readFixture("copy-to-namespace.yml")
	if err != nil {
		test.Failf("TestCopyToNamespace", "failed to read fixture copy-to-namespace.yml: %v", err)
		return err
	}
	if err := client.Apply("", template); err != nil {
		test.Failf("TestCopyToNamespace", "failed to apply template copy-to-namespace: %v", err)
		return err
	}

	secret := &v1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("copy-to-namespace-secret"),
			Namespace: ns,
			Labels: map[string]string{
				"e2e-test": "copy-to-namespace",
			},
		},
		Data: map[string][]byte{
			"foo": []byte("bar"),
		},
	}
	if _, err := k8s.CoreV1().Secrets(secret.Namespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		test.Failf("TestCopyToNamespace", "failed to create source secret")
		return err
	}

	secret1, err := client.WaitForResource("Secret", namespaces[0], secret.Name, 5*time.Minute)
	if err != nil {
		test.Failf("TestCopyToNamespace", "error waiting for secret %s in namespace %s", secret.Name, namespaces[0])
		return err
	}
	secret1Data := secret1.Object["data"].(map[string]interface{})
	// base64 encoded of "bar" is YmFy
	if secret1Data["foo"] != "YmFy" {
		test.Failf("TestCopyToNamespace", "expected secret1 data to have foo=bar, has foo=%s", secret1Data["foo"])
	}

	secret2, err := client.WaitForResource("Secret", namespaces[1], secret.Name, 5*time.Minute)
	if err != nil {
		test.Failf("TestCopyToNamespace", "error waiting for secret %s in namespace %s", secret.Name, namespaces[0])
		return err
	}
	secret2Data := secret2.Object["data"].(map[string]interface{})
	// base64 encoded of "bar" is YmFy
	if secret2Data["foo"] != "YmFy" {
		test.Failf("TestCopyToNamespace", "expected secret1 data to have foo=bar, has foo=%s", secret2Data["foo"])
	}

	test.Passf("TestCopyToNamespace", "Secret was copied to all namespaces")
	return nil
}

func TestAwxOperator(ctx context.Context, test *console.TestResults) error {
	awxName := fmt.Sprintf("test-awx-e2e-%s", utils.RandomString(6))
	awx := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "awx.flanksource.com/v1beta1",
			"kind":       "AWX",
			"metadata": map[string]interface{}{
				"name":      awxName,
				"namespace": "awx-operator",
			},
			"spec": map[string]interface{}{
				"version": "15.0.0",
				"backup": map[string]interface{}{
					"bucket": "e2e-postgres-backups",
				},
				"parameters": map[string]interface{}{
					"max_connections":      "1024",
					"shared_buffers":       "1024MB",
					"work_mem":             "475MB",
					"maintenance_work_mem": "634MB",
				},
				"cpu":    0.5,
				"memory": "6Gi",
			},
		},
	}

	if err := client.Apply(awx.GetNamespace(), awx); err != nil {
		test.Failf("TestAwxOperator", "failed to create test awx: %v", err)
		return err
	}

	defer func() {
		if err := client.DeleteUnstructured(awx.GetNamespace(), awx); err != nil {
			logger.Errorf("failed to delete awx %s: %v", awx.GetName(), err)
		}
	}()

	postgresqlDb := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "db.flanksource.com/v1",
			"kind":       "PostgresqlDB",
			"metadata": map[string]interface{}{
				"name":      awxName,
				"namespace": "postgres-operator",
			},
		},
	}

	if err := waitForPostgresqlDB(ctx, postgresqlDb, test, "TestAwxOperator"); err != nil {
		return err
	}

	postgres := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "acid.zalan.do/v1",
			"kind":       "postgresql",
			"metadata": map[string]interface{}{
				"name":      fmt.Sprintf("postgres-%s", awxName),
				"namespace": "postgres-operator",
			},
		},
	}

	if err := waitForPostgres(ctx, postgres, test, "TestAwxOperator"); err != nil {
		return err
	}

	test.Passf("TestAwxOperator", "All awx resources running")

	return nil
}

func TestForEachWithArray(ctx context.Context, test *console.TestResults) error {
	testName := "TestForEachWithArray"
	ns := fmt.Sprintf("test-abcd-e2e-%s", utils.RandomString(6))
	if err := client.CreateOrUpdateNamespace(ns, nil, nil); err != nil {
		test.Failf(testName, "failed to create namespace %s: %v", ns, err)
		return err
	}

	defer func() {
		if err := k8s.CoreV1().Namespaces().Delete(context.Background(), ns, metav1.DeleteOptions{}); err != nil {
			logger.Errorf("failed to delete namespace %s: %v", ns, err)
		}
	}()

	abcdName := fmt.Sprintf("abcd-test-array")
	abcd := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "abcd.flanksource.com/v1",
			"kind":       "ABCD",
			"metadata": map[string]interface{}{
				"name":      abcdName,
				"namespace": ns,
			},
			"spec": map[string]interface{}{
				"topics": []string{"a", "b", "c", "d"},
			},
		},
	}

	if err := client.Apply(ns, abcd); err != nil {
		test.Failf(testName, "failed to create test abcd: %v", err)
		return err
	}

	defer func() {
		if err := client.DeleteUnstructured(ns, abcd); err != nil {
			logger.Errorf("failed to delete abcd %s: %v", abcdName, err)
		}
	}()

	abcdTopics := []string{"a", "b", "c", "d"}
	for _, topic := range abcdTopics {
		name := fmt.Sprintf("abcd-test-array-%s", topic)
		if err := waitForAbcdTopic(ctx, name, ns, map[string]string{"topicName": topic}); err != nil {
			test.Failf(testName, "ABCD topic %s not found: %v", name, err)
		} else {
			test.Passf(testName, "ABCD topic %s found", name)
		}
	}

	return nil
}

func TestForEachWithMap(ctx context.Context, test *console.TestResults) error {
	testName := "TestForEachWithMap"
	ns := fmt.Sprintf("test-abcd-e2e-%s", utils.RandomString(6))
	if err := client.CreateOrUpdateNamespace(ns, nil, nil); err != nil {
		test.Failf(testName, "failed to create namespace %s: %v", ns, err)
		return err
	}

	defer func() {
		if err := k8s.CoreV1().Namespaces().Delete(context.Background(), ns, metav1.DeleteOptions{}); err != nil {
			logger.Errorf("failed to delete namespace %s: %v", ns, err)
		}
	}()

	abcdName := fmt.Sprintf("abcd-test-map")
	abcd := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "abcd.flanksource.com/v1",
			"kind":       "ABCD",
			"metadata": map[string]interface{}{
				"name":      abcdName,
				"namespace": ns,
			},
			"spec": map[string]interface{}{
				"topicsMap": map[string]string{
					"a1": "a2",
					"b1": "b2",
					"c1": "c2",
					"d1": "d2",
				},
			},
		},
	}

	if err := client.Apply(ns, abcd); err != nil {
		test.Failf(testName, "failed to create test abcd: %v", err)
		return err
	}

	defer func() {
		if err := client.DeleteUnstructured(ns, abcd); err != nil {
			logger.Errorf("failed to delete abcd %s: %v", abcdName, err)
		}
	}()

	abcdTopics := map[string]string{
		"a1": "a2",
		"b1": "b2",
		"c1": "c2",
		"d1": "d2",
	}
	for k, v := range abcdTopics {
		name := fmt.Sprintf("abcd-test-map-%s", k)
		if err := waitForAbcdTopic(ctx, name, ns, map[string]string{k: v}); err != nil {
			test.Failf(testName, "ABCD topic %s not found: %v", name, err)
		} else {
			test.Passf(testName, "ABCD topic %s found", name)
		}
	}

	return nil
}

func TestWhenConditional(ctx context.Context, test *console.TestResults) error {
	testName := "TestWhenConditional"
	ns := fmt.Sprintf("test-when-e2e-%s", utils.RandomString(6))
	if err := client.CreateOrUpdateNamespace(ns, nil, nil); err != nil {
		test.Failf(testName, "failed to create namespace %s: %v", ns, err)
		return err
	}

	defer func() {
		if err := k8s.CoreV1().Namespaces().Delete(context.Background(), ns, metav1.DeleteOptions{}); err != nil {
			logger.Errorf("failed to delete namespace %s: %v", ns, err)
		}
	}()

	appName := fmt.Sprintf("app-test-when")
	app := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "example.flanksource.com/v1",
			"kind":       "App",
			"metadata": map[string]interface{}{
				"name":      appName,
				"namespace": ns,
			},
			"spec": map[string]interface{}{
				"image":         "nginx",
				"exposeService": true,
			},
		},
	}

	if err := client.Apply(ns, app); err != nil {
		test.Failf(testName, "failed to create test app: %v", err)
		return err
	}

	defer func() {
		if err := client.DeleteUnstructured(ns, app); err != nil {
			logger.Errorf("failed to delete app %s: %v", appName, err)
		}
	}()
	if _, err := client.WaitForResource("Deployment", ns, appName, 5*time.Minute); err != nil {
		test.Failf(testName, "Deployment %s not found: %v", appName, err)
	} else {
		test.Passf(testName, "Deployment %s found", appName)
	}

	if _, err := waitForService(ctx, appName, ns); err != nil {
		test.Failf(testName, "Service %s not found: %v", appName, err)
	} else {
		test.Passf(testName, "Service %s found", appName)
	}

	return nil
}

func TestWhenConditionalFalse(ctx context.Context, test *console.TestResults) error {
	testName := "TestWhenConditionalFalse"
	ns := fmt.Sprintf("test-when-e2e-%s", utils.RandomString(6))
	if err := client.CreateOrUpdateNamespace(ns, nil, nil); err != nil {
		test.Failf(testName, "failed to create namespace %s: %v", ns, err)
		return err
	}

	defer func() {
		if err := k8s.CoreV1().Namespaces().Delete(context.Background(), ns, metav1.DeleteOptions{}); err != nil {
			logger.Errorf("failed to delete namespace %s: %v", ns, err)
		}
	}()

	appName := fmt.Sprintf("app-test-when")
	app := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "example.flanksource.com/v1",
			"kind":       "App",
			"metadata": map[string]interface{}{
				"name":      appName,
				"namespace": ns,
			},
			"spec": map[string]interface{}{
				"image":         "nginx",
				"exposeService": false,
			},
		},
	}

	if err := client.Apply(ns, app); err != nil {
		test.Failf(testName, "failed to create test app: %v", err)
		return err
	}

	defer func() {
		if err := client.DeleteUnstructured(ns, app); err != nil {
			logger.Errorf("failed to delete app %s: %v", appName, err)
		}
	}()

	if _, err := client.WaitForResource("Deployment", ns, appName, 5*time.Minute); err != nil {
		test.Failf(testName, "Deployment %s not found: %v", appName, err)
	} else {
		test.Passf(testName, "Deployment %s found", appName)
	}

	_, err := k8s.CoreV1().Services(ns).Get(ctx, appName, metav1.GetOptions{})
	if err == nil {
		test.Failf(testName, "Service %s was created", appName)
	} else if !kerrors.IsNotFound(err) {
		test.Failf(testName, "Error getting service %s", appName)
	} else {
		test.Passf(testName, "Service %s was not created", appName)
	}

	return nil
}

func TestDependsOnAttribute(ctx context.Context, test *console.TestResults) error {
	testName := "TestDependsOnAttribute"
	ns := fmt.Sprintf("test-depends-e2e-%s", utils.RandomString(6))
	if err := client.CreateOrUpdateNamespace(ns, nil, nil); err != nil {
		test.Failf(testName, "failed to create namespace %s: %v", ns, err)
		return err
	}
	defer func() {
		if err := client.ForceDeleteNamespace(ns, 5*time.Minute); err != nil {
			logger.Errorf("failed to delete namespace %s: %v", ns, err)
		}
	}()

	appName := fmt.Sprintf("app-test-dependson")
	app := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps.flanksource.com/v1",
			"kind":       "Depend",
			"metadata": map[string]interface{}{
				"name":      appName,
				"namespace": ns,
			},
			"spec": map[string]interface{}{
				"image": "nginx",
			},
		},
	}
	if err := client.Apply(ns, app); err != nil {
		test.Failf(testName, "failed to create test app: %v", err)
		return err
	}

	defer func() {
		if err := client.DeleteUnstructured(ns, app); err != nil {
			logger.Errorf("failed to delete app %s: %v", appName, err)
		}
	}()

	// Secret will be created

	if _, err := client.WaitForResource("Secret", ns, appName, 5*time.Minute); err != nil {
		test.Failf(testName, "Error while getting secret")
		return err
	} else {
		test.Passf(testName, "Secret %s created successfully", appName)
	}

	// Deployment will not be created
	_, err := k8s.AppsV1().Deployments(ns).Get(ctx, appName, metav1.GetOptions{})
	if err == nil {
		test.Failf(testName, "Deployment %s was created", appName)
		return fmt.Errorf("")
	} else if !kerrors.IsNotFound(err) {
		test.Failf(testName, "Error getting deployment %s", appName)
		return fmt.Errorf("")
	} else {
		test.Passf(testName, "Deployment %s was not created", appName)
	}

	// Updating the unstructured object and verify that deployment is created
	sampleApp := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps.flanksource.com/v1",
			"kind":       "Depend",
			"metadata": map[string]interface{}{
				"name":      appName,
				"namespace": ns,
			},
			"spec": map[string]interface{}{
				"image":  "nginx",
				"type":   "Ready",
				"status": "True",
			},
		},
	}
	if err := client.Apply(ns, sampleApp); err != nil {
		test.Failf(testName, "failed to update Depend app: %v", err)
		return err
	}

	// Deployment will be created
	if _, err := client.WaitForResource("Deployment", ns, appName, 5*time.Minute); err != nil {
		test.Failf(testName, "Error while getting deployment")
		return err
	} else {
		test.Passf(testName, "Deployment %s created successfully", appName)
	}

	return nil
}

func TestGitRepositorySource(ctx context.Context, test *console.TestResults) error {
	testName := "TestGitRepositorySource"

	ns := fmt.Sprintf("test-gitrepository-source-e2e-%s", utils.RandomString(6))
	if err := client.CreateOrUpdateNamespace(ns, nil, nil); err != nil {
		test.Failf(testName, "failed to create namespace %s: %v", ns, err)
		return err
	}
	defer func() {
		if err := client.ForceDeleteNamespace(ns, 5*time.Minute); err != nil {
			logger.Errorf("failed to delete namespace %s: %v", ns, err)
		}
	}()

	template := &templatev1.Template{
		TypeMeta:   metav1.TypeMeta{APIVersion: "templating.flanksource.com/v1", Kind: "Template"},
		ObjectMeta: metav1.ObjectMeta{Name: "gitrepository", Namespace: ns},
		Spec: templatev1.TemplateSpec{
			Source: templatev1.ResourceSelector{
				GitRepository: &templatev1.GitRepository{
					Name:      "template-operator-dashboards",
					Namespace: "default",
					Glob:      "/grafana/dashboards/*.json",
				},
			},
			Resources: []runtime.RawExtension{
				{
					Object: &unstructured.Unstructured{
						Object: map[string]interface{}{
							"apiVersion": "integreatly.org/v1alpha1",
							"kind":       "GrafanaDashboard",
							"metadata": map[string]interface{}{
								"name":      "{{ .filename | filepath.Base }}",
								"namespace": ns,
								"labels": map[string]string{
									"app": "grafana",
								},
							},
							"spec": map[string]interface{}{
								"json": "{{ .content }}",
							},
						},
					},
				},
			},
		},
	}

	if err := client.Apply("", template); err != nil {
		test.Failf(testName, "failed to create test app: %v", err)
		return err
	}

	defer func() {
		if err := crdK8s.Delete(context.Background(), template); err != nil {
			log.Errorf("failed to delete template: %v", err)
		}
	}()

	if err := waitForGrafanaDashboard(ctx, ns, "elasticsearch.json"); err != nil {
		test.Failf(testName, "expected grafana dashboard elasticsearch.json: %v", err)
		return err
	}

	test.Passf(testName, "GrafanaDashboard elasticsearch.json created based on git spec")

	return nil
}

func TestRestTemplate(ctx context.Context, test *console.TestResults) error {
	testName := "TestRestTemplate"
	mockserverUrl := "https://mockserver.127.0.0.1.nip.io"

	if err := clearExpectations(mockserverUrl); err != nil {
		logger.Errorf("failed to clear expectations: %v", err)
	}

	defer func() {
		if err := clearExpectations(mockserverUrl); err != nil {
			logger.Errorf("failed to clear expectations: %v", err)
		}
	}()

	generatedID := utils.RandomString(10)

	updateExpectationID, err := createRestUpdateExpectation(mockserverUrl, generatedID)
	if err != nil {
		test.Failf(testName, "failed to create update expectation: %v", err)
		return err
	}

	deleteExpectationID, err := createRestDeleteExpectation(mockserverUrl, generatedID)
	if err != nil {
		test.Failf(testName, "failed to create delete expectation: %v", err)
		return err
	}

	restName := fmt.Sprintf("rest-example")
	rest := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "templating.flanksource.com/v1",
			"kind":       "REST",
			"metadata": map[string]interface{}{
				"name": restName,
			},
			"spec": map[string]interface{}{
				"headers": map[string]string{
					"Content-Type": "application/json",
				},
				"update": map[string]interface{}{
					"url":    "http://mockserver.mockserver:80/api/v2/silences",
					"method": "POST",
					"body": `	
						{
							"matchers": [
								{
									"name": "alertname",
									"value": "ExcessivePodCPURatio",
									"isRegex": false,
									"isEqual": true
								}
							],
							{{ if .status.silenceID }}
								"id": "{{ .status.silenceID }}",
							{{ end }}
							"startsAt": "2021-07-14T10:19:19.862Z",
							"endsAt": "2021-11-14T10:19:19.862Z",
							"createdBy": "template-operator",
							"comment": "Automatically created by template operator REST"
						}
`,
					"status": map[string]string{
						"silenceID": "{{ .response.silenceID }}",
					},
				},
				"remove": map[string]interface{}{
					"method": "DELETE",
					"url":    "http://mockserver.mockserver:80/api/v2/silence/{{.status.silenceID }}",
				},
			},
		},
	}

	if err := client.Apply("", rest); err != nil {
		test.Failf(testName, "failed to create test app: %v", err)
		return err
	}

	defer func() {
		if deleteErr := forceDeleteRest(client, rest); deleteErr != nil {
			logger.Errorf("failed to delete rest %s: %v", restName, deleteErr)
		}
	}()

	if err := waitForUpdateExpectation(mockserverUrl, updateExpectationID); err != nil {
		test.Failf(testName, "failed to wait for update expectation: %v", err)
		return err
	}

	test.Passf(testName, "Operator called update API %s", restName)

	newRest, err := client.GetByKind("REST", "", restName)
	if err != nil {
		test.Failf(testName, "failed to get rest object: %v", err)
		return err
	}
	status, ok := newRest.Object["status"].(map[string]interface{})
	if !ok {
		err = errors.Errorf("failed to cast rest status to map")
		test.Failf(testName, err.Error())
		return err
	}
	silenceIDi, found := status["silenceID"]
	if !found {
		err = errors.Errorf("did not found silenceID field in rest status")
		test.Failf(testName, err.Error())
		return err
	}
	silenceID, ok := silenceIDi.(string)
	if !ok {
		err = errors.Errorf("expected status.silenceID to be string")
		test.Failf(testName, err.Error())
		return err
	}

	if silenceID != generatedID {
		err = errors.Errorf("expected silenceID to equal %s, got %s", generatedID, silenceID)
		test.Failf(testName, err.Error())
		return err
	}

	if err := client.DeleteUnstructured("", rest); err != nil {
		logger.Errorf("failed to delete rest %s: %v", restName, err)
	}

	if err := waitForDeleteExpectation(mockserverUrl, deleteExpectationID, silenceID); err != nil {
		test.Failf(testName, "failed to wait for delete expectation: %v", err)
		return err
	}

	test.Passf(testName, "Operator called delete API %s", restName)
	return nil
}

func waitForDeploymentChanged(ctx context.Context, deployment *appsv1.Deployment, fn deploymentFn) (*appsv1.Deployment, error) {
	for {
		d, err := k8s.AppsV1().Deployments(deployment.Namespace).Get(ctx, deployment.Name, metav1.GetOptions{})
		if err != nil {
			return nil, errors.Wrapf(err, "failed to get deployment %s", deployment.Name)
		}

		if fn(d) {
			return d, nil
		}

		log.Debugf("Deployment %s not changed", deployment.Name)
		time.Sleep(2 * time.Second)
	}
}

func waitForService(ctx context.Context, name, namespace string) (*v1.Service, error) {
	for {
		svc, err := k8s.CoreV1().Services(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil && !kerrors.IsNotFound(err) {
			return nil, errors.Wrapf(err, "failed to get service %s in namespace %s", name, namespace)
		} else if kerrors.IsNotFound(err) {
			time.Sleep(2 * time.Second)
			continue
		}
		return svc, nil
	}
}

func waitForPostgresqlDB(ctx context.Context, postgresqlDB *unstructured.Unstructured, test *console.TestResults, testName string) error {
	name := postgresqlDB.GetName()
	logger.Debugf("Waiting for PostgresqlDB to be created by Awx Operator")
	for {
		client, _, _, err := client.GetDynamicClientFor(postgresqlDB.GetNamespace(), postgresqlDB)
		if err != nil {
			test.Failf(testName, "failed to get client for PostgresqlDB: %v", err)
			return err
		}
		db, err := client.Get(ctx, name, metav1.GetOptions{})
		if err != nil && !kerrors.IsNotFound(err) {
			test.Failf(testName, "failed to get PostgresqlDB: %v", err)
			return err
		} else if kerrors.IsNotFound(err) {
			time.Sleep(2 * time.Second)
			continue
		}

		// db.SetManagedFields([]metav1.ManagedFieldsEntry{})
		unstructured.RemoveNestedField(db.Object, "creationTimestamp")
		db.SetManagedFields(nil)
		db.SetSelfLink("")
		db.SetUID("")
		db.SetResourceVersion("")
		db.SetGeneration(0)
		db.SetCreationTimestamp(metav1.Time{})
		yml, err := yaml.Marshal(&db.Object)
		if err != nil {
			test.Failf(testName, "failed to marshal PostgresqlDB: %v", err)
			return err
		}

		expectedYamlTemplate := `
apiVersion: db.flanksource.com/v1
kind: PostgresqlDB
metadata:
  annotations:
    template-operator-owner-ref: awx-operator/%s
  name: %s
  namespace: postgres-operator
spec:
  backup:
    bucket: e2e-postgres-backups
  cpu: "0.5"
  memory: 6Gi
  parameters:
    maintenance_work_mem: 634MB
    max_connections: "1024"
    shared_buffers: 1024MB
    work_mem: 475MB
  replicas: 2
  storage:
    storageClass: local-path
`
		if !expectYamlMatch(expectedYamlTemplate, string(yml), name, name) {
			test.Errorf("Expected postgresql template to match:\nExpected:\n%s\nFound:\n%s\n", expectedYamlTemplate, string(yml))
			test.Failf(testName, "postgresqlDB does not match")
			return errors.Errorf("postgresqlDB does not match")
		}

		test.Passf(testName, "Found postgresqlDB %s", name)

		return nil
	}
}

func waitForPostgres(ctx context.Context, postgres *unstructured.Unstructured, test *console.TestResults, testName string) error {
	name := postgres.GetName()
	logger.Debugf("Waiting for Postgres to be created by PostgresqlDB operator")
	for {
		client, _, _, err := client.GetDynamicClientFor(postgres.GetNamespace(), postgres)
		if err != nil {
			test.Failf(testName, "failed to get client for Postgres: %v", err)
			return err
		}
		db, err := client.Get(ctx, name, metav1.GetOptions{})
		if err != nil && !kerrors.IsNotFound(err) {
			test.Failf(testName, "failed to get Postgres: %v", err)
			return err
		} else if kerrors.IsNotFound(err) {
			time.Sleep(2 * time.Second)
			continue
		}

		yml, err := yaml.Marshal(db)
		if err != nil {
			test.Failf(testName, "failed to marshal Postgres to yaml: %v", err)
			return err
		}
		ddb := &postgresv1.Postgresql{}
		if err := yaml.Unmarshal(yml, ddb); err != nil {
			test.Failf(testName, "failed to unmarshal Postgres from yaml: %v", err)
			return err
		}

		test.Passf(testName, "Found postgres %s", name)

		expectInt(test, testName, "instances", int(ddb.Spec.NumberOfInstances), 2)
		expect(test, testName, "CPU Request", ddb.Spec.ResourceRequests.CPU, "0.5")
		expect(test, testName, "CPU Limit", ddb.Spec.ResourceLimits.CPU, "0.5")
		expect(test, testName, "Memory Request", ddb.Spec.ResourceRequests.Memory, "6Gi")
		expect(test, testName, "Memory Limit", ddb.Spec.ResourceLimits.Memory, "6Gi")
		expect(test, testName, "Max connections", ddb.Spec.Parameters["max_connections"], "1024")
		expect(test, testName, "Shared buffers", ddb.Spec.Parameters["shared_buffers"], "1024MB")
		expect(test, testName, "Work mem", ddb.Spec.Parameters["work_mem"], "475MB")
		expect(test, testName, "Maintenance work mem", ddb.Spec.Parameters["maintenance_work_mem"], "634MB")

		return nil
	}
}

func waitForAbcdTopic(ctx context.Context, name, namespace string, spec map[string]string) error {
	abcdTopic := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "abcd.flanksource.com/v1",
			"kind":       "ABCDTopic",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
		},
	}

	start := time.Now()

	for {
		client, _, _, err := client.GetDynamicClientFor(namespace, abcdTopic)
		if err != nil {
			return errors.Wrap(err, "failed to get client for ABCDTopic")
		}
		topic, err := client.Get(ctx, name, metav1.GetOptions{})
		if err != nil && !kerrors.IsNotFound(err) {
			return errors.Wrapf(err, "failed to get abcd topic %s", name)
		} else if kerrors.IsNotFound(err) {
			if start.Add(5 * time.Minute).Before(time.Now()) {
				fmt.Printf("Waiting time exceeded")
				return err
			}
			time.Sleep(2 * time.Second)
			continue
		}

		topicSpecYaml, err := yaml.Marshal(topic.Object["spec"])
		if err != nil {
			return errors.Wrap(err, "failed to marshal topic to yaml")
		}

		specYaml, err := yaml.Marshal(spec)
		if err != nil {
			return errors.Wrap(err, "failed to marshal spec to yaml")
		}

		if !expectYamlMatch(string(specYaml), string(topicSpecYaml)) {
			return fmt.Errorf("Spec for ABCDTopic %s does not match", name)
		}
		return nil
	}
}

func waitForGrafanaDashboard(ctx context.Context, ns, name string) error {
	dashboard := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "integreatly.org/v1alpha1",
			"kind":       "GrafanaDashboard",
			"metadata": map[string]interface{}{
				"name":      "elasticsearch.json",
				"namespace": ns,
			},
		},
	}

	start := time.Now()

	for {
		client, _, _, err := client.GetDynamicClientFor(ns, dashboard)
		if err != nil {
			return errors.Wrap(err, "failed to get client for GrafanaDashboard")
		}
		dash, err := client.Get(ctx, name, metav1.GetOptions{})
		if err != nil && !kerrors.IsNotFound(err) {
			return errors.Wrapf(err, "failed to get grafana dashboard %s", name)
		} else if kerrors.IsNotFound(err) {
			if start.Add(5 * time.Minute).Before(time.Now()) {
				fmt.Printf("Waiting time exceeded")
				return err
			}
			time.Sleep(2 * time.Second)
			continue
		}

		labels := dash.GetLabels()
		if labels["app"] != "grafana" {
			return errors.Errorf("Expected GrafanaDashboard labels.app to equal grafana")
		}

		spec, ok := dash.Object["spec"].(map[string]interface{})
		if !ok {
			return errors.Errorf("failed to convert spec to map")
		}

		json, ok := spec["json"].(string)
		if !ok {
			return errors.Errorf("failed to get spec.json")
		}

		titleResult := gjson.Get(json, "title")
		if titleResult.Str != "Elasticsearch" {
			return errors.Errorf("expected dashboard title to equal Elasticsearch")
		}

		uidResult := gjson.Get(json, "uid")
		if uidResult.Str != "miVgSWjWp" {
			return errors.Errorf("expected dashboard uid to equal miVgSWjWp")
		}

		return nil
	}
}

func readFixture(path string) (*templatev1.Template, error) {
	templateBytes, err := ioutil.ReadFile(filepath.Join("test", "fixtures", path))
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read template %s", path)
	}
	template := &templatev1.Template{}
	if err := yaml.Unmarshal(templateBytes, template); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal yaml")
	}

	return template, nil
}

func createRestUpdateExpectation(url string, generatedID string) (string, error) {
	requestBody := `{
	"httpRequest": {
			"method": "POST",
			"path": "/api/v2/silences",
	},
	"httpResponseTemplate": {
		"template": "return { statusCode: 200, body: JSON.stringify({silenceID: '%s' }) };",
		"templateType": "JAVASCRIPT"
	}
}
`
	return createExpectation(url, fmt.Sprintf(requestBody, generatedID))
}

func createRestDeleteExpectation(url string, generatedID string) (string, error) {
	requestBody := ` {
	"httpRequest": {
			"method": "DELETE",
			"path": "/api/v2/silence/{silenceId}",
			"pathParameters": {
				"silenceId": [{
						"schema": {
								"type": "string",
								"pattern": "^[a-z0-9A-Z-]+$"
						}
				}],
			}
	},
	"httpResponse": {
		"body": "{}"
	}
}
`
	return createExpectation(url, requestBody)
}

func createExpectation(baseUrl string, template string) (string, error) {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: tr}
	url := baseUrl + "/mockserver/expectation"
	req, err := http.NewRequest("PUT", url, bytes.NewBuffer([]byte(template)))
	if err != nil {
		return "", errors.Wrap(err, "failed to create http request")
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.Wrap(err, "failed to send put request")
	}
	defer resp.Body.Close()

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", errors.Wrap(err, "failed to read response body")
	}

	if resp.StatusCode != http.StatusCreated {
		return "", errors.Errorf("expected status code 201, got %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	data := []MockserverExpectation{}
	if err := json.Unmarshal(bodyBytes, &data); err != nil {
		return "", errors.Wrap(err, "failed to unmarshal body")
	}

	if len(data) != 1 {
		return "", errors.Errorf("expected create expectation response data to have length 1, got: %d", len(data))
	}

	return data[0].ID, nil
}

func waitForUpdateExpectation(url, expectationID string) error {
	ctx, _ := context.WithTimeout(context.Background(), 2*time.Minute)
	requestBody := `{
	"httpRequest": {
		"method": "POST",
		"path": "/api/v2/silences"
	},
	"times": {
		"atLeast": 1,
		"atMost": 1
	}
}
`
	return waitForExpectation(ctx, url, expectationID, requestBody)
}

func waitForDeleteExpectation(url, expectationID, silenceID string) error {
	ctx, _ := context.WithTimeout(context.Background(), 2*time.Minute)
	requestBody := `{
	"httpRequest": {
		"method": "DELETE",
		"path": "/api/v2/silence/%s"
	},
	"times": {
		"atLeast": 1,
		"atMost": 1
	}
}
`
	return waitForExpectation(ctx, url, expectationID, fmt.Sprintf(requestBody, silenceID))
}

func waitForExpectation(ctx context.Context, baseUrl, expectationID, template string) error {
	for {
		tr := &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
		client := &http.Client{Transport: tr}
		url := baseUrl + "/mockserver/verify"
		req, err := http.NewRequest("PUT", url, bytes.NewBuffer([]byte(template)))
		req.Header.Add("Content-Type", "application/json")
		if err != nil {
			return errors.Wrap(err, "failed to create http request")
		}
		req = req.WithContext(ctx)

		resp, err := client.Do(req)
		if err != nil {
			return errors.Wrap(err, "failed to send put request")
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusAccepted {
			return nil
		}
		bodyBytes, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return errors.Wrap(err, "failed to read response body")
		}

		log.Debugf("expectation could not be verified, statusCode: %d, sleeping... ", resp.StatusCode)
		log.Debugf("response body was: %s\n", string(bodyBytes))

		time.Sleep(5 * time.Second)
	}
}

func clearExpectations(baseUrl string) error {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: tr}
	url := baseUrl + "/mockserver/reset"
	req, err := http.NewRequest("PUT", url, nil)
	if err != nil {
		return errors.Wrap(err, "failed to create http request")
	}

	resp, err := client.Do(req)
	if err != nil {
		return errors.Wrap(err, "failed to send put request")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusAccepted {
		return errors.Errorf("expected status code 202, got: %d", resp.StatusCode)
	}

	return nil
}

// forceDeleteRest takes care of the finalizers on rest object.
// template operator is adding finalizers so it can reconcile on objects deleted
// when an object is deleted the operator first runs the delete API call and then removes the finalizer.
// If the delete API call defined by REST fails forver due to misconfiguration the operator will try to reconcile the object forever.
func forceDeleteRest(client *kommons.Client, rest *unstructured.Unstructured) error {
	i, err := client.GetClientByKind("REST")
	if err != nil {
		return errors.Wrap(err, "failed to get k8s client for REST")
	}

	r, err := i.Get(context.Background(), rest.GetName(), metav1.GetOptions{})
	if err != nil {
		if kerrors.IsNotFound(err) {
			return nil
		}
		return errors.Wrapf(err, "failed to get rest %s for delete", rest.GetName())
	}

	if err := i.Delete(context.Background(), rest.GetName(), metav1.DeleteOptions{}); err != nil {
		return errors.Wrapf(err, "failed to delete rest %s", rest.GetName())
	}

	// If it does not have finalizers, it means the object got deleted right away
	if len(r.GetFinalizers()) == 0 {
		return nil
	}

	r, err = i.Get(context.Background(), rest.GetName(), metav1.GetOptions{})
	if err != nil {
		return errors.Wrapf(err, "failed to get rest %s to remove finalizers", rest.GetName())
	}

	// Finalizers are removed after delete to avoid template operator reconciling on the object again and adding finalizers back
	r.SetFinalizers([]string{})
	if r, err = i.Update(context.Background(), r, metav1.UpdateOptions{}); err != nil {
		return errors.Wrapf(err, "failed to delete finalizers for rest %s", rest.GetName())
	}

	return nil
}

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return os.Getenv("USERPROFILE") // windows
}

func expectYamlMatch(expectedTemplate, found string, args ...interface{}) bool {
	expectedYaml := strings.TrimSpace(fmt.Sprintf(expectedTemplate, args...))
	foundYaml := strings.TrimSpace(found)
	if expectedYaml != foundYaml {
		logger.Debugf("Expected yaml:\n%s\nFound:\n%s\n", expectedYaml, foundYaml)
		return false
	}
	return true
}

func expectInt(test *console.TestResults, testName, field string, found, expected int) {
	if found != expected {
		test.Failf(testName, "Expected field %s to equal %d, got %d", field, expected, found)
	} else {
		test.Passf(testName, "%s equals %d as expected", field, expected)
	}
}

func expect(test *console.TestResults, testName, field string, found, expected string) {
	if found != expected {
		test.Failf(testName, "Expected field %s to equal %s, got %s", field, expected, found)
	} else {
		test.Passf(testName, "%s equals %s as expected", field, expected)
	}
}
