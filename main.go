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

package main

import (
	"flag"
	"os"
	"time"

	"github.com/flanksource/commons/logger"
	"github.com/flanksource/kommons"
	templatingflanksourcecomv1 "github.com/flanksource/template-operator/api/v1"
	"github.com/flanksource/template-operator/controllers"
	"github.com/flanksource/template-operator/k8s"
	zaplogfmt "github.com/sykesm/zap-logfmt"
	uzap "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	_ = clientgoscheme.AddToScheme(scheme)

	_ = templatingflanksourcecomv1.AddToScheme(scheme)
	// +kubebuilder:scaffold:scheme
}

func setupLogger(opts zap.Options) {
	configLog := uzap.NewProductionEncoderConfig()
	configLog.EncodeTime = func(ts time.Time, encoder zapcore.PrimitiveArrayEncoder) {
		encoder.AppendString(ts.UTC().Format(time.RFC3339Nano))
	}
	logfmtEncoder := zaplogfmt.NewEncoder(configLog)

	logger := zap.New(zap.UseFlagOptions(&opts), zap.Encoder(logfmtEncoder))
	ctrl.SetLogger(logger)
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var syncPeriod, expire time.Duration
	flag.DurationVar(&syncPeriod, "sync-period", 5*time.Minute, "The time duration to run a full reconcile")
	flag.DurationVar(&expire, "expire", 15*time.Minute, "The time duration to expire API resources cache")
	flag.StringVar(&metricsAddr, "metrics-addr", ":8080", "The address the metric endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "enable-leader-election", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")

	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	setupLogger(opts)

	setupLog.Info("Settings:", "sync-period", syncPeriod.Seconds())

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:             scheme,
		MetricsBindAddress: metricsAddr,
		Port:               9443,
		SyncPeriod:         &syncPeriod,
		LeaderElection:     enableLeaderElection,
		LeaderElectionID:   "ba344e13.flanksource.com",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	client := kommons.NewClient(mgr.GetConfig(), logger.StandardLogger())
	clientset, err := client.GetClientset()
	if err != nil {
		setupLog.Error(err, "failed to get clientset")
		os.Exit(1)
	}
	schemaCache := k8s.NewSchemaCache(clientset, expire, ctrl.Log.WithName("schema-cache"))

	if err = (&controllers.TemplateReconciler{
		Client: client,
		Cache:  schemaCache,
		Log:    ctrl.Log.WithName("controllers").WithName("Template"),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Template")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
