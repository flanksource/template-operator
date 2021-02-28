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
	templatev1 "github.com/flanksource/template-operator/api/v1"
	"github.com/flanksource/template-operator/k8s"
	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	ControllerClient client.Client
	Client           *kommons.Client
	Events           record.EventRecorder
	Log              logr.Logger
	Scheme           *runtime.Scheme
	Cache            *k8s.SchemaCache
}

// +kubebuilder:rbac:groups="*",resources="*",verbs="*"

func (r *TemplateReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
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

	tm, err := k8s.NewTemplateManager(r.Client, log, r.Cache, r.Events)
	if err != nil {
		incFailed(name)
		return reconcile.Result{}, err
	}
	if err := tm.Run(ctx, template); err != nil {
		incFailed(name)
		return reconcile.Result{}, err
	}
	incSuccess(name)
	return ctrl.Result{}, nil
}

func (r *TemplateReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.ControllerClient = mgr.GetClient()
	r.Events = mgr.GetEventRecorderFor("template-operator")

	return ctrl.NewControllerManagedBy(mgr).
		For(&templatev1.Template{}).
		Complete(r)
}

func incSuccess(name string) {
	templateCount.WithLabelValues(name).Inc()
	templateSuccess.WithLabelValues(name).Inc()
}

func incFailed(name string) {
	templateCount.WithLabelValues(name).Inc()
	templateFailed.WithLabelValues(name).Inc()
}
