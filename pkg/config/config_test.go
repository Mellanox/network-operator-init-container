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
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	configPgk "github.com/Mellanox/network-operator-init-container/pkg/config"
)

// envVars we touch in these tests. We unset them in BeforeEach so that tests which
// rely on defaults are not polluted by the environment of a developer's shell.
var envVarsUnderTest = []string{
	"STORAGE_MODULES",
	"THIRD_PARTY_RDMA_MODULES",
	"UNLOAD_STORAGE_MODULES",
	"UNLOAD_THIRD_PARTY_RDMA_MODULES",
	"SKIP_PREFLIGHT_CHECKS",
}

func createConfig(cfg *configPgk.Config) string {
	// Marshal only the JSON-driven fields. The env-tagged fields are populated from
	// env vars, so their in-memory contents don't need to be serialized.
	data, err := json.Marshal(cfg)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return string(data)
}

var _ = Describe("Config test", func() {
	BeforeEach(func() {
		// Unset env vars so tests that rely on struct-tag defaults are not polluted
		// by the developer's shell. Tests that need a specific value call Setenv.
		for _, k := range envVarsUnderTest {
			Expect(os.Unsetenv(k)).To(Succeed())
		}
	})

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
	It("Valid - moduleDependencyCheck empty config is accepted", func() {
		cfg, err := configPgk.Load(createConfig(&configPgk.Config{
			ModuleDependencyCheck: configPgk.ModuleDependencyCheckConfig{},
		}))
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.ModuleDependencyCheck.Modules).To(BeEmpty())
	})
	It("Valid - moduleDependencyCheck with modules", func() {
		cfg, err := configPgk.Load(createConfig(&configPgk.Config{
			ModuleDependencyCheck: configPgk.ModuleDependencyCheckConfig{
				Modules:      []string{"mlx5_core", "mlx5_ib"},
				HostProcPath: "/host/proc",
				HostSysPath:  "/host/sys",
			},
		}))
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.ModuleDependencyCheck.Modules).To(Equal([]string{"mlx5_core", "mlx5_ib"}))
		Expect(cfg.ModuleDependencyCheck.HostProcPath).To(Equal("/host/proc"))
		Expect(cfg.ModuleDependencyCheck.HostSysPath).To(Equal("/host/sys"))
	})
	It("Backward compatible - old config without moduleDependencyCheck field", func() {
		// Simulate an old ConfigMap that only has safeDriverLoad
		oldJSON := `{"safeDriverLoad":{"enable":false,"annotation":""}}`
		cfg, err := configPgk.Load(oldJSON)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.ModuleDependencyCheck.Modules).To(BeNil())
	})

	Context("env-tagged fields", func() {
		It("populates defaults when env vars are unset", func() {
			cfg, err := configPgk.Load(`{"safeDriverLoad":{"enable":false}}`)
			Expect(err).NotTo(HaveOccurred())

			// Defaults from struct tags.
			Expect(cfg.ModuleDependencyCheck.StorageModules).To(Equal([]string{
				"ib_iser", "ib_isert", "ib_srp", "ib_srpt",
				"nvme_rdma", "nvmet_rdma", "rpcrdma", "xprtrdma",
			}))
			Expect(cfg.ModuleDependencyCheck.ThirdPartyRDMAModules).To(Equal([]string{
				"bnxt_re", "efa", "erdma", "iw_cxgb4",
				"hfi1", "hns_roce", "ionic_rdma", "irdma",
				"ib_qib", "mana_ib", "ocrdma", "qedr",
				"rdma_rxe", "siw", "vmw_pvrdma",
			}))
			Expect(cfg.ModuleDependencyCheck.UnloadStorageModules).To(BeFalse())
			Expect(cfg.ModuleDependencyCheck.UnloadThirdPartyRDMAModules).To(BeFalse())
			Expect(cfg.ModuleDependencyCheck.SkipPreflightChecks).To(BeTrue())
		})

		It("overrides defaults when env vars are set", func() {
			GinkgoT().Setenv("STORAGE_MODULES", "custom_storage_a custom_storage_b")
			GinkgoT().Setenv("THIRD_PARTY_RDMA_MODULES", "custom_rdma_a custom_rdma_b custom_rdma_c")
			GinkgoT().Setenv("UNLOAD_STORAGE_MODULES", "true")
			GinkgoT().Setenv("UNLOAD_THIRD_PARTY_RDMA_MODULES", "true")
			GinkgoT().Setenv("SKIP_PREFLIGHT_CHECKS", "false")

			cfg, err := configPgk.Load(`{"safeDriverLoad":{"enable":false}}`)
			Expect(err).NotTo(HaveOccurred())

			Expect(cfg.ModuleDependencyCheck.StorageModules).To(Equal(
				[]string{"custom_storage_a", "custom_storage_b"}))
			Expect(cfg.ModuleDependencyCheck.ThirdPartyRDMAModules).To(Equal(
				[]string{"custom_rdma_a", "custom_rdma_b", "custom_rdma_c"}))
			Expect(cfg.ModuleDependencyCheck.UnloadStorageModules).To(BeTrue())
			Expect(cfg.ModuleDependencyCheck.UnloadThirdPartyRDMAModules).To(BeTrue())
			Expect(cfg.ModuleDependencyCheck.SkipPreflightChecks).To(BeFalse())
		})

		It("parses space-separated lists correctly", func() {
			// A single value with no separator must be accepted.
			GinkgoT().Setenv("STORAGE_MODULES", "only_one")
			cfg, err := configPgk.Load(`{"safeDriverLoad":{"enable":false}}`)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.ModuleDependencyCheck.StorageModules).To(Equal([]string{"only_one"}))
		})
	})
})
