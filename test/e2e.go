package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/commons/console"
	"github.com/flanksource/commons/logger"
	"github.com/flanksource/commons/utils"
	"github.com/flanksource/kommons"
	templatev1 "github.com/flanksource/template-operator/api/v1"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	var err error
	if home := homeDir(); home != "" {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}
	timeout = flag.Duration("timeout", 10*time.Minute, "Global timeout for all tests")
	flag.Parse()

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
