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

package config_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	configPgk "github.com/Mellanox/network-operator-init-container/pkg/config"
)

func createConfig(cfg *configPgk.Config) string {
	data, err := json.Marshal(cfg)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return string(data)
}

var _ = Describe("Config test", func() {
	It("Valid - safeDriverLoad disabled", func() {
		cfg, err := configPgk.Load(createConfig(&configPgk.Config{SafeDriverLoad: configPgk.SafeDriverLoadConfig{
			Enable: false,
		}}))
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.SafeDriverLoad.Enable).To(BeFalse())
	})
	It("Valid - safeDriverLoad enabled", func() {
		cfg, err := configPgk.Load(createConfig(&configPgk.Config{SafeDriverLoad: configPgk.SafeDriverLoadConfig{
			Enable:     true,
			Annotation: "something",
		}}))
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.SafeDriverLoad.Enable).To(BeTrue())
		Expect(cfg.SafeDriverLoad.Annotation).To(Equal("something"))
	})
	It("Failed to unmarshal config", func() {
		_, err := configPgk.Load("invalid\"")
		Expect(err).To(HaveOccurred())
	})
	It("Logical validation failed - no annotation", func() {
		_, err := configPgk.Load(createConfig(&configPgk.Config{SafeDriverLoad: configPgk.SafeDriverLoadConfig{
			Enable: true,
		}}))
		Expect(err).To(HaveOccurred())
	})
	It("Valid - moduleDependencyCheck disabled", func() {
		cfg, err := configPgk.Load(createConfig(&configPgk.Config{
			ModuleDependencyCheck: configPgk.ModuleDependencyCheckConfig{
				Enable: false,
			},
		}))
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.ModuleDependencyCheck.Enable).To(BeFalse())
	})
	It("Valid - moduleDependencyCheck enabled", func() {
		cfg, err := configPgk.Load(createConfig(&configPgk.Config{
			ModuleDependencyCheck: configPgk.ModuleDependencyCheckConfig{
				Enable:       true,
				Modules:      []string{"mlx5_core", "mlx5_ib"},
				HostProcPath: "/host/proc",
				HostSysPath:  "/host/sys",
			},
		}))
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.ModuleDependencyCheck.Enable).To(BeTrue())
		Expect(cfg.ModuleDependencyCheck.Modules).To(Equal([]string{"mlx5_core", "mlx5_ib"}))
		Expect(cfg.ModuleDependencyCheck.HostProcPath).To(Equal("/host/proc"))
		Expect(cfg.ModuleDependencyCheck.HostSysPath).To(Equal("/host/sys"))
	})
	It("Valid - moduleDependencyCheck enabled with UnloadThirdPartyRDMA", func() {
		cfg, err := configPgk.Load(createConfig(&configPgk.Config{
			ModuleDependencyCheck: configPgk.ModuleDependencyCheckConfig{
				Enable:               true,
				Modules:              []string{"mlx5_core", "ib_core"},
				UnloadThirdPartyRDMA: true,
				HostProcPath:         "/host/proc",
				HostSysPath:          "/host/sys",
			},
		}))
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.ModuleDependencyCheck.Enable).To(BeTrue())
		Expect(cfg.ModuleDependencyCheck.Modules).To(Equal([]string{"mlx5_core", "ib_core"}))
		Expect(cfg.ModuleDependencyCheck.UnloadThirdPartyRDMA).To(BeTrue())
	})
	It("Logical validation failed - moduleDependencyCheck enabled with no modules", func() {
		_, err := configPgk.Load(createConfig(&configPgk.Config{
			ModuleDependencyCheck: configPgk.ModuleDependencyCheckConfig{
				Enable: true,
			},
		}))
		Expect(err).To(HaveOccurred())
	})
	It("Backward compatible - old config without moduleDependencyCheck field", func() {
		// Simulate an old ConfigMap that only has safeDriverLoad
		oldJSON := `{"safeDriverLoad":{"enable":false,"annotation":""}}`
		cfg, err := configPgk.Load(oldJSON)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.ModuleDependencyCheck.Enable).To(BeFalse())
		Expect(cfg.ModuleDependencyCheck.Modules).To(BeNil())
	})
})
