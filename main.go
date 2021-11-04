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
	"context"
	"errors"
	"os"
	"time"

	apiv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"

	"github.com/flanksource/commons/logger"
	"github.com/flanksource/kommons"
	templatingflanksourcecomv1 "github.com/flanksource/template-operator/api/v1"
	"github.com/flanksource/template-operator/controllers"
	"github.com/flanksource/template-operator/k8s"
	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v2"
	apiv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	extapi "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlzap "sigs.k8s.io/controller-runtime/pkg/log/zap"
	// +kubebuilder:scaffold:imports
)

var (
	scheme     = runtime.NewScheme()
	setupLog   = ctrl.Log.WithName("setup")
	ctrlLogger logr.Logger
)

func init() {
	_ = clientgoscheme.AddToScheme(scheme)

	_ = templatingflanksourcecomv1.AddToScheme(scheme)
	apiv1.AddToScheme(scheme)
	apiv1beta1.AddToScheme(scheme)
	// +kubebuilder:scaffold:scheme

	yaml.FutureLineWrap()
}

func setupLogger(cmd *cobra.Command, args []string) error {
	zapLogger := logger.GetZapLogger()
	if zapLogger == nil {
		logger.Fatalf("failed to get zap logger")
		return errors.New("failed to get zap logger")
	}

	loggr := ctrlzap.NewRaw(
		ctrlzap.UseDevMode(true),
		ctrlzap.WriteTo(os.Stderr),
		ctrlzap.Level(zapLogger.Level),
		ctrlzap.StacktraceLevel(zapLogger.StackTraceLevel),
		ctrlzap.Encoder(zapLogger.GetEncoder()),
	)

	ctrlLogger = zapr.NewLogger(loggr)
	ctrl.SetLogger(ctrlLogger)

	return nil
}

func serve(cmd *cobra.Command, args []string) {
	if err := setupLogger(cmd, args); err != nil {
		logger.Fatalf("failed to setup logger: %v", err)
	}

	metricsAddr, _ := cmd.Flags().GetString("metrics-addr")
	syncPeriod, _ := cmd.Flags().GetDuration("sync-period")
	expire, _ := cmd.Flags().GetDuration("expire")
	enableLeaderElection, _ := cmd.Flags().GetBool("enable-leader-election")

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
	restConfig, err := client.GetRESTConfig()
	if err != nil {
		setupLog.Error(err, "failed to get rest config")
		os.Exit(1)
	}
	crdClient, err := extapi.NewForConfig(restConfig)
	if err != nil {
		setupLog.Error(err, "failed to get crd client")
		os.Exit(1)
	}
	schemaCache := k8s.NewSchemaCache(clientset, crdClient, expire, ctrl.Log.WithName("schema-cache"))

	watcher, err := k8s.NewWatcher(client, ctrl.Log.WithName("watcher"))
	if err != nil {
		setupLog.Error(err, "failed to setup watcher")
		os.Exit(1)
	}

	if err = (&controllers.TemplateReconciler{
		Client: controllers.Client{
			KommonsClient: client,
			Cache:         schemaCache,
			Log:           ctrl.Log.WithName("controllers").WithName("Template"),
			Scheme:        mgr.GetScheme(),
			Watcher:       watcher,
		},
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Template")
		os.Exit(1)
	}
	//CRDReconciler shares a SchemaCache with TemplateReconciler, and resets it if changes to CRDs are reported, so that the TemplateReconciler will pick them up
	if err = (&controllers.CRDReconciler{
		Client: controllers.Client{
			KommonsClient: client,
			Cache:         schemaCache,
			Log:           ctrl.Log.WithName("controllers").WithName("Template"),
			Scheme:        mgr.GetScheme(),
		},
		ResourceVersion: 0,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Template")
		os.Exit(1)
	}
	if err = (&controllers.RESTReconciler{
		Client: controllers.Client{
			KommonsClient: client,
			Cache:         schemaCache,
			Log:           ctrl.Log.WithName("controllers").WithName("Template"),
			Scheme:        mgr.GetScheme(),
			Watcher:       watcher,
		},
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "REST")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func run(cmd *cobra.Command, args []string) {
	template, _ := cmd.Flags().GetString("template")
	obj, _ := cmd.Flags().GetString("obj")
	expire := 15 * time.Minute

	client, err := kommons.NewClientFromDefaults(logger.StandardLogger())
	if err != nil {
		setupLog.Error(err, "failed to create client")
		os.Exit(1)
	}
	clientset, err := client.GetClientset()
	if err != nil {
		setupLog.Error(err, "failed to get clientset")
		os.Exit(1)
	}
	restConfig, err := client.GetRESTConfig()
	if err != nil {
		setupLog.Error(err, "failed to get rest config")
		os.Exit(1)
	}
	crdClient, err := extapi.NewForConfig(restConfig)
	if err != nil {
		setupLog.Error(err, "failed to get crd client")
		os.Exit(1)
	}
	schemaCache := k8s.NewSchemaCache(clientset, crdClient, expire, ctrl.Log.WithName("schema-cache"))

	watcher, err := k8s.NewWatcher(client, ctrl.Log.WithName("watcher"))
	if err != nil {
		setupLog.Error(err, "failed to setup watcher")
		os.Exit(1)
	}

	tm, err := k8s.NewTemplateManager(client, ctrlLogger, schemaCache, &k8s.NullEventRecorder{}, watcher)
	if err != nil {
		setupLog.Error(err, "failed to create template manager")
		os.Exit(1)
	}

	if _, err := tm.RunOnce(context.Background(), template, obj); err != nil {
		setupLog.Error(err, "failed to run template")
		os.Exit(1)
	}
}

func main() {
	var rootCmd = &cobra.Command{
		Use:   "template-operator",
		Short: "The Template Operator is for platform engineers needing an easy and reliable way to create, copy and update kubernetes resources.",
		Long:  `The Template Operator is for platform engineers needing an easy and reliable way to create, copy and update kubernetes resources.`,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			logger.UseZap(cmd.Flags())
			return setupLogger(cmd, args)
		},
	}
	// Set Development mode value
	rootCmd.PersistentFlags().Bool("json-logs", false, "Enable json logging")
	rootCmd.PersistentFlags().String("loglevel", "info",
		"Zap Level to configure the verbosity of logging. Can be one of 'debug', 'info', 'error', "+
			"or any integer value > 0 which corresponds to custom debug levels of increasing verbosity")

	serveCmd := &cobra.Command{
		Use:   "serve",
		Short: "Run kubernetes controller",
		Long:  "Run kubernetes controller",
		Run:   serve,
	}
	serveCmd.Flags().Duration("sync-period", 5*time.Minute, "The time duration to run a full reconcile")
	serveCmd.Flags().Duration("expire", 15*time.Minute, "The time duration to expire API resources cache")
	serveCmd.Flags().String("metrics-addr", ":8080", "The address the metric endpoint binds to.")
	serveCmd.Flags().Bool("enable-leader-election", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	rootCmd.AddCommand(serveCmd)

	runCmd := &cobra.Command{
		Use:   "run",
		Short: "Execute template locally",
		Long:  "Execute template locally",
		Run:   run,
	}
	runCmd.Flags().String("template", "", "The template to run")
	runCmd.Flags().String("obj", "", "The object used as source for template")
	rootCmd.AddCommand(runCmd)

	if err := rootCmd.Execute(); err != nil {
		setupLog.Error(err, "problem running root command")
		os.Exit(1)
	}
}
