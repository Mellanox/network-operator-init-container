/*
 Copyright 2023, NVIDIA CORPORATION & AFFILIATES
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

// Package app provides the command and main loop for the network-operator-init-container.
package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	cliflag "k8s.io/component-base/cli/flag"
	_ "k8s.io/component-base/logs/json/register" // register json format for logger
	"k8s.io/component-base/term"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/Mellanox/network-operator-init-container/cmd/network-operator-init-container/app/options"
	configPgk "github.com/Mellanox/network-operator-init-container/pkg/config"
	"github.com/Mellanox/network-operator-init-container/pkg/modules"
	"github.com/Mellanox/network-operator-init-container/pkg/utils/version"
)

const requeueInterval = 5 * time.Second

// NewNetworkOperatorInitContainerCommand creates a new command
func NewNetworkOperatorInitContainerCommand() *cobra.Command {
	opts := options.New()
	ctx := ctrl.SetupSignalHandler()

	cmd := &cobra.Command{
		Use:          "network-operator-init-container",
		Long:         `NVIDIA Network Operator init container`,
		SilenceUsage: true,
		Version:      version.GetVersionString(),
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := opts.Validate(); err != nil {
				return fmt.Errorf("invalid config: %w", err)
			}
			conf, err := ctrl.GetConfig()
			if err != nil {
				return fmt.Errorf("failed to read config for k8s client: %v", err)
			}
			return RunNetworkOperatorInitContainer(logr.NewContext(ctx, klog.NewKlogr()), conf, opts)
		},
		Args: func(cmd *cobra.Command, args []string) error {
			for _, arg := range args {
				if arg != "" {
					return fmt.Errorf("%q does not take any arguments, got %q", cmd.CommandPath(), args)
				}
			}

			return nil
		},
	}

	sharedFS := cliflag.NamedFlagSets{}
	opts.AddNamedFlagSets(&sharedFS)

	cmdFS := cmd.PersistentFlags()
	for _, f := range sharedFS.FlagSets {
		cmdFS.AddFlagSet(f)
	}

	cols, _, _ := term.TerminalSize(cmd.OutOrStdout())
	cliflag.SetUsageAndHelpFunc(cmd, sharedFS, cols)

	return cmd
}

// RunNetworkOperatorInitContainer runs init container main loop
func RunNetworkOperatorInitContainer(ctx context.Context, config *rest.Config, opts *options.Options) error {
	logger := logr.FromContextOrDiscard(ctx)
	ctx, cFunc := context.WithCancel(ctx)
	defer cFunc()
	logger.Info("start network-operator-init-container",
		"Options", opts, "Version", version.GetVersionString())
	ctrl.SetLogger(logger)

	mgr, err := ctrl.NewManager(config, ctrl.Options{
		Metrics: metricsserver.Options{BindAddress: "0"},
		Cache: cache.Options{
			ByObject: map[client.Object]cache.ByObject{
				&corev1.Node{}: {Field: fields.ParseSelectorOrDie(
					fmt.Sprintf("metadata.name=%s", opts.NodeName))}}},
	})
	if err != nil {
		logger.Error(err, "unable to create manager")
		return err
	}

	k8sClient, err := client.New(config,
		client.Options{Scheme: mgr.GetScheme(), Mapper: mgr.GetRESTMapper()})
	if err != nil {
		logger.Error(err, "failed to create k8sClient client")
		return err
	}

	confConfigMap := &corev1.ConfigMap{}

	err = k8sClient.Get(ctx, client.ObjectKey{
		Name:      opts.ConfigMapName,
		Namespace: opts.ConfigMapNamespace,
	}, confConfigMap)

	if err != nil {
		logger.Error(err, "failed to read config map with configuration")
		return err
	}

	initContCfg, err := configPgk.Load(confConfigMap.Data[opts.ConfigMapKey])
	if err != nil {
		logger.Error(err, "failed to read configuration")
		return err
	}
	logger.Info("network-operator-init-container configuration", "config", initContCfg.String())

	// Module dependency check — skipped by default. When SKIP_PREFLIGHT_CHECKS=false,
	// the check runs and any finding returns an error that blocks driver load.
	if err := runModuleDependencyCheck(ctx, initContCfg, logger); err != nil {
		return err
	}

	if !initContCfg.SafeDriverLoad.Enable {
		logger.Info("safe driver loading is disabled, exit")
		return nil
	}

	errCh := make(chan error, 1)

	if err = (&NodeReconciler{
		ErrCh:              errCh,
		SafeLoadAnnotation: initContCfg.SafeDriverLoad.Annotation,
		Client:             mgr.GetClient(),
		Scheme:             mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		logger.Error(err, "unable to create controller", "controller", "Node")
		return err
	}

	if err = setNodeAnnotation(ctx, k8sClient, opts.NodeName, initContCfg.SafeDriverLoad.Annotation); err != nil {
		logger.Error(err, "unable to set annotation for node", "node", opts.NodeName)
		return err
	}

	logger.Info("wait for annotation to be removed",
		"annotation", initContCfg.SafeDriverLoad.Annotation, "node", opts.NodeName)

	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := mgr.Start(ctx); err != nil {
			logger.Error(err, "problem running manager")
			writeCh(errCh, err)
		}
	}()
	defer wg.Wait()
	select {
	case <-ctx.Done():
		return fmt.Errorf("waiting canceled")
	case err = <-errCh:
		cFunc()
		return err
	}
}

// NodeReconciler reconciles Node object
type NodeReconciler struct {
	ErrCh              chan error
	SafeLoadAnnotation string
	client.Client
	Scheme *runtime.Scheme
}

// Reconcile contains logic to sync Node object
func (r *NodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	reqLog := log.FromContext(ctx).WithValues("annotation", r.SafeLoadAnnotation)

	node := &corev1.Node{}
	err := r.Client.Get(ctx, req.NamespacedName, node)
	if err != nil {
		if apiErrors.IsNotFound(err) {
			reqLog.Info("Node object not found, exit")
			writeCh(r.ErrCh, err)
			return ctrl.Result{}, err
		}
		reqLog.Error(err, "failed to get Node object from the cache")
		writeCh(r.ErrCh, err)
		return ctrl.Result{}, err
	}

	if node.GetAnnotations()[r.SafeLoadAnnotation] == "" {
		reqLog.Info("annotation removed, unblock loading")
		writeCh(r.ErrCh, nil)
		return ctrl.Result{}, nil
	}
	reqLog.Info("annotation still present, waiting")

	return ctrl.Result{RequeueAfter: requeueInterval}, nil
}

func setNodeAnnotation(ctx context.Context, k8sClient client.Client, nodeName, annotation string) error {
	node := &corev1.Node{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
		return fmt.Errorf("failed to read node object from the API: %w", err)
	}
	return k8sClient.Patch(ctx, node, client.RawPatch(
		types.MergePatchType, []byte(
			fmt.Sprintf(`{"metadata":{"annotations":{%q: %q}}}`,
				annotation, "true"))))
}

func writeCh(ch chan error, err error) {
	select {
	case ch <- err:
	default:
	}
}

// runModuleDependencyCheck performs the module dependency pre-flight check and returns
// an error if any issues are found. When SkipPreflightChecks is true (the default), the
// check is skipped entirely and the function returns nil.
func runModuleDependencyCheck(ctx context.Context, initContCfg *configPgk.Config, logger logr.Logger) error {
	if initContCfg.ModuleDependencyCheck.SkipPreflightChecks {
		logger.Info("SKIP_PREFLIGHT_CHECKS=true; skipping module dependency check")
		return nil
	}

	logger.Info("running module dependency check",
		"modules", initContCfg.ModuleDependencyCheck.Modules)

	if len(initContCfg.ModuleDependencyCheck.Modules) == 0 {
		logger.Info("no MOFED modules configured for pre-flight dependency check, skipping")
		return nil
	}

	procPath := initContCfg.ModuleDependencyCheck.HostProcPath
	if procPath == "" {
		procPath = "/proc"
	}
	sysPath := initContCfg.ModuleDependencyCheck.HostSysPath
	if sysPath == "" {
		sysPath = "/sys"
	}

	if initContCfg.ModuleDependencyCheck.UnloadThirdPartyRDMAModules {
		logger.Info("UNLOAD_THIRD_PARTY_RDMA_MODULES is enabled; known third-party RDMA modules will be skipped")
	}
	if initContCfg.ModuleDependencyCheck.UnloadStorageModules {
		logger.Info("UNLOAD_STORAGE_MODULES is enabled; known storage modules will be skipped")
	}

	checker := modules.NewChecker(
		initContCfg.ModuleDependencyCheck.Modules,
		initContCfg.ModuleDependencyCheck.ThirdPartyRDMAModules,
		initContCfg.ModuleDependencyCheck.StorageModules,
		initContCfg.ModuleDependencyCheck.UnloadThirdPartyRDMAModules,
		initContCfg.ModuleDependencyCheck.UnloadStorageModules,
		procPath, sysPath, logger)

	report, err := checker.RunAllChecks(ctx)
	if err != nil {
		return fmt.Errorf("module dependency check failed: %w", err)
	}

	return reportPreFlightIssues(logger, report)
}

// reportPreFlightIssues logs all pre-flight check issues and returns an error if any were found.
func reportPreFlightIssues(logger logr.Logger, report *modules.DependencyReport) error {
	totalIssues := len(report.ThirdPartyRDMA) + len(report.StorageModules) +
		len(report.UnknownKernelModules) + len(report.UserspaceIssues)
	if totalIssues == 0 {
		return nil
	}

	// Category 1: known third-party RDMA modules (automatable)
	if len(report.ThirdPartyRDMA) > 0 {
		for _, dep := range report.ThirdPartyRDMA {
			logger.Error(fmt.Errorf("third-party RDMA module dependency"),
				"third-party RDMA module blocking MOFED driver reload",
				"mofedModule", dep.MofedModule,
				"dependents", strings.Join(dep.Dependents, ", "))
		}
		logger.Error(fmt.Errorf("third-party RDMA modules require configuration change"),
			"Recommended action: set UNLOAD_THIRD_PARTY_RDMA_MODULES=\"true\" in "+
				"NicClusterPolicy ofedDriver env vars to automatically unload known third-party "+
				"RDMA modules before driver reload. Verify that no running workloads depend on "+
				"these modules before enabling.")
	}

	// Category 1b: known storage-over-RDMA modules (automatable via UNLOAD_STORAGE_MODULES)
	if len(report.StorageModules) > 0 {
		for _, dep := range report.StorageModules {
			logger.Error(fmt.Errorf("storage module dependency"),
				"storage-over-RDMA module blocking MOFED driver reload",
				"mofedModule", dep.MofedModule,
				"dependents", strings.Join(dep.Dependents, ", "))
		}
		logger.Error(fmt.Errorf("storage modules require configuration change"),
			"Recommended action: set UNLOAD_STORAGE_MODULES=\"true\" in "+
				"NicClusterPolicy ofedDriver env vars to automatically unload known "+
				"storage-over-RDMA modules before driver reload. Verify that no running "+
				"workloads depend on these modules before enabling.")
	}

	// Category 2: unknown kernel modules (error level — manual intervention)
	if len(report.UnknownKernelModules) > 0 {
		for _, dep := range report.UnknownKernelModules {
			logger.Error(fmt.Errorf("unknown kernel module dependency"),
				"unrecognized module(s) blocking MOFED driver reload",
				"mofedModule", dep.MofedModule,
				"dependents", strings.Join(dep.Dependents, ", "))
		}
		logger.Error(fmt.Errorf("unknown kernel modules require manual intervention"),
			"Required action: manually unload or blacklist these modules "+
				"before deploying the DOCA driver. Automatic unloading is not supported "+
				"for unrecognized modules.")
	}

	// Category 3: userspace processes (error level — manual intervention)
	if len(report.UserspaceIssues) > 0 {
		for _, issue := range report.UserspaceIssues {
			logger.Error(fmt.Errorf("userspace process holding module"),
				"userspace reference(s) blocking MOFED module unload",
				"module", issue.Module,
				"refcount", issue.Refcount,
				"kernelHolders", issue.HolderCount,
				"holders", strings.Join(issue.Holders, ", "),
				"userspaceRefs", issue.UserspaceCount)
		}
		logger.Error(fmt.Errorf("userspace processes require manual intervention"),
			"Required action: identify and stop processes using MOFED modules. "+
				"Run on host: lsof /dev/infiniband/* or fuser -v /dev/infiniband/*. "+
				"Common culprits: opensm, ibacm, rdma-ndd, srpd")
	}

	return fmt.Errorf("pre-flight check found %d issue(s); cannot safely reload MOFED drivers", totalIssues)
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Node{}).
		Complete(r)
}
