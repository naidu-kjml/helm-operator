/*
Copyright 2020 The Operator-SDK Authors.

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

package run

import (
	"flag"
	"os"
	"runtime"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	zapl "sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/joelanford/helm-operator/internal/version"
	"github.com/joelanford/helm-operator/pkg/annotation"
	"github.com/joelanford/helm-operator/pkg/manager"
	"github.com/joelanford/helm-operator/pkg/reconciler"
	"github.com/joelanford/helm-operator/pkg/watches"
)

func NewCmd() *cobra.Command {
	r := run{}
	zapfs := flag.NewFlagSet("zap", flag.ExitOnError)
	opts := &zapl.Options{}
	opts.BindFlags(zapfs)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the operator",
		Run: func(cmd *cobra.Command, _ []string) {
			logf.SetLogger(zapl.New(zapl.UseFlagOptions(opts)))
			r.run(cmd)
		},
	}
	r.bindFlags(cmd.Flags())
	cmd.Flags().AddGoFlagSet(zapfs)
	return cmd
}

type run struct {
	metricsAddr             string
	enableLeaderElection    bool
	leaderElectionID        string
	leaderElectionNamespace string

	watchesFile                    string
	defaultMaxConcurrentReconciles int
	defaultReconcilePeriod         time.Duration
}

func (r *run) bindFlags(fs *pflag.FlagSet) {
	fs.StringVar(&r.metricsAddr, "metrics-addr", ":8080", "The address the metric endpoint binds to.")
	fs.BoolVar(&r.enableLeaderElection, "enable-leader-election", false,
		"Enable leader election for controller manager. Enabling this will ensure there is only one active controller manager.")
	fs.StringVar(&r.leaderElectionID, "leader-election-id", "",
		"Name of the configmap that is used for holding the leader lock.")
	fs.StringVar(&r.leaderElectionNamespace, "leader-election-namespace", "",
		"Namespace in which to create the leader election configmap for holding the leader lock (required if running locally with leader election enabled).")

	fs.StringVar(&r.watchesFile, "watches-file", "./watches.yaml", "Path to watches.yaml file.")
	fs.DurationVar(&r.defaultReconcilePeriod, "reconcile-period", time.Minute, "Default reconcile period for controllers (use 0 to disable periodic reconciliation)")
	fs.IntVar(&r.defaultMaxConcurrentReconciles, "max-concurrent-reconciles", runtime.NumCPU(), "Default maximum number of concurrent reconciles for controllers.")
}

var log = logf.Log.WithName("cmd")

func printVersion() {
	log.Info("Version",
		"Go Version", runtime.Version(),
		"GOOS", runtime.GOOS,
		"GOARCH", runtime.GOARCH,
		"helm-operator", version.Version)
}

func (r *run) run(cmd *cobra.Command) {
	printVersion()

	// Deprecated: OPERATOR_NAME environment variable is an artifact of the legacy operator-sdk project scaffolding.
	//   Flag `--leader-election-id` should be used instead.
	if operatorName, found := os.LookupEnv("OPERATOR_NAME"); found {
		log.Info("environment variable OPERATOR_NAME has been deprecated, use --leader-election-id instead.")
		if cmd.Flags().Lookup("leader-election-id").Changed {
			log.Info("ignoring OPERATOR_NAME environment variable since --leader-election-id is set")
		} else {
			r.leaderElectionID = operatorName
		}
	}

	options := ctrl.Options{
		MetricsBindAddress:      r.metricsAddr,
		LeaderElection:          r.enableLeaderElection,
		LeaderElectionID:        r.leaderElectionID,
		LeaderElectionNamespace: r.leaderElectionNamespace,
		NewClient:               manager.NewDelegatingClientFunc(),
	}
	manager.ConfigureWatchNamespaces(&options, log)
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), options)
	if err != nil {
		log.Error(err, "unable to start manager")
		os.Exit(1)
	}

	ws, err := watches.Load(r.watchesFile)
	if err != nil {
		log.Error(err, "unable to load watches.yaml", "path", r.watchesFile)
		os.Exit(1)
	}

	for _, w := range ws {
		reconcilePeriod := r.defaultReconcilePeriod
		if w.ReconcilePeriod != nil {
			reconcilePeriod = w.ReconcilePeriod.Duration
		}

		maxConcurrentReconciles := r.defaultMaxConcurrentReconciles
		if w.MaxConcurrentReconciles != nil {
			maxConcurrentReconciles = *w.MaxConcurrentReconciles
		}

		r, err := reconciler.New(
			reconciler.WithChart(*w.Chart),
			reconciler.WithGroupVersionKind(w.GroupVersionKind),
			reconciler.WithOverrideValues(w.OverrideValues),
			reconciler.SkipDependentWatches(w.WatchDependentResources != nil && !*w.WatchDependentResources),
			reconciler.WithMaxConcurrentReconciles(maxConcurrentReconciles),
			reconciler.WithReconcilePeriod(reconcilePeriod),
			reconciler.WithInstallAnnotations(annotation.DefaultInstallAnnotations...),
			reconciler.WithUpgradeAnnotations(annotation.DefaultUpgradeAnnotations...),
			reconciler.WithUninstallAnnotations(annotation.DefaultUninstallAnnotations...),
		)
		if err != nil {
			log.Error(err, "unable to create helm reconciler", "controller", "Helm")
			os.Exit(1)
		}

		if err := r.SetupWithManager(mgr); err != nil {
			log.Error(err, "unable to create controller", "controller", "Helm")
			os.Exit(1)
		}
		log.Info("configured watch", "gvk", w.GroupVersionKind, "chartPath", w.ChartPath, "maxConcurrentReconciles", maxConcurrentReconciles, "reconcilePeriod", reconcilePeriod)
	}

	log.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "problem running manager")
		os.Exit(1)
	}
}
