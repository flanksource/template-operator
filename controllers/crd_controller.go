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
	"github.com/flanksource/kommons"
	"github.com/flanksource/template-operator/k8s"
	"github.com/go-logr/logr"
	apiv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	"strconv"
)

// CRDReconciler reconciles changes to CRD objects
type CRDReconciler struct {
	ControllerClient client.Client
	Client           *kommons.Client
	Events           record.EventRecorder
	Log              logr.Logger
	Scheme           *runtime.Scheme
	Cache            *k8s.SchemaCache
	ResourceVersion	 int

}

// +kubebuilder:rbac:groups="*",resources="*",verbs="*"

func (r *CRDReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("crd", req.NamespacedName)
	log.V(2).Info("crd update detected, checking cache state")
	crd := &apiv1.CustomResourceDefinition{}

	if err := r.ControllerClient.Get(context.Background(), req.NamespacedName, crd); err != nil {
		return reconcile.Result{}, err
	}

	resourceVersion, err := strconv.Atoi(crd.ResourceVersion)
	if err != nil {
		return reconcile.Result{}, err
	}

	if resourceVersion > r.ResourceVersion {
		log.V(2).Info("Newer resourceVersion detected, resetting cache")
		if err := r.resetCache(); err != nil {
			return reconcile.Result{}, err
		}
		r.ResourceVersion = resourceVersion
	}
	return reconcile.Result{}, nil
}

func (r *CRDReconciler) resetCache() error {
	if err := r.Cache.ExpireSchema(); err != nil {
		return err
	}
	return nil
}

func (r *CRDReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.ControllerClient = mgr.GetClient()
	r.Events = mgr.GetEventRecorderFor("template-operator")
	c, err := controller.New("crd-monitor", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}
	return c.Watch(&source.Kind{Type: &apiv1.CustomResourceDefinition{}}, &handler.EnqueueRequestForObject{})
}
