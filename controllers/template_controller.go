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
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/yaml"

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
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch

func (r *TemplateReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("template", req.NamespacedName)

	template := &templatev1.Template{}
	if err := r.Get(ctx, req.NamespacedName, template); err != nil {
		if kerrors.IsNotFound(err) {
			log.Error(err, "template not found")
			return reconcile.Result{}, nil
		}
		log.Error(err, "failed to get template")
		return reconcile.Result{}, err
	}

	filter := &k8s.Filter{DynamicClient: r.DynamicClient, Log: log}
	resourcePatches, err := filter.ResourcesForTemplate(ctx, template)
	if err != nil {
		return reconcile.Result{}, err
	}

	patchApplier := k8s.NewPatchApplier(r.DynamicClient.Clientset, log)

	for _, resourcePatch := range resourcePatches {
		yml, _ := yaml.Marshal(resourcePatch.Resource)
		fmt.Printf("=================\nResource before:\n%s\n---\n", yml)

		newResource, err := patchApplier.Apply(resourcePatch.Resource, resourcePatch.Patch, resourcePatch.PatchType)
		if err != nil {
			log.Error(err, "failed to apply patch to resource", "kind", resourcePatch.Kind, "name", resourcePatch.Resource.GetName(), "namespace", resourcePatch.Resource.GetNamespace())
			continue
		}
		resource := *newResource

		yml, _ = yaml.Marshal(resource)
		fmt.Printf("=================\nResource after:\n%s\n---\n", yml)
	}

	return ctrl.Result{}, nil
}

func (r *TemplateReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&templatev1.Template{}).
		Complete(r)
}
