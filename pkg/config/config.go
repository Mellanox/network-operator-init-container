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

// Package config provides configuration types and loading for the init container.
package config

import (
	"fmt"

	"k8s.io/apimachinery/pkg/util/json"
)

// Load parse configuration from the provided string
func Load(config string) (*Config, error) {
	cfg := &Config{}
	if err := json.Unmarshal([]byte(config), cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal configuration: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("configuration is invalid: %v", err)
	}
	return cfg, nil
}

// Config contains configuration for the init container
type Config struct {
	// configuration options for safeDriverLoading feature
	SafeDriverLoad SafeDriverLoadConfig `json:"safeDriverLoad"`
	// configuration options for module dependency checking feature
	ModuleDependencyCheck ModuleDependencyCheckConfig `json:"moduleDependencyCheck"`
}

// ModuleDependencyCheckConfig contains configuration options for module dependency checking feature
type ModuleDependencyCheckConfig struct {
	// enable module dependency checking feature
	Enable bool `json:"enable"`
	// list of MOFED kernel modules to check for external dependencies
	Modules []string `json:"modules"`
	// when true, all known third-party RDMA modules are treated as allowed (driver will handle them)
	UnloadThirdPartyRDMA bool `json:"unloadThirdPartyRdma"`
	// path to the host's /proc filesystem mount inside the container
	HostProcPath string `json:"hostProcPath"`
	// path to the host's /sys filesystem mount inside the container
	HostSysPath string `json:"hostSysPath"`
}

// SafeDriverLoadConfig contains configuration options for safeDriverLoading feature
type SafeDriverLoadConfig struct {
	// enable safeDriverLoading feature
	Enable bool `json:"enable"`
	// annotation to use for safeDriverLoading feature
	Annotation string `json:"annotation"`
}

// Validate checks the configuration
func (c *Config) Validate() error {
	if c.SafeDriverLoad.Enable && c.SafeDriverLoad.Annotation == "" {
		return fmt.Errorf(".safeDriverLoad.annotation is required if safeDriverLoad feature is enabled")
	}
	if c.ModuleDependencyCheck.Enable && len(c.ModuleDependencyCheck.Modules) == 0 {
		return fmt.Errorf(".moduleDependencyCheck.modules is required if moduleDependencyCheck feature is enabled")
	}
	return nil
}

// String returns string representation of the configuration
func (c *Config) String() string {
	//nolint:errchkjson
	data, _ := json.Marshal(c)
	return string(data)
}
