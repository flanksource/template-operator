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
)

// CRDReconciler reconciles changes to CRD objects
type CRDReconciler struct {
	ControllerClient client.Client
	Client           *kommons.Client
	Events           record.EventRecorder
	Log              logr.Logger
	Scheme           *runtime.Scheme
	Cache            *k8s.SchemaCache
	CRDcache		 map[string]string

}

// +kubebuilder:rbac:groups="*",resources="*",verbs="*"

func (r *CRDReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("crd", req.NamespacedName)

	if err := r.Cache.ExpireSchema(); err != nil {
		log.Error(err, "failed to reset internal CRD cache")
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
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
