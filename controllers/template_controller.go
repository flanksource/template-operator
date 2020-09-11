/*


Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	templatev1 "github.com/flanksource/template-operator/api/v1"
	"github.com/flanksource/template-operator/k8s"
)

// TemplateReconciler reconciles a Template object
type TemplateReconciler struct {
	client.Client
	DynamicClient *k8s.DynamicClient
	Log           logr.Logger
	Scheme        *runtime.Scheme
}

// +kubebuilder:rbac:groups=templating.flanksource.com,resources=templates,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=templating.flanksource.com,resources=templates/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=extensions,resources=ingresses,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch

func (r *TemplateReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("template", req.NamespacedName)

	template := &templatev1.Template{}
	if err := r.Get(ctx, req.NamespacedName, template); err != nil {
		if errors.IsNotFound(err) {
			log.Error(err, "template not found")
			return reconcile.Result{}, nil
		}
		log.Error(err, "failed to get template")
		return reconcile.Result{}, err
	}

	labelMap, err := metav1.LabelSelectorAsMap(&template.Spec.Source.LabelSelector)
	if err != nil {
		log.Error(err, "failed to convert namespace label selector to map")
		return reconcile.Result{}, err
	}
	listOptions := metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labelMap).String(),
	}
	namespaces, err := r.DynamicClient.Clientset.CoreV1().Namespaces().List(ctx, listOptions)
	if err != nil {
		log.Error(err, "failed to list namespaces")
		return reconcile.Result{}, err
	}

	for _, namespace := range namespaces.Items {
		for _, os := range template.Spec.Source.ObjectSelector {
			client, err := r.DynamicClient.GetClientByKind(os.Kind)
			if err != nil {
				log.Error(err, "failed to get dynamic client for", "kind", os.Kind)
				continue
			}
			options := metav1.ListOptions{
				FieldSelector: template.Spec.Source.FieldSelector,
				// LabelSelector: template.Spec.Source.LabelSelector.String(),
			}
			resources, err := client.Namespace(namespace.Name).List(ctx, options)
			if err != nil {
				log.Error(err, "failed to list resources for", "kind", os.Kind)
				continue
			}

			fmt.Printf("Found %s resources\n", len(resources.Items))

			for _, r := range resources.Items {
				fmt.Println("Found resource", "kind", r.GetKind(), "name", r.GetName(), "namespace", r.GetNamespace())
			}
		}
	}

	return ctrl.Result{}, nil
}

func (r *TemplateReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&templatev1.Template{}).
		Complete(r)
}
