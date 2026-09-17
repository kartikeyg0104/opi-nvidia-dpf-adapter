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
	"slices"
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

// Second vendor (AMD Pensando DSC). Schemas are stubs under crds/amd until the
// real CRDs come off lab host dh1; the GVKs are what config/mappings/amd-dsc200.yaml
// emits. Named here only so cases can refer to them -- the suite registers every
// GVK a loaded mapping mentions, so a third vendor needs no edit here.
var (
	dscDeviceGVK   = schema.GroupVersionKind{Group: "dpu.amd.com", Version: "v1alpha1", Kind: "DSCDevice"}
	dscProfileGVK  = schema.GroupVersionKind{Group: "dpu.amd.com", Version: "v1alpha1", Kind: "DSCProfile"}
	dscFirmwareGVK = schema.GroupVersionKind{Group: "dpu.amd.com", Version: "v1alpha1", Kind: "DSCFirmware"}
	dscPolicyGVK   = schema.GroupVersionKind{Group: "dpu.amd.com", Version: "v1alpha1", Kind: "DSCNodePolicy"}
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

	// Mappings load first: the scheme below is derived from them, so dropping a
	// new vendor's FieldMapping into config/mappings is enough to have this
	// suite serve that vendor's GVKs. No Go edit per vendor.
	specs = loadSpecs()

	crdDirs := siblingCRDDirs()
	for _, d := range crdDirs {
		if _, err := os.Stat(d); err != nil {
			Skip("sibling CRD dir not found (need dpu-operator + doca-platform checked out beside this repo): " + d)
		}
	}
	// Vendor CRDs vendored into this repo (stubs, or real ones once fetched).
	crdDirs = append(crdDirs, localCRDDirs()...)

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
	for _, gvk := range gvksFromSpecs(specs) {
		registerUnstructured(scheme, gvk)
	}

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())
})

// loadSpecs reads the shipped mappings plus the conformance-only fixtures
// (e.g. the cross-namespace cleanup mapping), keyed by metadata.name.
func loadSpecs() map[string]*mapping.Spec {
	GinkgoHelper()
	out := map[string]*mapping.Spec{}
	for _, dir := range []string{filepath.Join("..", "..", "config", "mappings"), "testdata"} {
		loaded, err := mapping.LoadDir(dir)
		Expect(err).NotTo(HaveOccurred(), "load mappings from %s", dir)
		for _, s := range loaded {
			out[s.Metadata.Name] = s
		}
	}
	return out
}

// gvksFromSpecs returns every GVK the loaded mappings read or write, in a
// stable order. Registering from the mappings rather than a hardcoded list is
// what makes a new vendor a data-only change for this suite too.
func gvksFromSpecs(byName map[string]*mapping.Spec) []schema.GroupVersionKind {
	seen := map[schema.GroupVersionKind]bool{}
	var out []schema.GroupVersionKind
	add := func(gvk schema.GroupVersionKind) {
		if !seen[gvk] {
			seen[gvk] = true
			out = append(out, gvk)
		}
	}
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		s := byName[n]
		add(s.Source.GVK())
		for _, e := range s.Emit {
			add(e.Target.GVK())
		}
	}
	return out
}

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

// localCRDDirs returns CRD directories vendored into this repo. Vendors whose
// CRDs are not published as a Go module (or not yet fetched off the lab hosts)
// land here; crds/amd currently holds stubs modelled on the DSC2-100 in dh1.
func localCRDDirs() []string {
	return []string{filepath.Join("crds", "amd")}
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
