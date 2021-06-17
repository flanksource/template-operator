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
	"k8s.io/client-go/discovery"
	"strconv"

	"github.com/go-logr/logr"
	apiv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// CRDReconciler reconciles changes to CRD objects
type CRDReconciler struct {
	Client
	ResourceVersion int
}

// +kubebuilder:rbac:groups="*",resources="*",verbs="*"

func (r *CRDReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("crd", req.NamespacedName)
	log.V(2).Info("crd update detected, checking cache state")
	v1, err := r.HasKind(CRDV1Group, CRDV1Version)
	if err != nil {
		return ctrl.Result{}, err
	}
	if v1 {
		return r.reconcileV1(ctx, req, log)
	}
	return r.reconcileV1beta1(ctx, req, log)
}

func (r *CRDReconciler) reconcileV1(ctx context.Context, req ctrl.Request, log logr.Logger) (ctrl.Result, error) {
	crd := &apiv1.CustomResourceDefinition{}
	if err := r.ControllerClient.Get(ctx, req.NamespacedName, crd); err != nil {
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

func (r *CRDReconciler) reconcileV1beta1(ctx context.Context, req ctrl.Request, log logr.Logger) (ctrl.Result, error) {
	crd := &apiv1beta1.CustomResourceDefinition{}
	if err := r.ControllerClient.Get(ctx, req.NamespacedName, crd); err != nil {
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
	config, err := buildKubeConnectionConfig()
	if err != nil {
		return err
	}
	r.Discovery, err = discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return err
	}
	v1, err := r.HasKind(CRDV1Group, CRDV1Version)
	if err != nil {
		return err
	}
	if v1 {
		return c.Watch(&source.Kind{Type: &apiv1.CustomResourceDefinition{}}, &handler.EnqueueRequestForObject{})
	}
	return c.Watch(&source.Kind{Type: &apiv1beta1.CustomResourceDefinition{}}, &handler.EnqueueRequestForObject{})
}
