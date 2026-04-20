/*
 Copyright 2026, NVIDIA CORPORATION & AFFILIATES
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

package modules_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/klog/v2"

	"github.com/Mellanox/network-operator-init-container/pkg/modules"
)

func TestModules(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Modules Suite")
}

// helper to create a fake /proc/modules file
func writeProcModules(dir, content string) {
	err := os.WriteFile(filepath.Join(dir, "modules"), []byte(content), 0644)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
}

// helper to create /sys/module/<mod>/holders/<holder> structure
func createHolder(sysDir, mod, holder string) {
	holdersDir := filepath.Join(sysDir, "module", mod, "holders")
	err := os.MkdirAll(holdersDir, 0755)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	// Create a file representing the holder symlink
	err = os.WriteFile(filepath.Join(holdersDir, holder), []byte{}, 0644)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
}

// helper to create /sys/module/<mod>/holders/ with no entries
func createEmptyHolders(sysDir, mod string) {
	holdersDir := filepath.Join(sysDir, "module", mod, "holders")
	err := os.MkdirAll(holdersDir, 0755)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
}

// helper to build a map from Dependency slice for easier assertions
func depsToMap(deps []modules.Dependency) map[string][]string {
	m := make(map[string][]string)
	for _, d := range deps {
		m[d.MofedModule] = d.Dependents
	}
	return m
}

var _ = Describe("Checker", func() {
	var (
		procDir string
		sysDir  string
		ctx     context.Context
	)

	BeforeEach(func() {
		var err error
		procDir, err = os.MkdirTemp("", "proc-*")
		Expect(err).NotTo(HaveOccurred())
		sysDir, err = os.MkdirTemp("", "sys-*")
		Expect(err).NotTo(HaveOccurred())
		ctx = context.Background()
	})

	AfterEach(func() {
		Expect(os.RemoveAll(procDir)).To(Succeed())
		Expect(os.RemoveAll(sysDir)).To(Succeed())
	})

	mofedModules := []string{"mlx5_core", "mlx5_ib", "ib_core", "rdma_cm"}
	// Match the defaults from config.ModuleDependencyCheckConfig so existing
	// spec expectations about qedr/nvme_rdma/ib_iser/ib_srp classification hold.
	defaultThirdPartyRDMA := []string{
		"bnxt_re", "efa", "erdma", "iw_cxgb4",
		"hfi1", "hns_roce", "ionic_rdma", "irdma",
		"ib_qib", "mana_ib", "ocrdma", "qedr",
		"rdma_rxe", "siw", "vmw_pvrdma",
	}
	defaultStorage := []string{
		"ib_iser", "ib_isert", "ib_srp", "ib_srpt",
		"nvme_rdma", "nvmet_rdma", "rpcrdma", "xprtrdma",
	}
	logger := klog.NewKlogr()

	It("should return no dependencies when no modules are loaded", func() {
		writeProcModules(procDir, "")
		checker := modules.NewChecker(mofedModules, defaultThirdPartyRDMA, defaultStorage, false, false, procDir, sysDir, logger)
		deps, err := checker.CheckDependencies(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(deps).To(BeEmpty())
	})

	It("should return no dependencies when MOFED modules have no external dependents", func() {
		writeProcModules(procDir, `mlx5_core 1234567 2 mlx5_ib, Live 0xffffffffa0000000
mlx5_ib 456789 0 - Live 0xffffffffa0100000
ib_core 789012 3 mlx5_ib,rdma_cm, Live 0xffffffffa0200000
rdma_cm 111111 0 - Live 0xffffffffa0300000
`)
		createEmptyHolders(sysDir, "mlx5_core")
		createEmptyHolders(sysDir, "mlx5_ib")
		createEmptyHolders(sysDir, "ib_core")
		createEmptyHolders(sysDir, "rdma_cm")

		checker := modules.NewChecker(mofedModules, defaultThirdPartyRDMA, defaultStorage, false, false, procDir, sysDir, logger)
		deps, err := checker.CheckDependencies(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(deps).To(BeEmpty())
	})

	It("should detect immediate non-MOFED dependent", func() {
		// ko2iblnd uses ib_core
		writeProcModules(procDir, `ib_core 789012 1 ko2iblnd, Live 0xffffffffa0200000
ko2iblnd 55555 0 - Live 0xffffffffa0400000
`)
		createHolder(sysDir, "ib_core", "ko2iblnd")
		createEmptyHolders(sysDir, "ko2iblnd")

		checker := modules.NewChecker(mofedModules, defaultThirdPartyRDMA, defaultStorage, false, false, procDir, sysDir, logger)
		deps, err := checker.CheckDependencies(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(deps).To(HaveLen(1))
		Expect(deps[0].MofedModule).To(Equal("ib_core"))
		Expect(deps[0].Dependents).To(ConsistOf("ko2iblnd"))
	})

	It("should detect transitive chain: ext_mod -> ko2iblnd -> ib_core", func() {
		// ib_core is used by ko2iblnd; ko2iblnd is used by ext_mod
		writeProcModules(procDir, `ib_core 789012 1 ko2iblnd, Live 0xffffffffa0200000
ko2iblnd 55555 1 ext_mod, Live 0xffffffffa0400000
ext_mod 99999 0 - Live 0xffffffffa0500000
`)
		createHolder(sysDir, "ib_core", "ko2iblnd")
		createHolder(sysDir, "ko2iblnd", "ext_mod")
		createEmptyHolders(sysDir, "ext_mod")

		checker := modules.NewChecker(mofedModules, defaultThirdPartyRDMA, defaultStorage, false, false, procDir, sysDir, logger)
		deps, err := checker.CheckDependencies(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(deps).To(HaveLen(1))
		Expect(deps[0].MofedModule).To(Equal("ib_core"))
		Expect(deps[0].Dependents).To(ConsistOf("ko2iblnd", "ext_mod"))
	})

	It("should detect diamond dependency: app -> netA,storageA -> ib_core", func() {
		// ib_core is used by netA and storageA; both are used by app
		writeProcModules(procDir, `ib_core 789012 2 netA,storageA, Live 0xffffffffa0200000
netA 33333 1 app, Live 0xffffffffa0600000
storageA 44444 1 app, Live 0xffffffffa0700000
app 22222 0 - Live 0xffffffffa0800000
`)
		createHolder(sysDir, "ib_core", "netA")
		createHolder(sysDir, "ib_core", "storageA")
		createHolder(sysDir, "netA", "app")
		createHolder(sysDir, "storageA", "app")
		createEmptyHolders(sysDir, "app")

		checker := modules.NewChecker(mofedModules, defaultThirdPartyRDMA, defaultStorage, false, false, procDir, sysDir, logger)
		deps, err := checker.CheckDependencies(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(deps).To(HaveLen(1))
		Expect(deps[0].MofedModule).To(Equal("ib_core"))
		Expect(deps[0].Dependents).To(ConsistOf("netA", "storageA", "app"))
	})

	It("should not report MOFED-internal dependencies", func() {
		// mlx5_ib uses ib_core, both MOFED — should not be reported
		writeProcModules(procDir, `ib_core 789012 1 mlx5_ib, Live 0xffffffffa0200000
mlx5_ib 456789 0 - Live 0xffffffffa0100000
`)
		createHolder(sysDir, "ib_core", "mlx5_ib")
		createEmptyHolders(sysDir, "mlx5_ib")

		checker := modules.NewChecker(mofedModules, defaultThirdPartyRDMA, defaultStorage, false, false, procDir, sysDir, logger)
		deps, err := checker.CheckDependencies(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(deps).To(BeEmpty())
	})

	It("should skip modules not loaded (no /sys dir) without error", func() {
		writeProcModules(procDir, `some_other_mod 1234 0 - Live 0xffffffffa0000000
`)
		// No sysfs entries for MOFED modules at all

		checker := modules.NewChecker(mofedModules, defaultThirdPartyRDMA, defaultStorage, false, false, procDir, sysDir, logger)
		deps, err := checker.CheckDependencies(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(deps).To(BeEmpty())
	})

	It("should handle malformed /proc/modules lines gracefully", func() {
		writeProcModules(procDir, `short_line 1234
ib_core 789012 1 ko2iblnd, Live 0xffffffffa0200000
another_bad
ko2iblnd 55555 0 - Live 0xffffffffa0400000
`)
		createHolder(sysDir, "ib_core", "ko2iblnd")
		createEmptyHolders(sysDir, "ko2iblnd")

		checker := modules.NewChecker(mofedModules, defaultThirdPartyRDMA, defaultStorage, false, false, procDir, sysDir, logger)
		deps, err := checker.CheckDependencies(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(deps).To(HaveLen(1))
		Expect(deps[0].MofedModule).To(Equal("ib_core"))
		Expect(deps[0].Dependents).To(ConsistOf("ko2iblnd"))
	})

	It("should handle missing /proc/modules file gracefully", func() {
		// procDir exists but has no modules file — no error, empty result
		checker := modules.NewChecker(mofedModules, defaultThirdPartyRDMA, defaultStorage, false, false, procDir, sysDir, logger)
		deps, err := checker.CheckDependencies(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(deps).To(BeEmpty())
	})

	It("should report non-MOFED module as blocking dep (no allowedModules filtering)", func() {
		// ko2iblnd uses ib_core and is not a known third-party RDMA module
		writeProcModules(procDir, `ib_core 789012 1 ko2iblnd, Live 0xffffffffa0200000
ko2iblnd 55555 0 - Live 0xffffffffa0400000
`)
		createHolder(sysDir, "ib_core", "ko2iblnd")
		createEmptyHolders(sysDir, "ko2iblnd")

		checker := modules.NewChecker(mofedModules, defaultThirdPartyRDMA, defaultStorage, false, false, procDir, sysDir, logger)
		deps, err := checker.CheckDependencies(ctx)
		Expect(err).NotTo(HaveOccurred())
		// ko2iblnd is not in KnownThirdPartyRDMAModules, so CheckDependencies still reports it
		Expect(deps).To(HaveLen(1))
		Expect(deps[0].MofedModule).To(Equal("ib_core"))
		Expect(deps[0].Dependents).To(ConsistOf("ko2iblnd"))
	})

	Context("CheckUserspaceUsers", func() {
		It("should report issue when refcount > holder count", func() {
			// ib_umad: refcount=2, 1 holder → UserspaceCount=1
			writeProcModules(procDir, "ib_umad 28672 2 mlx5_ib 0x00000000\n")
			createHolder(sysDir, "ib_umad", "mlx5_ib")

			checker := modules.NewChecker([]string{"ib_umad"}, defaultThirdPartyRDMA, defaultStorage, false, false, procDir, sysDir, logger)
			issues, err := checker.CheckUserspaceUsers(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(issues).To(HaveLen(1))
			Expect(issues[0].Module).To(Equal("ib_umad"))
			Expect(issues[0].Refcount).To(Equal(2))
			Expect(issues[0].HolderCount).To(Equal(1))
			Expect(issues[0].Holders).To(ConsistOf("mlx5_ib"))
			Expect(issues[0].UserspaceCount).To(Equal(1))
		})

		It("should not report issue when refcount == holder count", func() {
			// ib_umad: refcount=1, 1 holder → no issue
			writeProcModules(procDir, "ib_umad 28672 1 mlx5_ib 0x00000000\n")
			createHolder(sysDir, "ib_umad", "mlx5_ib")

			checker := modules.NewChecker([]string{"ib_umad"}, defaultThirdPartyRDMA, defaultStorage, false, false, procDir, sysDir, logger)
			issues, err := checker.CheckUserspaceUsers(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(issues).To(BeEmpty())
		})

		It("should not report issue when refcount is 0", func() {
			// ib_umad: refcount=0 → no issue
			writeProcModules(procDir, "ib_umad 28672 0 - 0x00000000\n")
			createEmptyHolders(sysDir, "ib_umad")

			checker := modules.NewChecker([]string{"ib_umad"}, defaultThirdPartyRDMA, defaultStorage, false, false, procDir, sysDir, logger)
			issues, err := checker.CheckUserspaceUsers(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(issues).To(BeEmpty())
		})

		It("should skip module not in /proc/modules without error", func() {
			// No entry for ib_umad in /proc/modules
			writeProcModules(procDir, "some_other_mod 1234 0 - 0x00000000\n")

			checker := modules.NewChecker([]string{"ib_umad"}, defaultThirdPartyRDMA, defaultStorage, false, false, procDir, sysDir, logger)
			issues, err := checker.CheckUserspaceUsers(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(issues).To(BeEmpty())
		})

		It("should report all issues when multiple modules have userspace references", func() {
			// ib_umad: refcount=2, 1 holder -> issue; ib_uverbs: refcount=3, 0 holders -> issue
			// ib_core: refcount=1, 1 holder -> no issue
			writeProcModules(procDir,
				"ib_umad 28672 2 mlx5_ib 0x00000000\n"+
					"ib_uverbs 65536 3 - 0x00000000\n"+
					"ib_core 262144 1 mlx5_ib 0x00000000\n")
			createHolder(sysDir, "ib_umad", "mlx5_ib")
			createEmptyHolders(sysDir, "ib_uverbs")
			createHolder(sysDir, "ib_core", "mlx5_ib")

			checker := modules.NewChecker([]string{"ib_umad", "ib_uverbs", "ib_core"}, defaultThirdPartyRDMA, defaultStorage, false, false, procDir, sysDir, logger)
			issues, err := checker.CheckUserspaceUsers(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(issues).To(HaveLen(2))

			issueMap := make(map[string]modules.UserspaceIssue)
			for _, issue := range issues {
				issueMap[issue.Module] = issue
			}
			Expect(issueMap).To(HaveKey("ib_umad"))
			Expect(issueMap["ib_umad"].UserspaceCount).To(Equal(1))
			Expect(issueMap).To(HaveKey("ib_uverbs"))
			Expect(issueMap["ib_uverbs"].UserspaceCount).To(Equal(3))
			Expect(issueMap).NotTo(HaveKey("ib_core"))
		})
	})

	It("should detect deps on multiple MOFED modules simultaneously", func() {
		// ext_a uses mlx5_core; ext_b uses ib_core; ext_c uses ext_b (transitive to ib_core)
		writeProcModules(procDir, `mlx5_core 1234567 1 ext_a, Live 0xffffffffa0000000
ib_core 789012 1 ext_b, Live 0xffffffffa0200000
ext_a 11111 0 - Live 0xffffffffa0900000
ext_b 22222 1 ext_c, Live 0xffffffffa0a00000
ext_c 33333 0 - Live 0xffffffffa0b00000
`)
		createHolder(sysDir, "mlx5_core", "ext_a")
		createHolder(sysDir, "ib_core", "ext_b")
		createHolder(sysDir, "ext_b", "ext_c")
		createEmptyHolders(sysDir, "ext_a")
		createEmptyHolders(sysDir, "ext_c")

		checker := modules.NewChecker(mofedModules, defaultThirdPartyRDMA, defaultStorage, false, false, procDir, sysDir, logger)
		deps, err := checker.CheckDependencies(ctx)
		Expect(err).NotTo(HaveOccurred())

		depMap := depsToMap(deps)
		Expect(depMap).To(HaveKey("mlx5_core"))
		Expect(depMap["mlx5_core"]).To(ConsistOf("ext_a"))
		Expect(depMap).To(HaveKey("ib_core"))
		Expect(depMap["ib_core"]).To(ConsistOf("ext_b", "ext_c"))
	})

	Context("RunAllChecks", func() {
		It("should classify known RDMA module (qedr) as Category 1a", func() {
			writeProcModules(procDir, `ib_core 789012 1 qedr, Live 0xffffffffa0200000
qedr 55555 0 - Live 0xffffffffa0400000
`)
			createHolder(sysDir, "ib_core", "qedr")
			createEmptyHolders(sysDir, "qedr")

			checker := modules.NewChecker(mofedModules, defaultThirdPartyRDMA, defaultStorage, false, false, procDir, sysDir, logger)
			report, err := checker.RunAllChecks(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(report.ThirdPartyRDMA).To(HaveLen(1))
			Expect(report.ThirdPartyRDMA[0].MofedModule).To(Equal("ib_core"))
			Expect(report.ThirdPartyRDMA[0].Dependents).To(ConsistOf("qedr"))
			Expect(report.StorageModules).To(BeEmpty())
			Expect(report.UnknownKernelModules).To(BeEmpty())
			Expect(report.UserspaceIssues).To(BeEmpty())
		})

		It("should classify unknown module (my_driver) as Category 2", func() {
			writeProcModules(procDir, `ib_core 789012 1 my_driver, Live 0xffffffffa0200000
my_driver 55555 0 - Live 0xffffffffa0400000
`)
			createHolder(sysDir, "ib_core", "my_driver")
			createEmptyHolders(sysDir, "my_driver")

			checker := modules.NewChecker(mofedModules, defaultThirdPartyRDMA, defaultStorage, false, false, procDir, sysDir, logger)
			report, err := checker.RunAllChecks(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(report.ThirdPartyRDMA).To(BeEmpty())
			Expect(report.StorageModules).To(BeEmpty())
			Expect(report.UnknownKernelModules).To(HaveLen(1))
			Expect(report.UnknownKernelModules[0].MofedModule).To(Equal("ib_core"))
			Expect(report.UnknownKernelModules[0].Dependents).To(ConsistOf("my_driver"))
			Expect(report.UserspaceIssues).To(BeEmpty())
		})

		It("should classify refcount mismatch as Category 3", func() {
			// ib_umad: refcount=2, 1 holder -> userspace issue
			// Include mlx5_ib in MOFED modules so it is not treated as external dep
			writeProcModules(procDir, "ib_umad 28672 2 mlx5_ib, Live 0x00000000\nmlx5_ib 65536 0 - Live 0x00000001\n")
			createHolder(sysDir, "ib_umad", "mlx5_ib")
			createEmptyHolders(sysDir, "mlx5_ib")

			checker := modules.NewChecker([]string{"ib_umad", "mlx5_ib"}, defaultThirdPartyRDMA, defaultStorage, false, false, procDir, sysDir, logger)
			report, err := checker.RunAllChecks(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(report.ThirdPartyRDMA).To(BeEmpty())
			Expect(report.StorageModules).To(BeEmpty())
			Expect(report.UnknownKernelModules).To(BeEmpty())
			Expect(report.UserspaceIssues).To(HaveLen(1))
			Expect(report.UserspaceIssues[0].Module).To(Equal("ib_umad"))
			Expect(report.UserspaceIssues[0].UserspaceCount).To(Equal(1))
		})

		It("should skip known RDMA module when unloadThirdPartyRDMA=true", func() {
			writeProcModules(procDir, `ib_core 789012 1 qedr, Live 0xffffffffa0200000
qedr 55555 0 - Live 0xffffffffa0400000
`)
			createHolder(sysDir, "ib_core", "qedr")
			createEmptyHolders(sysDir, "qedr")

			checker := modules.NewChecker(mofedModules, defaultThirdPartyRDMA, defaultStorage, true, false, procDir, sysDir, logger)
			report, err := checker.RunAllChecks(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(report.ThirdPartyRDMA).To(BeEmpty())
			Expect(report.StorageModules).To(BeEmpty())
			Expect(report.UnknownKernelModules).To(BeEmpty())
			Expect(report.UserspaceIssues).To(BeEmpty())
		})

		It("should classify ib_iser and ib_srp as Category 1b with default storage list", func() {
			// The original motivating bug: ib_iser (and ib_srp) were being classified as
			// unknown because they weren't in the hardcoded storage list. They must now
			// be recognized via the default STORAGE_MODULES env-driven list.
			writeProcModules(procDir, `ib_core 789012 2 ib_iser,ib_srp, Live 0xffffffffa0200000
ib_iser 55555 0 - Live 0xffffffffa0400000
ib_srp 66666 0 - Live 0xffffffffa0500000
`)
			createHolder(sysDir, "ib_core", "ib_iser")
			createHolder(sysDir, "ib_core", "ib_srp")
			createEmptyHolders(sysDir, "ib_iser")
			createEmptyHolders(sysDir, "ib_srp")

			checker := modules.NewChecker(mofedModules, defaultThirdPartyRDMA, defaultStorage, false, false, procDir, sysDir, logger)
			report, err := checker.RunAllChecks(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(report.ThirdPartyRDMA).To(BeEmpty())
			Expect(report.StorageModules).To(HaveLen(1))
			Expect(report.StorageModules[0].MofedModule).To(Equal("ib_core"))
			Expect(report.StorageModules[0].Dependents).To(ConsistOf("ib_iser", "ib_srp"))
			Expect(report.UnknownKernelModules).To(BeEmpty())
			Expect(report.UserspaceIssues).To(BeEmpty())
		})

		It("should classify storage module (nvme_rdma) as Category 1b when flag is false", func() {
			writeProcModules(procDir, `ib_core 789012 1 nvme_rdma, Live 0xffffffffa0200000
nvme_rdma 55555 0 - Live 0xffffffffa0400000
`)
			createHolder(sysDir, "ib_core", "nvme_rdma")
			createEmptyHolders(sysDir, "nvme_rdma")

			checker := modules.NewChecker(mofedModules, defaultThirdPartyRDMA, defaultStorage, false, false, procDir, sysDir, logger)
			report, err := checker.RunAllChecks(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(report.ThirdPartyRDMA).To(BeEmpty())
			Expect(report.StorageModules).To(HaveLen(1))
			Expect(report.StorageModules[0].MofedModule).To(Equal("ib_core"))
			Expect(report.StorageModules[0].Dependents).To(ConsistOf("nvme_rdma"))
			Expect(report.UnknownKernelModules).To(BeEmpty())
			Expect(report.UserspaceIssues).To(BeEmpty())
		})

		It("should skip storage module when unloadStorageModules=true", func() {
			writeProcModules(procDir, `ib_core 789012 1 nvme_rdma, Live 0xffffffffa0200000
nvme_rdma 55555 0 - Live 0xffffffffa0400000
`)
			createHolder(sysDir, "ib_core", "nvme_rdma")
			createEmptyHolders(sysDir, "nvme_rdma")

			checker := modules.NewChecker(mofedModules, defaultThirdPartyRDMA, defaultStorage, false, true, procDir, sysDir, logger)
			report, err := checker.RunAllChecks(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(report.ThirdPartyRDMA).To(BeEmpty())
			Expect(report.StorageModules).To(BeEmpty())
			Expect(report.UnknownKernelModules).To(BeEmpty())
			Expect(report.UserspaceIssues).To(BeEmpty())
		})

		It("should silently greenlit core RDMA module (iw_cm)", func() {
			writeProcModules(procDir, `ib_core 789012 1 iw_cm, Live 0xffffffffa0200000
iw_cm 55555 0 - Live 0xffffffffa0400000
`)
			createHolder(sysDir, "ib_core", "iw_cm")
			createEmptyHolders(sysDir, "iw_cm")

			checker := modules.NewChecker(mofedModules, defaultThirdPartyRDMA, defaultStorage, false, false, procDir, sysDir, logger)
			report, err := checker.RunAllChecks(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(report.ThirdPartyRDMA).To(BeEmpty())
			Expect(report.StorageModules).To(BeEmpty())
			Expect(report.UnknownKernelModules).To(BeEmpty())
			Expect(report.UserspaceIssues).To(BeEmpty())
		})

		It("should silently greenlit mlx5-prefixed module", func() {
			// mlx5_vdpa depends on mlx5_core (MOFED) but is not in the MOFED module list
			writeProcModules(procDir, `mlx5_core 1234567 1 mlx5_vdpa, Live 0xffffffffa0000000
mlx5_vdpa 55555 0 - Live 0xffffffffa0400000
`)
			createHolder(sysDir, "mlx5_core", "mlx5_vdpa")
			createEmptyHolders(sysDir, "mlx5_vdpa")

			checker := modules.NewChecker([]string{"mlx5_core"}, defaultThirdPartyRDMA, defaultStorage, false, false, procDir, sysDir, logger)
			report, err := checker.RunAllChecks(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(report.ThirdPartyRDMA).To(BeEmpty())
			Expect(report.StorageModules).To(BeEmpty())
			Expect(report.UnknownKernelModules).To(BeEmpty())
			Expect(report.UserspaceIssues).To(BeEmpty())
		})

		It("should greenlit mlx5 prefix while reporting other blocking modules", func() {
			writeProcModules(procDir, `mlx5_core 1234567 2 mlx5_vdpa,my_driver, Live 0xffffffffa0000000
mlx5_vdpa 55555 0 - Live 0xffffffffa0400000
my_driver 44444 0 - Live 0xffffffffa0500000
`)
			createHolder(sysDir, "mlx5_core", "mlx5_vdpa")
			createHolder(sysDir, "mlx5_core", "my_driver")
			createEmptyHolders(sysDir, "mlx5_vdpa")
			createEmptyHolders(sysDir, "my_driver")

			checker := modules.NewChecker([]string{"mlx5_core"}, defaultThirdPartyRDMA, defaultStorage, false, false, procDir, sysDir, logger)
			report, err := checker.RunAllChecks(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(report.ThirdPartyRDMA).To(BeEmpty())
			Expect(report.StorageModules).To(BeEmpty())
			Expect(report.UnknownKernelModules).To(HaveLen(1))
			Expect(report.UnknownKernelModules[0].Dependents).To(ConsistOf("my_driver"))
			Expect(report.UserspaceIssues).To(BeEmpty())
		})

		It("should report mix of all four kernel module categories", func() {
			// qedr (known RDMA) -> Cat 1a; nvme_rdma (storage) -> Cat 1b;
			// my_driver (unknown) -> Cat 2; mlx5_vdpa (greenlit) -> skipped;
			// ib_umad refcount mismatch -> Cat 3
			writeProcModules(procDir, `ib_core 789012 3 qedr,nvme_rdma,my_driver, Live 0xffffffffa0200000
mlx5_core 1234567 1 mlx5_vdpa, Live 0xffffffffa0000000
qedr 55555 0 - Live 0xffffffffa0400000
nvme_rdma 66666 0 - Live 0xffffffffa0600000
my_driver 44444 0 - Live 0xffffffffa0500000
mlx5_vdpa 77777 0 - Live 0xffffffffa0700000
ib_umad 28672 2 mlx5_ib 0x00000000
`)
			createHolder(sysDir, "ib_core", "qedr")
			createHolder(sysDir, "ib_core", "nvme_rdma")
			createHolder(sysDir, "ib_core", "my_driver")
			createHolder(sysDir, "mlx5_core", "mlx5_vdpa")
			createEmptyHolders(sysDir, "qedr")
			createEmptyHolders(sysDir, "nvme_rdma")
			createEmptyHolders(sysDir, "my_driver")
			createEmptyHolders(sysDir, "mlx5_vdpa")
			createHolder(sysDir, "ib_umad", "mlx5_ib")

			checker := modules.NewChecker([]string{"mlx5_core", "mlx5_ib", "ib_core", "rdma_cm", "ib_umad"},
				defaultThirdPartyRDMA, defaultStorage, false, false, procDir, sysDir, logger)
			report, err := checker.RunAllChecks(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(report.ThirdPartyRDMA).To(HaveLen(1))
			Expect(report.ThirdPartyRDMA[0].Dependents).To(ConsistOf("qedr"))
			Expect(report.StorageModules).To(HaveLen(1))
			Expect(report.StorageModules[0].Dependents).To(ConsistOf("nvme_rdma"))
			Expect(report.UnknownKernelModules).To(HaveLen(1))
			Expect(report.UnknownKernelModules[0].Dependents).To(ConsistOf("my_driver"))
			Expect(report.UserspaceIssues).To(HaveLen(1))
			Expect(report.UserspaceIssues[0].Module).To(Equal("ib_umad"))
		})

		It("should return empty report when no issues", func() {
			writeProcModules(procDir, `mlx5_core 1234567 1 mlx5_ib, Live 0xffffffffa0000000
mlx5_ib 456789 0 - Live 0xffffffffa0100000
ib_core 789012 1 mlx5_ib, Live 0xffffffffa0200000
`)
			// Create holders matching refcounts so no userspace mismatch
			createHolder(sysDir, "mlx5_core", "mlx5_ib")
			createEmptyHolders(sysDir, "mlx5_ib")
			createHolder(sysDir, "ib_core", "mlx5_ib")

			checker := modules.NewChecker(mofedModules, defaultThirdPartyRDMA, defaultStorage, false, false, procDir, sysDir, logger)
			report, err := checker.RunAllChecks(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(report.ThirdPartyRDMA).To(BeEmpty())
			Expect(report.StorageModules).To(BeEmpty())
			Expect(report.UnknownKernelModules).To(BeEmpty())
			Expect(report.UserspaceIssues).To(BeEmpty())
		})
	})
})
