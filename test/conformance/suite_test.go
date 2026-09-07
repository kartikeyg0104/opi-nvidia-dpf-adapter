/*
Copyright 2026 Kartikey Gupta.

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

// Package conformance is a vendor-agnostic envtest suite that validates the
// Hybrid translation pattern against mock OPI CRs: emitted-child GC/owner-refs
// and status mirroring. It is table-driven so a vendor drops in their mapping
// YAML plus a case describing the mock source and expected children.
package conformance

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/kartikeyg0104/opi-nvidia-dpf-adapter/pkg/mapping"
)

var (
	ctx       context.Context
	cancel    context.CancelFunc
	testEnv   *envtest.Environment
	cfg       *rest.Config
	k8sClient client.Client
	scheme    *runtime.Scheme
	specs     map[string]*mapping.Spec
)

// GVKs the suite reads or writes. Registered as unstructured so the suite needs
// no generated Go types for the OPI or DPF APIs.
var (
	sfcGVK    = schema.GroupVersionKind{Group: "config.openshift.io", Version: "v1", Kind: "ServiceFunctionChain"}
	dpuGVK    = schema.GroupVersionKind{Group: "config.openshift.io", Version: "v1", Kind: "DataProcessingUnit"}
	svcGVK    = schema.GroupVersionKind{Group: "svc.dpu.nvidia.com", Version: "v1alpha1", Kind: "DPUService"}
	devGVK    = schema.GroupVersionKind{Group: "provisioning.dpu.nvidia.com", Version: "v1alpha1", Kind: "DPUDevice"}
	flavorGVK = schema.GroupVersionKind{Group: "provisioning.dpu.nvidia.com", Version: "v1alpha1", Kind: "DPUFlavor"}
	bfbGVK    = schema.GroupVersionKind{Group: "provisioning.dpu.nvidia.com", Version: "v1alpha1", Kind: "BFB"}
	provDPU   = schema.GroupVersionKind{Group: "provisioning.dpu.nvidia.com", Version: "v1alpha1", Kind: "DPU"}
)

func registerUnstructured(s *runtime.Scheme, gvk schema.GroupVersionKind) {
	s.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
	metav1.AddToGroupVersion(s, gvk.GroupVersion())
}

func TestConformance(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "OPI→DPF Conformance Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))
	ctx, cancel = context.WithCancel(context.TODO())

	crdDirs := siblingCRDDirs()
	for _, d := range crdDirs {
		if _, err := os.Stat(d); err != nil {
			Skip("sibling CRD dir not found (need dpu-operator + doca-platform checked out beside this repo): " + d)
		}
	}

	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     crdDirs,
		ErrorIfCRDPathMissing: true,
	}
	if dir := envtestBinDir(); dir != "" {
		testEnv.BinaryAssetsDirectory = dir
	}

	var err error
	cfg, err = testEnv.Start()
	if err != nil {
		Skip("envtest binaries not available (run setup-envtest): " + err.Error())
	}
	Expect(cfg).NotTo(BeNil())

	scheme = runtime.NewScheme()
	Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
	for _, gvk := range []schema.GroupVersionKind{sfcGVK, dpuGVK, svcGVK, devGVK, flavorGVK, bfbGVK, provDPU} {
		registerUnstructured(scheme, gvk)
	}

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	loaded, err := mapping.LoadDir(filepath.Join("..", "..", "config", "mappings"))
	Expect(err).NotTo(HaveOccurred())
	specs = map[string]*mapping.Spec{}
	for _, s := range loaded {
		specs[s.Metadata.Name] = s
	}
})

var _ = AfterSuite(func() {
	if cancel != nil {
		cancel()
	}
	if testEnv == nil || cfg == nil {
		return
	}
	Eventually(func() error { return testEnv.Stop() }, time.Minute, time.Second).Should(Succeed())
})

// siblingCRDDirs returns the OPI/DPF CRD base directories in the sibling repos.
// The workspace root is three levels above this package (test/conformance).
func siblingCRDDirs() []string {
	ws, _ := filepath.Abs(filepath.Join("..", "..", ".."))
	return []string{
		filepath.Join(ws, "dpu-operator", "config", "crd", "bases"),
		filepath.Join(ws, "doca-platform", "config", "dpuservice", "crd", "bases"),
		filepath.Join(ws, "doca-platform", "config", "provisioning", "crd", "bases"),
	}
}

// envtestBinDir mirrors the controller suite: find kube-apiserver/etcd under
// bin/k8s so the suite runs without KUBEBUILDER_ASSETS set explicitly.
func envtestBinDir() string {
	base := filepath.Join("..", "..", "bin", "k8s")
	entries, err := os.ReadDir(base)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			return filepath.Join(base, e.Name())
		}
	}
	return ""
}
