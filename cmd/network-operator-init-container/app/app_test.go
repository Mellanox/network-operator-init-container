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

package app_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/json"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Mellanox/network-operator-init-container/cmd/network-operator-init-container/app"
	"github.com/Mellanox/network-operator-init-container/cmd/network-operator-init-container/app/options"
	configPgk "github.com/Mellanox/network-operator-init-container/pkg/config"
)

const (
	testConfigMapName      = "test"
	testConfigMapNamespace = "default"
	testConfigMapKey       = "conf"
	testNodeName           = "node1"
	testAnnotation         = "foo.bar/spam"
)

func createNode(name string) *corev1.Node {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
	Expect(k8sClient.Create(ctx, node)).NotTo(HaveOccurred())
	return node
}

func createConfig(cfg *configPgk.Config) {
	data, err := json.Marshal(cfg)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	err = k8sClient.Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: testConfigMapName, Namespace: testConfigMapNamespace},
		Data:       map[string]string{testConfigMapKey: string(data)},
	})
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
}

func newOpts() *options.Options {
	return &options.Options{
		ConfigMapName:      testConfigMapName,
		ConfigMapNamespace: testConfigMapNamespace,
		ConfigMapKey:       testConfigMapKey,
	}
}

var _ = Describe("Init container", func() {
	var (
		testCtx   context.Context
		testCFunc context.CancelFunc
	)

	BeforeEach(func() {
		testCtx, testCFunc = context.WithCancel(ctx)
	})

	AfterEach(func() {
		err := k8sClient.Delete(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: testConfigMapName, Namespace: testConfigMapNamespace},
		})
		if !apiErrors.IsNotFound(err) {
			Expect(err).NotTo(HaveOccurred())
		}
		testCFunc()
	})
	It("Succeed", func() {
		testDone := make(chan interface{})
		go func() {
			defer close(testDone)
			defer GinkgoRecover()
			opts := newOpts()
			opts.NodeName = testNodeName
			createConfig(&configPgk.Config{SafeDriverLoad: configPgk.SafeDriverLoadConfig{
				Enable:     true,
				Annotation: testAnnotation,
			}})
			var err error
			appExit := make(chan interface{})
			go func() {
				err = app.RunNetworkOperatorInitContainer(testCtx, cfg, opts)
				close(appExit)
			}()
			node := &corev1.Node{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(testCtx, types.NamespacedName{Name: testNodeName}, node)).NotTo(HaveOccurred())
				g.Expect(node.GetAnnotations()[testAnnotation]).NotTo(BeEmpty())
			}, 30, 1).Should(Succeed())
			// remove annotation
			Expect(k8sClient.Patch(testCtx, node, client.RawPatch(
				types.MergePatchType, []byte(
					fmt.Sprintf(`{"metadata":{"annotations":{%q: null}}}`,
						testAnnotation))))).NotTo(HaveOccurred())
			Eventually(appExit, 30, 1).Should(BeClosed())
			Expect(err).NotTo(HaveOccurred())
		}()
		Eventually(testDone, 1*time.Minute).Should(BeClosed())
	})
	It("Unknown node", func() {
		testDone := make(chan interface{})
		go func() {
			defer close(testDone)
			defer GinkgoRecover()
			opts := newOpts()
			opts.NodeName = "unknown-node"
			createConfig(&configPgk.Config{SafeDriverLoad: configPgk.SafeDriverLoadConfig{
				Enable:     true,
				Annotation: testAnnotation,
			}})
			var err error
			appExit := make(chan interface{})
			go func() {
				err = app.RunNetworkOperatorInitContainer(testCtx, cfg, opts)
				close(appExit)
			}()
			Eventually(appExit, 30, 1).Should(BeClosed())
			Expect(err).To(HaveOccurred())
		}()
		Eventually(testDone, 1*time.Minute).Should(BeClosed())
	})
	It("Canceled", func() {
		testDone := make(chan interface{})
		go func() {
			defer close(testDone)
			defer GinkgoRecover()
			opts := newOpts()
			opts.NodeName = testNodeName
			createConfig(&configPgk.Config{SafeDriverLoad: configPgk.SafeDriverLoadConfig{
				Enable:     true,
				Annotation: testAnnotation,
			}})
			var err error
			appExit := make(chan interface{})
			go func() {
				err = app.RunNetworkOperatorInitContainer(testCtx, cfg, opts)
				close(appExit)
			}()
			testCFunc()
			Eventually(appExit, 30, 1).Should(BeClosed())
			Expect(err).To(HaveOccurred())
		}()
		Eventually(testDone, 1*time.Minute).Should(BeClosed())
	})
	It("Failed to read config", func() {
		testDone := make(chan interface{})
		go func() {
			defer close(testDone)
			defer GinkgoRecover()
			opts := newOpts()
			opts.NodeName = "unknown-node"
			var err error
			appExit := make(chan interface{})
			go func() {
				err = app.RunNetworkOperatorInitContainer(testCtx, cfg, opts)
				close(appExit)
			}()
			Eventually(appExit, 30, 1).Should(BeClosed())
			Expect(err).To(HaveOccurred())
		}()
		Eventually(testDone, 1*time.Minute).Should(BeClosed())
	})
	It("Safe loading disabled", func() {
		testDone := make(chan interface{})
		go func() {
			defer close(testDone)
			defer GinkgoRecover()
			opts := newOpts()
			opts.NodeName = testNodeName
			createConfig(&configPgk.Config{SafeDriverLoad: configPgk.SafeDriverLoadConfig{
				Enable: false,
			}})
			var err error
			appExit := make(chan interface{})
			go func() {
				err = app.RunNetworkOperatorInitContainer(testCtx, cfg, opts)
				close(appExit)
			}()
			Eventually(appExit, 30, 1).Should(BeClosed())
			Expect(err).NotTo(HaveOccurred())
		}()
		Eventually(testDone, 1*time.Minute).Should(BeClosed())
	})

	Context("pre-flight check skip vs run behavior", func() {
		// Build a fake host /proc and /sys layout that contains a blocking dependency
		// my_driver -> ib_core. The check will classify my_driver as an unknown kernel
		// module. Whether this aborts init is controlled by SKIP_PREFLIGHT_CHECKS.
		var procDir, sysDir string

		BeforeEach(func() {
			var err error
			procDir, err = os.MkdirTemp("", "app-proc-*")
			Expect(err).NotTo(HaveOccurred())
			sysDir, err = os.MkdirTemp("", "app-sys-*")
			Expect(err).NotTo(HaveOccurred())

			Expect(os.WriteFile(
				filepath.Join(procDir, "modules"),
				[]byte("ib_core 789012 1 my_driver, Live 0xffffffffa0200000\n"+
					"my_driver 55555 0 - Live 0xffffffffa0400000\n"),
				0644,
			)).To(Succeed())

			holders := filepath.Join(sysDir, "module", "ib_core", "holders")
			Expect(os.MkdirAll(holders, 0755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(holders, "my_driver"), []byte{}, 0644)).To(Succeed())
			Expect(os.MkdirAll(filepath.Join(sysDir, "module", "my_driver", "holders"), 0755)).To(Succeed())
		})

		AfterEach(func() {
			Expect(os.RemoveAll(procDir)).To(Succeed())
			Expect(os.RemoveAll(sysDir)).To(Succeed())
			// Ensure we don't leak env vars between specs.
			Expect(os.Unsetenv("SKIP_PREFLIGHT_CHECKS")).To(Succeed())
		})

		It("blocks init when SkipPreflightChecks=false and issues are found", func() {
			Expect(os.Setenv("SKIP_PREFLIGHT_CHECKS", "false")).To(Succeed())

			testDone := make(chan interface{})
			go func() {
				defer close(testDone)
				defer GinkgoRecover()
				opts := newOpts()
				opts.NodeName = testNodeName
				createConfig(&configPgk.Config{
					SafeDriverLoad: configPgk.SafeDriverLoadConfig{Enable: false},
					ModuleDependencyCheck: configPgk.ModuleDependencyCheckConfig{
						Modules:      []string{"ib_core"},
						HostProcPath: procDir,
						HostSysPath:  sysDir,
					},
				})
				var err error
				appExit := make(chan interface{})
				go func() {
					err = app.RunNetworkOperatorInitContainer(testCtx, cfg, opts)
					close(appExit)
				}()
				Eventually(appExit, 30, 1).Should(BeClosed())
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("pre-flight check found"))
			}()
			Eventually(testDone, 1*time.Minute).Should(BeClosed())
		})

		It("does not run check when SkipPreflightChecks=true (default)", func() {
			// Env var is unset in BeforeEach, so the struct-tag default (true) applies.
			testDone := make(chan interface{})
			go func() {
				defer close(testDone)
				defer GinkgoRecover()
				opts := newOpts()
				opts.NodeName = testNodeName
				createConfig(&configPgk.Config{
					// Safe driver load disabled so the call returns cleanly after the check.
					SafeDriverLoad: configPgk.SafeDriverLoadConfig{Enable: false},
					ModuleDependencyCheck: configPgk.ModuleDependencyCheckConfig{
						Modules:      []string{"ib_core"},
						HostProcPath: procDir,
						HostSysPath:  sysDir,
					},
				})
				var err error
				appExit := make(chan interface{})
				go func() {
					err = app.RunNetworkOperatorInitContainer(testCtx, cfg, opts)
					close(appExit)
				}()
				Eventually(appExit, 30, 1).Should(BeClosed())
				Expect(err).NotTo(HaveOccurred())
			}()
			Eventually(testDone, 1*time.Minute).Should(BeClosed())
		})
	})
})
