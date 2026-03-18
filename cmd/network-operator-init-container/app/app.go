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
	"k8s.io/component-base/term"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	// register json format for logger
	_ "k8s.io/component-base/logs/json/register"

	"github.com/Mellanox/network-operator-init-container/cmd/network-operator-init-container/app/options"
	configPgk "github.com/Mellanox/network-operator-init-container/pkg/config"
	"github.com/Mellanox/network-operator-init-container/pkg/modules"
	"github.com/Mellanox/network-operator-init-container/pkg/utils/version"
)

// NewNetworkOperatorInitContainerCommand creates a new command
func NewNetworkOperatorInitContainerCommand() *cobra.Command {
	opts := options.New()
	ctx := ctrl.SetupSignalHandler()

	cmd := &cobra.Command{
		Use:          "network-operator-init-container",
		Long:         `NVIDIA Network Operator init container`,
		SilenceUsage: true,
		Version:      version.GetVersionString(),
		RunE: func(cmd *cobra.Command, args []string) error {
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
				if len(arg) > 0 {
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

	// Module dependency check — fail fast before safe driver loading
	if initContCfg.ModuleDependencyCheck.Enable {
		logger.Info("running module dependency check",
			"modules", initContCfg.ModuleDependencyCheck.Modules)

		procPath := initContCfg.ModuleDependencyCheck.HostProcPath
		if procPath == "" {
			procPath = "/proc"
		}
		sysPath := initContCfg.ModuleDependencyCheck.HostSysPath
		if sysPath == "" {
			sysPath = "/sys"
		}

		if len(initContCfg.ModuleDependencyCheck.AllowedModules) > 0 {
			logger.Info("allowed modules (will not block driver loading)",
				"allowedModules", initContCfg.ModuleDependencyCheck.AllowedModules)
		}

		checker := modules.NewChecker(
			initContCfg.ModuleDependencyCheck.Modules,
			initContCfg.ModuleDependencyCheck.AllowedModules,
			procPath, sysPath, logger)

		deps, err := checker.CheckDependencies(ctx)
		if err != nil {
			return fmt.Errorf("module dependency check failed: %w", err)
		}

		userspaceIssues, err := checker.CheckUserspaceUsers(ctx)
		if err != nil {
			return fmt.Errorf("userspace user check failed: %w", err)
		}

		totalIssues := len(deps) + len(userspaceIssues)
		if totalIssues > 0 {
			logger.Error(nil, "ERROR: Pre-flight check failed — cannot safely reload MOFED drivers")

			if len(deps) > 0 {
				logger.Error(nil, "Blocking kernel module dependencies:")
				for _, dep := range deps {
					logger.Error(nil, fmt.Sprintf("  - %s is used by: %s (not in allowedModules)",
						dep.MofedModule, strings.Join(dep.Dependents, ", ")))
				}
				logger.Error(nil, "  Actions:")
				logger.Error(nil, "    1. Add these modules to UNLOAD_CUSTOM_MODULES in NicClusterPolicy env vars")
				logger.Error(nil, "    2. Or unload/remove these modules from the host before deploying DOCA driver")
			}

			if len(userspaceIssues) > 0 {
				logger.Error(nil, "Userspace processes holding MOFED modules:")
				for _, issue := range userspaceIssues {
					holderStr := ""
					if len(issue.Holders) > 0 {
						holderStr = fmt.Sprintf(" (%s)", strings.Join(issue.Holders, ", "))
					}
					logger.Error(nil, fmt.Sprintf("  - %s: refcount=%d, kernel holders=%d%s, %d unknown userspace reference(s)",
						issue.Module, issue.Refcount, issue.HolderCount, holderStr, issue.UserspaceCount))
				}
				logger.Error(nil, "  Actions:")
				logger.Error(nil, "    1. Run on the host: lsof /dev/infiniband/* or fuser /dev/infiniband/*")
				logger.Error(nil, "    2. Stop the identified process(es) before deploying DOCA driver")
				logger.Error(nil, "    3. Common culprits: opensm, ibacm, rdma-ndd")
			}

			return fmt.Errorf("pre-flight check found %d issue(s); cannot safely reload MOFED drivers", totalIssues)
		}
		logger.Info("module dependency check passed")
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

	node := &corev1.Node{}
	err = k8sClient.Get(ctx, types.NamespacedName{Name: opts.NodeName}, node)
	if err != nil {
		logger.Error(err, "failed to read node object from the API", "node", opts.NodeName)
		return err
	}
	err = k8sClient.Patch(ctx, node, client.RawPatch(
		types.MergePatchType, []byte(
			fmt.Sprintf(`{"metadata":{"annotations":{%q: %q}}}`,
				initContCfg.SafeDriverLoad.Annotation, "true"))))
	if err != nil {
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

	return ctrl.Result{RequeueAfter: time.Second * 5}, nil
}

func writeCh(ch chan error, err error) {
	select {
	case ch <- err:
	default:
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Node{}).
		Complete(r)
}
