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

	"github.com/caarlos0/env/v11"
	"k8s.io/apimachinery/pkg/util/json"

	"github.com/Mellanox/doca-driver-build/entrypoint/pkg/mofedmodules"
)

// Load parse configuration from the provided string
func Load(config string) (*Config, error) {
	cfg := &Config{}
	if err := json.Unmarshal([]byte(config), cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal configuration: %v", err)
	}
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("failed to parse env vars into configuration: %v", err)
	}
	// Fall back to the shared defaults exported by doca-driver-build so both
	// containers agree on the canonical module lists.
	if len(cfg.ModuleDependencyCheck.StorageModules) == 0 {
		cfg.ModuleDependencyCheck.StorageModules = append(cfg.ModuleDependencyCheck.StorageModules,
			mofedmodules.DefaultStorageModules...)
	}
	if len(cfg.ModuleDependencyCheck.ThirdPartyRDMAModules) == 0 {
		cfg.ModuleDependencyCheck.ThirdPartyRDMAModules = append(cfg.ModuleDependencyCheck.ThirdPartyRDMAModules,
			mofedmodules.DefaultThirdPartyRDMAModules...)
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

// ModuleDependencyCheckConfig contains configuration options for module dependency checking feature.
//
// Module lists and unload flags are populated from environment variables using caarlos0/env/v11,
// mirroring doca-driver-build's env-var-driven pattern. The list of MOFED modules and host
// filesystem paths remain JSON-driven because they are node-level configuration rather than
// user-facing MOFED knobs.
type ModuleDependencyCheckConfig struct {
	// list of MOFED kernel modules to check for external dependencies
	Modules []string `json:"modules"`
	// path to the host's /proc filesystem mount inside the container
	HostProcPath string `json:"hostProcPath"`
	// path to the host's /sys filesystem mount inside the container
	HostSysPath string `json:"hostSysPath"`

	// StorageModules is the list of storage-over-RDMA modules that the driver container
	// is expected to handle when UnloadStorageModules is true. Populated from STORAGE_MODULES
	// (space-separated); defaults to mofedmodules.DefaultStorageModules when unset.
	StorageModules []string `env:"STORAGE_MODULES" envSeparator:" "`
	// ThirdPartyRDMAModules is the list of third-party RDMA NIC-vendor modules that the
	// driver container is expected to handle when UnloadThirdPartyRDMAModules is true.
	// Populated from THIRD_PARTY_RDMA_MODULES (space-separated); defaults to
	// mofedmodules.DefaultThirdPartyRDMAModules when unset.
	ThirdPartyRDMAModules []string `env:"THIRD_PARTY_RDMA_MODULES" envSeparator:" "`
	// UnloadStorageModules, when true, signals that the driver container will unload
	// storage-over-RDMA modules; the pre-flight check treats them as allowed.
	UnloadStorageModules bool `env:"UNLOAD_STORAGE_MODULES" envDefault:"false"`
	// UnloadThirdPartyRDMAModules, when true, signals that the driver container will
	// unload third-party RDMA modules; the pre-flight check treats them as allowed.
	UnloadThirdPartyRDMAModules bool `env:"UNLOAD_THIRD_PARTY_RDMA_MODULES" envDefault:"false"`
	// SkipPreflightChecks controls whether the module dependency check runs. When true
	// (default), the check is skipped and init succeeds immediately. When false, the
	// check runs and any finding returns an error that blocks driver load.
	SkipPreflightChecks bool `env:"SKIP_PREFLIGHT_CHECKS" envDefault:"true"`
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
	return nil
}

// String returns string representation of the configuration
func (c *Config) String() string {
	//nolint:errchkjson
	data, _ := json.Marshal(c)
	return string(data)
}
