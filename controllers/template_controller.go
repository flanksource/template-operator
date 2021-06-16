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

	templatev1 "github.com/flanksource/template-operator/api/v1"
	"github.com/flanksource/template-operator/k8s"
	"github.com/prometheus/client_golang/prometheus"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var (
	templateCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "template_operator_template_count",
			Help: "Total template runs count",
		},
		[]string{"template"},
	)
	templateSuccess = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "template_operator_template_success",
			Help: "Total successful template runs count",
		},
		[]string{"template"},
	)
	templateFailed = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "template_operator_template_failed",
			Help: "Total failed template runs count",
		},
		[]string{"template"},
	)
)

func init() {
	metrics.Registry.MustRegister(templateCount, templateSuccess, templateFailed)
}

// TemplateReconciler reconciles a Template object
type TemplateReconciler struct {
	Client
}

// +kubebuilder:rbac:groups="*",resources="*",verbs="*"

func (r *TemplateReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("template", req.NamespacedName)
	name := req.NamespacedName.String()

	template := &templatev1.Template{}
	if err := r.ControllerClient.Get(ctx, req.NamespacedName, template); err != nil {
		if kerrors.IsNotFound(err) {
			log.Error(err, "template not found")
			return reconcile.Result{}, nil
		}
		log.Error(err, "failed to get template")
		incFailed(name)
		return reconcile.Result{}, err
	}
	//If the TemplateManager will fetch a new schema, ensure the kommons.client also does so in order to ensure they contain the same information
	if r.Cache.SchemaHasExpired() {
		r.KommonsClient.ResetRestMapper()
	}
	tm, err := k8s.NewTemplateManager(r.KommonsClient, log, r.Cache, r.Events, r.Watcher)
	if err != nil {
		incFailed(name)
		return reconcile.Result{}, err
	}
	result, err := tm.Run(ctx, template, r.reconcileObject(req.NamespacedName))
	if err != nil {
		incFailed(name)
		return reconcile.Result{}, err
	}
	incSuccess(name)
	return result, nil
}

func (r *TemplateReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.ControllerClient = mgr.GetClient()
	r.Events = mgr.GetEventRecorderFor("template-operator")

	return ctrl.NewControllerManagedBy(mgr).
		For(&templatev1.Template{}).
		Complete(r)
}

func (r *TemplateReconciler) reconcileObject(namespacedName types.NamespacedName) k8s.CallbackFunc {
	return func(obj unstructured.Unstructured) error {
		ctx := context.Background()
		log := r.Log.WithValues("template", namespacedName)
		name := namespacedName.String()
		template := &templatev1.Template{}
		if err := r.ControllerClient.Get(ctx, namespacedName, template); err != nil {
			if kerrors.IsNotFound(err) {
				log.Error(err, "template not found")
				return err
			}
			log.Error(err, "failed to get template")
			incFailed(name)
			return err
		}

		//If the TemplateManager will fetch a new schema, ensure the kommons.client also does so in order to ensure they contain the same information
		if r.Cache.SchemaHasExpired() {
			r.KommonsClient.ResetRestMapper()
		}
		tm, err := k8s.NewTemplateManager(r.KommonsClient, log, r.Cache, r.Events, r.Watcher)
		if err != nil {
			incFailed(name)
			return err
		}
		_, err = tm.HandleSource(ctx, template, obj)
		if err != nil {
			incFailed(name)
			return err
		}
		incSuccess(name)

		return nil
	}
}

func incSuccess(name string) {
	templateCount.WithLabelValues(name).Inc()
	templateSuccess.WithLabelValues(name).Inc()
}

func incFailed(name string) {
	templateCount.WithLabelValues(name).Inc()
	templateFailed.WithLabelValues(name).Inc()
}
