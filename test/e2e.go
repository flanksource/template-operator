package main

import (
	"context"
	"flag"
	"fmt"
	"io/ioutil"
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
	namespace  = "platform-system"
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
	}
	scheme              = runtime.NewScheme()
	restConfig          *rest.Config
	pullRequestUsername string
)

type Test func(context.Context, *console.TestResults) error
type DeferFunc func()
type deploymentFn func(*appsv1.Deployment) bool

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

	deadline, _ := context.WithTimeout(ctx, 2*time.Minute)
	if secret1, err := waitForSecret(deadline, secret.Name, namespaces[0]); err != nil {
		test.Failf("TestCopyToNamespace", "error waiting for secret %s in namespace %s", secret.Name, namespaces[0])
		return err
	} else if string(secret1.Data["foo"]) != "bar" {
		test.Failf("TestCopyToNamespace", "expected secret1 data to have foo=bar, has foo=%s", string(secret1.Data["foo"]))
	}
	if secret2, err := waitForSecret(deadline, secret.Name, namespaces[0]); err != nil {
		test.Failf("TestCopyToNamespace", "error waiting for secret %s in namespace %s", secret.Name, namespaces[0])
		return err
	} else if string(secret2.Data["foo"]) != "bar" {
		test.Failf("TestCopyToNamespace", "expected secret2 data to have foo=bar, has foo=%s", string(secret2.Data["foo"]))
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

	if _, err := waitForDeployment(ctx, appName, ns); err != nil {
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

	if _, err := waitForDeployment(ctx, appName, ns); err != nil {
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

func waitForSecret(ctx context.Context, name, namespace string) (*v1.Secret, error) {
	for {
		secret, err := k8s.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil && !kerrors.IsNotFound(err) {
			return nil, errors.Wrapf(err, "failed to get secret %s in namespace %s", name, namespace)
		} else if kerrors.IsNotFound(err) {
			time.Sleep(2 * time.Second)
			continue
		}
		return secret, nil
	}
}

func waitForDeployment(ctx context.Context, name, namespace string) (*appsv1.Deployment, error) {
	for {
		deployment, err := k8s.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil && !kerrors.IsNotFound(err) {
			return nil, errors.Wrapf(err, "failed to get deployment %s in namespace %s", name, namespace)
		} else if kerrors.IsNotFound(err) {
			time.Sleep(2 * time.Second)
			continue
		}
		return deployment, nil
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

	for {
		client, _, _, err := client.GetDynamicClientFor(namespace, abcdTopic)
		if err != nil {
			return errors.Wrap(err, "failed to get client for ABCDTopic")
		}
		topic, err := client.Get(ctx, name, metav1.GetOptions{})
		if err != nil && !kerrors.IsNotFound(err) {
			return errors.Wrapf(err, "failed to get abcd topic %s", name)
		} else if kerrors.IsNotFound(err) {
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

func assertEquals(test *console.TestResults, name, actual, expected string) error {
	if actual != expected {
		test.Failf(name, "expected %s to equal %s", actual, expected)
		return errors.Errorf("Test %s expected %s to equal %s", name, actual, expected)
	}
	return nil
}

func assertInterfaceEquals(test *console.TestResults, name string, actual, expected interface{}) error {
	actualYml, err := yaml.Marshal(actual)
	if err != nil {
		return errors.Wrap(err, "failed to marshal actual")
	}

	expectedYml, err := yaml.Marshal(expected)
	if err != nil {
		return errors.Wrap(err, "failed to marshal expected")
	}

	if string(actualYml) != string(expectedYml) {
		test.Failf("Test %s expected: %s\n\nTo Equal:\n%s\n", name, string(actualYml), string(expectedYml))
		return errors.Errorf("Test %s expected:\n%s\nTo Match:\n%s\n", name, actualYml, expectedYml)
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
