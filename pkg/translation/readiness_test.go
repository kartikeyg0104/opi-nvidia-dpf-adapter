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

package translation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kartikeyg0104/opi-nvidia-dpf-adapter/pkg/lifecycle"
	"github.com/kartikeyg0104/opi-nvidia-dpf-adapter/pkg/mapping"
)

var (
	flavorGVK = schema.GroupVersionKind{Group: "provisioning.dpu.nvidia.com", Version: "v1alpha1", Kind: "DPUFlavor"}
	bfbGVK    = schema.GroupVersionKind{Group: "provisioning.dpu.nvidia.com", Version: "v1alpha1", Kind: "BFB"}
	provDPU   = schema.GroupVersionKind{Group: "provisioning.dpu.nvidia.com", Version: "v1alpha1", Kind: "DPU"}
)

func dpuSpec() *mapping.Spec {
	loaded, err := mapping.LoadDir(filepath.Join("..", "..", "config", "mappings"))
	Expect(err).NotTo(HaveOccurred())
	for _, spec := range loaded {
		if spec.Metadata.Name == "dataprocessingunit" {
			return spec
		}
	}
	Fail("dataprocessingunit mapping missing")
	return nil
}

func bluefieldSource() *unstructured.Unstructured {
	src := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"nodeName":       "kind-worker",
			"dpuProductName": "BlueField-3",
			"isDpuSide":      false,
		},
	}}
	src.SetGroupVersionKind(dpuGVK)
	src.SetName("bf3-worker")
	src.SetUID("dpu-uid-1")
	src.SetAnnotations(map[string]string{
		"discovery.opiproject.org/product-name":     "BlueField-3",
		"provisioning.dpu.nvidia.com/serial-number": "MT1234",
		"provisioning.dpu.nvidia.com/pci-address":   "0000-03-00",
		"dpu.nvidia.com/bfb-url":                    "https://example.invalid/bf.bfb",
	})
	return src
}

func lcmConfig(ready bool) *unstructured.Unstructured {
	status, reason := "False", lifecycle.OperatorInstalling
	if ready {
		status, reason = "True", lifecycle.OperatorReady
	}
	cfg := &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{
			"conditions": []any{
				map[string]any{
					"type":   lifecycle.OperatorReadyConditionType,
					"status": status,
					"reason": reason,
				},
			},
		},
	}}
	cfg.SetGroupVersionKind(lifecycle.DpuOperatorConfigGVK)
	cfg.SetName("cluster")
	return cfg
}

func deviceExists(cl client.Client) bool {
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(devGVK)
	err := cl.Get(ctx, types.NamespacedName{Name: "mt1234", Namespace: "dpf-operator-system"}, got)
	return err == nil
}

var _ = Describe("LCM readiness gate", func() {
	It("holds a source created before DPF is ready and applies it once VendorOperatorReady is True", func() {
		scheme := testScheme(dpuGVK, devGVK, flavorGVK, bfbGVK, provDPU, lifecycle.DpuOperatorConfigGVK)
		src := bluefieldSource()
		cfg := lcmConfig(false)
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(src, cfg).WithStatusSubresource(src, cfg).Build()
		r := &Reconciler{Client: cl, Scheme: scheme, Spec: dpuSpec()}
		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "bf3-worker"}}

		result, err := r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(DPFNotReadyRequeue))
		Expect(deviceExists(cl)).To(BeFalse())

		Expect(cl.Get(ctx, types.NamespacedName{Name: "cluster"}, cfg)).To(Succeed())
		Expect(unstructured.SetNestedSlice(cfg.Object, []any{
			map[string]any{
				"type":   lifecycle.OperatorReadyConditionType,
				"status": "True",
				"reason": lifecycle.OperatorReady,
			},
		}, "status", "conditions")).To(Succeed())
		Expect(cl.Status().Update(ctx, cfg)).To(Succeed())

		// The watch, not a poll, is what admits the waiting source.
		Expect(r.enqueueIfLCMReady(ctx, cfg)).To(ConsistOf(req))

		result, err = r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(reconcile.Result{}))
		Expect(deviceExists(cl)).To(BeTrue())
	})

	It("does not block when no DpuOperatorConfig exists", func() {
		scheme := testScheme(dpuGVK, devGVK, flavorGVK, bfbGVK, provDPU, lifecycle.DpuOperatorConfigGVK)
		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(bluefieldSource()).
			WithStatusSubresource(bluefieldSource()).
			Build()
		r := &Reconciler{Client: cl, Scheme: scheme, Spec: dpuSpec()}
		result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "bf3-worker"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(BeZero())
		Expect(deviceExists(cl)).To(BeTrue())
	})

	It("does not requeue a source that emits nothing while LCM is still installing", func() {
		scheme := testScheme(dpuGVK, devGVK, flavorGVK, bfbGVK, provDPU, lifecycle.DpuOperatorConfigGVK)
		src := bluefieldSource()
		Expect(unstructured.SetNestedField(src.Object, "DSC-200", "spec", "dpuProductName")).To(Succeed())
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(src, lcmConfig(false)).WithStatusSubresource(src).Build()
		r := &Reconciler{Client: cl, Scheme: scheme, Spec: dpuSpec()}
		result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "bf3-worker"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(reconcile.Result{}))
		Expect(deviceExists(cl)).To(BeFalse())
	})

	It("retries on a fixed delay while a target API is undiscovered, then applies", func() {
		scheme := testScheme(dpuGVK, devGVK, flavorGVK, bfbGVK, provDPU)
		cl := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(bluefieldSource()).
			WithStatusSubresource(bluefieldSource()).
			Build()
		installed := false
		r := &Reconciler{
			Client: cl,
			Scheme: scheme,
			Spec:   dpuSpec(),
			TargetsInstalled: func(context.Context, []schema.GroupVersionKind) (bool, error) {
				return installed, nil
			},
		}
		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "bf3-worker"}}

		result, err := r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(DPFNotReadyRequeue))
		Expect(deviceExists(cl)).To(BeFalse())

		installed = true
		result, err = r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(reconcile.Result{}))
		Expect(deviceExists(cl)).To(BeTrue())
	})

	It("enqueues the source only for a served target CRD", func() {
		scheme := testScheme(dpuGVK, devGVK)
		src := bluefieldSource()
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(src).Build()
		r := &Reconciler{Client: cl, Scheme: scheme, Spec: dpuSpec()}
		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "bf3-worker"}}

		Expect(r.enqueueIfTargetCRD(ctx, targetCRD(devGVK, true))).To(ConsistOf(req))
		Expect(r.enqueueIfTargetCRD(ctx, targetCRD(devGVK, false))).To(BeEmpty())
		other := schema.GroupVersionKind{Group: "amd.example.com", Version: "v1", Kind: "DSCDevice"}
		Expect(r.enqueueIfTargetCRD(ctx, targetCRD(other, true))).To(BeEmpty())
	})

	It("does not enqueue sources while VendorOperatorReady is still False", func() {
		scheme := testScheme(dpuGVK, lifecycle.DpuOperatorConfigGVK)
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(bluefieldSource()).Build()
		r := &Reconciler{Client: cl, Scheme: scheme, Spec: dpuSpec()}
		Expect(r.enqueueIfLCMReady(ctx, lcmConfig(false))).To(BeEmpty())
	})

	It("skips Owns for kinds discovery does not serve", func() {
		mapper := &stubMapper{missing: map[schema.GroupKind]bool{
			devGVK.GroupKind(): true,
		}}
		got, err := ownableTargets(mapper, []schema.GroupVersionKind{devGVK, flavorGVK})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal([]schema.GroupVersionKind{flavorGVK}))
		got, err = ownableTargets(nil, []schema.GroupVersionKind{devGVK})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal([]schema.GroupVersionKind{devGVK}))
	})

	It("resets a stale discovery mapper once and sees the CRD that appeared", func() {
		mapper := &stubMapper{missing: map[schema.GroupKind]bool{devGVK.GroupKind(): true}}
		installed, err := MapperTargetsInstalled(mapper)(ctx, []schema.GroupVersionKind{devGVK})
		Expect(err).NotTo(HaveOccurred())
		Expect(installed).To(BeFalse())
		Expect(mapper.resets).To(Equal(1))

		mapper.clearOnReset = true
		installed, err = MapperTargetsInstalled(mapper)(ctx, []schema.GroupVersionKind{devGVK})
		Expect(err).NotTo(HaveOccurred())
		Expect(installed).To(BeTrue())
		Expect(mapper.resets).To(Equal(2))

		installed, err = MapperTargetsInstalled(&errMapper{})(ctx, []schema.GroupVersionKind{devGVK})
		Expect(err).To(MatchError("discovery down"))
		Expect(installed).To(BeFalse())
	})

	It("wires the manager to the discovery gate before mapping controllers start", func() {
		b, err := os.ReadFile(filepath.Join("..", "..", "cmd", "main.go"))
		Expect(err).NotTo(HaveOccurred())
		src := string(b)
		cfg := strings.Index(src, "registerUnstructured(scheme, lifecycle.DpuOperatorConfigGVK)")
		crd := strings.Index(src, `Kind: "CustomResourceDefinition"`)
		loop := strings.Index(src, "for _, spec := range specs")
		Expect(cfg).To(BeNumerically(">", 0))
		Expect(crd).To(BeNumerically(">", 0))
		Expect(cfg).To(BeNumerically("<", loop))
		Expect(crd).To(BeNumerically("<", loop))
		Expect(src).To(ContainSubstring("translation.MapperTargetsInstalled(mgr.GetRESTMapper())"))
	})
})

func targetCRD(gvk schema.GroupVersionKind, served bool) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"group": gvk.Group,
			"names": map[string]any{"kind": gvk.Kind},
			"versions": []any{
				map[string]any{"name": gvk.Version, "served": served},
			},
		},
	}}
	u.SetGroupVersionKind(crdGVK())
	u.SetName(strings.ToLower(gvk.Kind))
	return u
}

type errMapper struct{ stubMapper }

func (m *errMapper) RESTMapping(schema.GroupKind, ...string) (*apimeta.RESTMapping, error) {
	return nil, fmt.Errorf("discovery down")
}

type stubMapper struct {
	missing      map[schema.GroupKind]bool
	resets       int
	clearOnReset bool
}

func (m *stubMapper) Reset() {
	m.resets++
	if m.clearOnReset {
		m.missing = nil
	}
}

func (m *stubMapper) RESTMapping(gk schema.GroupKind, versions ...string) (*apimeta.RESTMapping, error) {
	if m.missing[gk] {
		return nil, &apimeta.NoKindMatchError{GroupKind: gk, SearchedVersions: versions}
	}
	version := ""
	if len(versions) > 0 {
		version = versions[0]
	}
	return &apimeta.RESTMapping{GroupVersionKind: gk.WithVersion(version)}, nil
}

func (m *stubMapper) KindFor(schema.GroupVersionResource) (schema.GroupVersionKind, error) {
	return schema.GroupVersionKind{}, nil
}
func (m *stubMapper) KindsFor(schema.GroupVersionResource) ([]schema.GroupVersionKind, error) {
	return nil, nil
}
func (m *stubMapper) ResourceFor(schema.GroupVersionResource) (schema.GroupVersionResource, error) {
	return schema.GroupVersionResource{}, nil
}
func (m *stubMapper) ResourcesFor(schema.GroupVersionResource) ([]schema.GroupVersionResource, error) {
	return nil, nil
}
func (m *stubMapper) RESTMappings(schema.GroupKind, ...string) ([]*apimeta.RESTMapping, error) {
	return nil, nil
}
func (m *stubMapper) ResourceSingularizer(string) (string, error) { return "", nil }
