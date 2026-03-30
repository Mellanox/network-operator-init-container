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

// Package options provides configuration options for the init container command.
package options

import (
	goflag "flag"
	"fmt"

	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/logs"
	logsapi "k8s.io/component-base/logs/api/v1"
	ctrl "sigs.k8s.io/controller-runtime"
)

// New creates new Options
func New() *Options {
	return &Options{
		LogConfig: logsapi.NewLoggingConfiguration(),
	}
}

// Options contains application options
type Options struct {
	NodeName           string
	ConfigMapName      string
	ConfigMapNamespace string
	ConfigMapKey       string
	LogConfig          *logsapi.LoggingConfiguration
}

// AddNamedFlagSets returns FlagSet for Options
func (o *Options) AddNamedFlagSets(sharedFS *cliflag.NamedFlagSets) {
	configFS := sharedFS.FlagSet("Config")
	configFS.StringVar(&o.NodeName, "node-name", "",
		"name of the k8s node on which this app runs")
	configFS.StringVar(&o.ConfigMapName, "configmap-name", "",
		"name of the configmap with configuration for the app")
	configFS.StringVar(&o.ConfigMapNamespace, "configmap-namespace", "",
		"namespace of the configmap with configuration for the app")
	configFS.StringVar(&o.ConfigMapKey, "configmap-key", "config.json",
		"key inside the configmap with configuration for the app")

	logFS := sharedFS.FlagSet("Logging")
	logsapi.AddFlags(o.LogConfig, logFS)
	logs.AddFlags(logFS, logs.SkipLoggingConfigurationFlags())

	generalFS := sharedFS.FlagSet("General")
	_ = generalFS.Bool("version", false, "print version and exit")
	_ = generalFS.BoolP("help", "h", false, "print help and exit")

	kubernetesFS := sharedFS.FlagSet("Kubernetes")

	goFS := goflag.NewFlagSet("tmp", goflag.ContinueOnError)
	ctrl.RegisterFlags(goFS)
	kubernetesFS.AddGoFlagSet(goFS)
}

// Validate registered options
func (o *Options) Validate() error {
	var err error

	if o.NodeName == "" {
		return fmt.Errorf("node-name is required parameter")
	}

	if o.ConfigMapName == "" {
		return fmt.Errorf("configmap-name is required parameter")
	}

	if o.ConfigMapNamespace == "" {
		return fmt.Errorf("configmap-namespace is required parameter")
	}

	if o.ConfigMapKey == "" {
		return fmt.Errorf("configmap-key is required parameter")
	}

	if err = logsapi.ValidateAndApply(o.LogConfig, nil); err != nil {
		return fmt.Errorf("failed to validate logging flags. %w", err)
	}
	return err
}
