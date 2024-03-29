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
	"encoding/json"
	"reflect"
	"strings"
	"time"

	"github.com/flanksource/commons/utils"
	templatev1 "github.com/flanksource/template-operator/api/v1"
	"github.com/flanksource/template-operator/k8s"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	objectModifiedError = "the object has been modified; please apply your changes to the latest version and try again"
)

var (
	RESTDeleteFinalizer = "termination.flanksource.com/protect"
)

var (
	restCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "template_operator_rest_count",
			Help: "Total rest runs count",
		},
		[]string{"rest"},
	)
	restSuccess = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "template_operator_rest_success",
			Help: "Total successful rest runs count",
		},
		[]string{"rest"},
	)
	restFailed = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "template_operator_rest_failed",
			Help: "Total failed rest runs count",
		},
		[]string{"test"},
	)
)

func init() {
	metrics.Registry.MustRegister(restCount, restSuccess, restFailed)
}

// RESTReconciler reconciles a REST object
type RESTReconciler struct {
	Client
}

// +kubebuilder:rbac:groups="*",resources="*",verbs="*"

func (r *RESTReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("rest", req.NamespacedName, "requestID", utils.RandomString(10))
	name := req.NamespacedName.String()

	log.V(2).Info("Started reconciling")

	rest := &templatev1.REST{}
	if err := r.ControllerClient.Get(ctx, req.NamespacedName, rest); err != nil {
		if kerrors.IsNotFound(err) {
			log.Error(err, "rest not found")
			return reconcile.Result{}, nil
		}
		log.Error(err, "failed to get template")
		incRESTFailed(name)
		return reconcile.Result{}, err
	}

	if rest.Status == nil {
		rest.Status = map[string]string{}
	}
	oldStatus := cloneMap(rest.Status)

	//If the TemplateManager will fetch a new schema, ensure the kommons.client also does so in order to ensure they contain the same information
	if r.Cache.SchemaHasExpired() {
		r.KommonsClient.ResetRestMapper()
	}
	tm, err := k8s.NewRESTManager(r.KommonsClient, log)
	if err != nil {
		incRESTFailed(name)
		return reconcile.Result{}, err
	}

	hasFinalizer := false
	for _, finalizer := range rest.ObjectMeta.Finalizers {
		if finalizer == RESTDeleteFinalizer {
			hasFinalizer = true
		}
	}

	if rest.ObjectMeta.DeletionTimestamp != nil {
		log.V(2).Info("Object marked as deleted")
		if err = tm.Delete(ctx, rest); err != nil {
			return reconcile.Result{}, err
		}
		if err := r.removeFinalizers(rest); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	if !hasFinalizer {
		log.V(2).Info("Setting finalizer")
		rest.ObjectMeta.Finalizers = append(rest.ObjectMeta.Finalizers, RESTDeleteFinalizer)
		if err := r.ControllerClient.Update(ctx, rest); err != nil {
			log.Error(err, "failed to add finalizer to object")
			return ctrl.Result{}, err
		}
		log.V(2).Info("Finalizer set, exiting reconcile")

		return ctrl.Result{}, nil
	}

	statusUpdates, err := tm.Update(ctx, rest)
	if err != nil {
		log.Error(err, "Failed to run update REST")
		incRESTFailed(name)
		return reconcile.Result{}, err
	}

	if err := r.updateStatus(ctx, rest, statusUpdates, oldStatus); err != nil {
		return reconcile.Result{}, err
	}

	incRESTSuccess(name)
	log.V(2).Info("Finished reconciling", "generation", rest.ObjectMeta.Generation)
	return ctrl.Result{}, nil
}

func (r *RESTReconciler) updateStatus(ctx context.Context, rest *templatev1.REST, statusUpdates, oldStatus map[string]string) error {
	backoff := wait.Backoff{
		Duration: 50 * time.Millisecond,
		Factor:   1.5,
		Jitter:   2,
		Steps:    10,
		Cap:      5 * time.Second,
	}
	var err error

	r.addStatusUpdates(rest, statusUpdates)

	if reflect.DeepEqual(rest.Status, oldStatus) {
		r.Log.V(2).Info("REST status did not change, skipping")
		return nil
	}

	setRestStatus(rest)

	js, _ := json.Marshal(rest.Status)
	js2, _ := json.Marshal(oldStatus)
	r.Log.V(2).Info("Checking:", "status", string(js), "oldStatus", string(js2))

	for backoff.Steps > 0 {
		js, err := json.Marshal(statusUpdates)
		r.Log.V(2).Info("Updating status: setting", "statusUpdates", string(js), "err", err)
		if err = r.ControllerClient.Status().Update(ctx, rest); err == nil {
			return nil
		}
		sleepDuration := backoff.Step()
		r.Log.Info("update status failed, sleeping", "duration", sleepDuration, "err", err)
		time.Sleep(sleepDuration)
		if strings.Contains(err.Error(), objectModifiedError) {
			if err := r.ControllerClient.Get(context.Background(), types.NamespacedName{Name: rest.Name}, rest); err != nil {
				return errors.Wrap(err, "failed to refetch object")
			}
			r.addStatusUpdates(rest, statusUpdates)
			if reflect.DeepEqual(rest.Status, oldStatus) {
				return nil
			}
			setRestStatus(rest)
		}
	}

	return err
}

func (r *RESTReconciler) removeFinalizers(rest *templatev1.REST) error {
	backoff := wait.Backoff{
		Duration: 50 * time.Millisecond,
		Factor:   1.5,
		Jitter:   2,
		Steps:    10,
		Cap:      5 * time.Second,
	}
	var err error

	rest.ObjectMeta.Finalizers = r.removeFinalizer(rest)

	for backoff.Steps > 0 {
		if err = r.ControllerClient.Update(context.Background(), rest); err == nil {
			return nil
		}
		sleepDuration := backoff.Step()
		r.Log.Info("remove finalizers failed, sleeping", "duration", sleepDuration, "err", err)
		time.Sleep(sleepDuration)
		if strings.Contains(err.Error(), objectModifiedError) {
			if err := r.ControllerClient.Get(context.Background(), types.NamespacedName{Name: rest.Name}, rest); err != nil {
				return errors.Wrap(err, "failed to refetch object")
			}
			rest.ObjectMeta.Finalizers = r.removeFinalizer(rest)
		}
	}

	return nil
}

func (r *RESTReconciler) removeFinalizer(rest *templatev1.REST) []string {
	finalizers := []string{}
	for _, finalizer := range rest.ObjectMeta.Finalizers {
		if finalizer != RESTDeleteFinalizer {
			finalizers = append(finalizers, finalizer)
		}
	}
	return finalizers
}

func (r *RESTReconciler) addStatusUpdates(rest *templatev1.REST, statusUpdates map[string]string) {
	if rest.Status == nil {
		rest.Status = map[string]string{}
	}
	for k, v := range statusUpdates {
		rest.Status[k] = v
	}
}

func (r *RESTReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.ControllerClient = mgr.GetClient()
	r.Events = mgr.GetEventRecorderFor("template-operator")

	return ctrl.NewControllerManagedBy(mgr).
		For(&templatev1.REST{}).
		Complete(r)
}

func incRESTSuccess(name string) {
	restCount.WithLabelValues(name).Inc()
	restSuccess.WithLabelValues(name).Inc()
}

func incRESTFailed(name string) {
	restCount.WithLabelValues(name).Inc()
	restFailed.WithLabelValues(name).Inc()
}

func setRestStatus(rest *templatev1.REST) {
	rest.Status["lastUpdated"] = metav1.Now().String()
}

func cloneMap(m map[string]string) map[string]string {
	x := map[string]string{}
	for k, v := range m {
		x[k] = v
	}
	return x
}
