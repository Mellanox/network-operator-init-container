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

// Package modules provides kernel module dependency checking for MOFED driver pre-flight validation.
package modules

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
)

const (
	procModulesMinFields      = 4
	procModulesRefcountFields = 3
	logVerbosityDebug         = 2
)

// KnownThirdPartyRDMAModules is the set of non-NVIDIA kernel modules from the RDMA ecosystem
// (third-party NIC vendor RDMA providers). When UnloadThirdPartyRDMA is true these are
// treated as allowed and the driver container will handle their unloading.
//
// NOTE: Do NOT add core RDMA infrastructure modules (iw_cm, ib_cm, rdma_cm, etc.)
// here — MOFED manages those in its own unload sequence. Do NOT add storage-over-RDMA
// modules (ib_srp, ib_iser, ib_isert, nvme_rdma, etc.) — those are handled by
// UNLOAD_STORAGE_MODULES in the driver container.
var KnownThirdPartyRDMAModules = map[string]struct{}{
	"bnxt_re": {}, "efa": {}, "erdma": {}, "iw_cxgb4": {},
	"hfi1": {}, "hns_roce": {}, "ionic_rdma": {}, "irdma": {},
	"ib_qib": {}, "mana_ib": {}, "ocrdma": {}, "qedr": {},
	"rdma_rxe": {}, "siw": {}, "vmw_pvrdma": {},
}

// KnownStorageModules is the set of storage-over-RDMA kernel modules that can block
// MOFED driver reload. When UnloadStorageModules is true these are treated as allowed
// and the driver container will handle their unloading via UNLOAD_STORAGE_MODULES.
//
// This list must be kept in sync with doca-driver-build's StorageModules
// (entrypoint/internal/config/config.go, STORAGE_MODULES env var).
var KnownStorageModules = map[string]struct{}{
	"ib_isert": {}, "nvme_rdma": {}, "nvmet_rdma": {},
	"rpcrdma": {}, "xprtrdma": {}, "ib_srpt": {},
}

// DependencyReport contains the classified blocking dependencies.
type DependencyReport struct {
	// Category 1a: Known third-party RDMA modules — can be auto-unloaded
	ThirdPartyRDMA []Dependency
	// Category 1b: Known storage-over-RDMA modules — can be auto-unloaded
	StorageModules []Dependency
	// Category 2: Unknown kernel modules — user must handle manually
	UnknownKernelModules []Dependency
	// Category 3: Userspace references — user must identify and stop processes
	UserspaceIssues []UserspaceIssue
}

// Dependency represents a MOFED module and the non-MOFED modules that depend on it.
type Dependency struct {
	// MofedModule is the name of the MOFED kernel module that has external dependents.
	MofedModule string
	// Dependents is the list of non-MOFED modules that depend on MofedModule.
	Dependents []string
}

// Checker analyzes kernel module dependencies to detect third-party modules
// that depend on NVIDIA MOFED modules. It reads /proc/modules and
// /sys/module/*/holders/ to identify blocking dependencies, performing
// full transitive tree traversal to find indirect dependents.
type Checker struct {
	modules              map[string]struct{}
	unloadThirdPartyRDMA bool
	unloadStorageModules bool
	hostProcPath         string
	hostSysPath          string
	logger               logr.Logger
}

// NewChecker creates a new module dependency checker.
// modules is the list of MOFED kernel modules to check for external dependencies.
// unloadThirdPartyRDMA controls whether known third-party RDMA modules are treated
// as allowed (the driver container will handle their unloading).
// unloadStorageModules controls whether known storage-over-RDMA modules are treated
// as allowed (the driver container will handle their unloading).
// hostProcPath and hostSysPath are paths to the host's /proc and /sys mounts.
func NewChecker(
	modules []string, unloadThirdPartyRDMA bool, unloadStorageModules bool,
	hostProcPath, hostSysPath string, logger logr.Logger,
) *Checker {
	moduleSet := make(map[string]struct{}, len(modules))
	for _, m := range modules {
		moduleSet[m] = struct{}{}
	}
	return &Checker{
		modules:              moduleSet,
		unloadThirdPartyRDMA: unloadThirdPartyRDMA,
		unloadStorageModules: unloadStorageModules,
		hostProcPath:         hostProcPath,
		hostSysPath:          hostSysPath,
		logger:               logger,
	}
}

// parseProcModulesUsers opens and scans a /proc/modules file, calling addEdge
// for every module-user relationship found.
func (c *Checker) parseProcModulesUsers(procModulesPath string, addEdge func(mod, user string)) {
	file, err := os.Open(procModulesPath) //nolint:gosec // path is constructed from trusted hostProcPath config
	if err != nil {
		if os.IsNotExist(err) {
			c.logger.V(1).Info("proc modules file not found, skipping", "path", procModulesPath)
		} else {
			c.logger.V(1).Info("failed to open proc modules, skipping", "path", procModulesPath, "error", err)
		}
		return
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < procModulesMinFields {
			c.logger.V(logVerbosityDebug).Info("skipping malformed proc/modules line", "line", line)
			continue
		}

		modName := fields[0]
		depsField := fields[3]
		if depsField == "-" || depsField == "" {
			continue
		}

		// deps field contains modules that USE this module, with trailing comma
		depNames := strings.Split(strings.TrimRight(depsField, ","), ",")
		for _, user := range depNames {
			user = strings.TrimSpace(user)
			if user == "" {
				continue
			}
			addEdge(modName, user)
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		c.logger.V(1).Info("error scanning proc modules", "error", scanErr)
	}
}

// buildUsersMap parses /proc/modules and /sys/module/*/holders/ for ALL modules
// to build a map of module -> list of modules that use it.
// In /proc/modules, the 4th field lists modules that use the module on that line.
// In /sys/module/<mod>/holders/, each entry is a module that uses <mod>.
func (c *Checker) buildUsersMap(ctx context.Context) map[string][]string {
	_ = ctx
	usersMap := make(map[string][]string)
	// Track unique edges to avoid duplicates when merging proc and sys sources.
	edgeSet := make(map[string]map[string]struct{})

	addEdge := func(mod, user string) {
		if edgeSet[mod] == nil {
			edgeSet[mod] = make(map[string]struct{})
		}
		if _, exists := edgeSet[mod][user]; exists {
			return
		}
		edgeSet[mod][user] = struct{}{}
		usersMap[mod] = append(usersMap[mod], user)
	}

	// Parse /proc/modules
	procModulesPath := filepath.Join(c.hostProcPath, "modules")
	c.parseProcModulesUsers(procModulesPath, addEdge)

	// Supplement with /sys/module/*/holders/ for ALL modules
	sysModuleDir := filepath.Join(c.hostSysPath, "module")
	modDirs, err := os.ReadDir(sysModuleDir)
	if err != nil {
		if !os.IsNotExist(err) {
			c.logger.V(1).Info("failed to read sys module dir, skipping", "path", sysModuleDir, "error", err)
		}
		return usersMap
	}

	for _, modDir := range modDirs {
		if !modDir.IsDir() {
			continue
		}
		modName := modDir.Name()
		holdersPath := filepath.Join(sysModuleDir, modName, "holders")
		entries, err := os.ReadDir(holdersPath)
		if err != nil {
			// Module dir exists but no holders subdir — skip
			continue
		}
		for _, entry := range entries {
			addEdge(modName, entry.Name())
		}
	}

	return usersMap
}

// UserspaceIssue represents a module with userspace references that would block unloading.
type UserspaceIssue struct {
	Module         string
	Refcount       int
	HolderCount    int
	Holders        []string // kernel module holders
	UserspaceCount int      // refcount - holderCount
}

// CheckUserspaceUsers detects modules where refcount > holder count,
// indicating userspace processes are holding the module open.
func (c *Checker) CheckUserspaceUsers(ctx context.Context) ([]UserspaceIssue, error) {
	_ = ctx

	// Parse /proc/modules to get refcount for each configured module.
	refcounts := make(map[string]int)
	procModulesPath := filepath.Join(c.hostProcPath, "modules")
	file, err := os.Open(procModulesPath) //nolint:gosec // path is constructed from trusted hostProcPath config
	if err != nil {
		if !os.IsNotExist(err) {
			c.logger.V(1).Info("failed to open proc modules, skipping", "path", procModulesPath, "error", err)
		}
		// Not loaded — skip silently.
		return nil, nil
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < procModulesRefcountFields {
			c.logger.V(logVerbosityDebug).Info("skipping malformed proc/modules line", "line", line)
			continue
		}
		modName := fields[0]
		if _, configured := c.modules[modName]; !configured {
			continue
		}
		rc, err := strconv.Atoi(fields[2])
		if err != nil {
			c.logger.V(logVerbosityDebug).Info("skipping unparseable refcount", "module", modName, "field", fields[2])
			continue
		}
		refcounts[modName] = rc
	}
	if scanErr := scanner.Err(); scanErr != nil {
		c.logger.V(1).Info("error scanning proc modules", "error", scanErr)
	}

	var issues []UserspaceIssue

	for mod := range c.modules {
		rc, loaded := refcounts[mod]
		if !loaded {
			// Module not in /proc/modules — skip silently.
			continue
		}

		// Count entries in /sys/module/<mod>/holders/
		holdersPath := filepath.Join(c.hostSysPath, "module", mod, "holders")
		entries, err := os.ReadDir(holdersPath)
		holderCount := 0
		var holders []string
		if err == nil {
			for _, e := range entries {
				holders = append(holders, e.Name())
			}
			holderCount = len(holders)
		}

		if rc > holderCount {
			issues = append(issues, UserspaceIssue{
				Module:         mod,
				Refcount:       rc,
				HolderCount:    holderCount,
				Holders:        holders,
				UserspaceCount: rc - holderCount,
			})
		}
	}

	return issues, nil
}

// CheckDependencies analyzes loaded kernel modules and returns any non-MOFED modules
// that transitively depend on the configured MOFED modules. It performs BFS from each
// MOFED module upward through non-MOFED users to find all reachable dependents.
// Returns nil if no blocking dependencies are found.
func (c *Checker) CheckDependencies(ctx context.Context) ([]Dependency, error) {
	usersMap := c.buildUsersMap(ctx)

	var deps []Dependency

	for mofedMod := range c.modules {
		collected := make(map[string]struct{})

		// Seed BFS with immediate users of this MOFED module that are NOT MOFED.
		queue := []string{}
		for _, user := range usersMap[mofedMod] {
			if _, isMofed := c.modules[user]; isMofed {
				continue
			}
			if _, seen := collected[user]; !seen {
				collected[user] = struct{}{}
				queue = append(queue, user)
			}
		}

		// BFS: follow each non-MOFED user's users recursively.
		for len(queue) > 0 {
			current := queue[0]
			queue = queue[1:]

			for _, user := range usersMap[current] {
				if _, isMofed := c.modules[user]; isMofed {
					continue
				}
				if _, seen := collected[user]; seen {
					continue
				}
				collected[user] = struct{}{}
				queue = append(queue, user)
			}
		}

		if len(collected) == 0 {
			continue
		}

		dep := Dependency{MofedModule: mofedMod}
		for m := range collected {
			dep.Dependents = append(dep.Dependents, m)
		}
		deps = append(deps, dep)
	}

	return deps, nil
}

// RunAllChecks performs BFS dependency detection and userspace detection, then
// classifies each blocking issue into one of four categories:
//   - Category 1a: Known third-party RDMA modules (auto-unloadable when flag is set)
//   - Category 1b: Known storage-over-RDMA modules (auto-unloadable when flag is set)
//   - Category 2: Unknown kernel modules (manual intervention required)
//   - Category 3: Userspace processes holding modules open
//
// Modules with an "mlx5" prefix are NVIDIA's own modules and are always silently skipped.
func (c *Checker) RunAllChecks(ctx context.Context) (*DependencyReport, error) {
	report := &DependencyReport{}

	// Step 1: BFS dependency detection
	allDeps, err := c.CheckDependencies(ctx)
	if err != nil {
		return nil, err
	}

	// Step 2: Classify kernel module dependencies
	for _, dep := range allDeps {
		var thirdParty []string
		var storage []string
		var unknown []string
		for _, d := range dep.Dependents {
			// mlx5-prefixed modules are NVIDIA's own — always greenlit
			if strings.HasPrefix(d, "mlx5") {
				continue
			}
			if _, isKnown := KnownThirdPartyRDMAModules[d]; isKnown {
				if !c.unloadThirdPartyRDMA {
					thirdParty = append(thirdParty, d)
				}
				continue
			}
			if _, isStorage := KnownStorageModules[d]; isStorage {
				if !c.unloadStorageModules {
					storage = append(storage, d)
				}
				continue
			}
			unknown = append(unknown, d)
		}
		if len(thirdParty) > 0 {
			report.ThirdPartyRDMA = append(report.ThirdPartyRDMA, Dependency{
				MofedModule: dep.MofedModule,
				Dependents:  thirdParty,
			})
		}
		if len(storage) > 0 {
			report.StorageModules = append(report.StorageModules, Dependency{
				MofedModule: dep.MofedModule,
				Dependents:  storage,
			})
		}
		if len(unknown) > 0 {
			report.UnknownKernelModules = append(report.UnknownKernelModules, Dependency{
				MofedModule: dep.MofedModule,
				Dependents:  unknown,
			})
		}
	}

	// Step 3: Userspace detection (Category 3)
	userspaceIssues, err := c.CheckUserspaceUsers(ctx)
	if err != nil {
		return nil, err
	}
	report.UserspaceIssues = userspaceIssues

	return report, nil
}
