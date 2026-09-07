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

package conformance

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/yaml"

	"github.com/kartikeyg0104/opi-nvidia-dpf-adapter/pkg/translation"
)

// childSpec is an emitted DPF object a case expects. setReady mocks the DPF
// operator marking that child Ready (requires a status subresource on its CRD).
type childSpec struct {
	gvk      schema.GroupVersionKind
	name, ns string
	setReady bool
}

// conformanceCase is one vendor-agnostic row: a mock OPI source, the mapping
// that translates it, and the children plus status outcome it must produce.
type conformanceCase struct {
	name        string
	mappingName string
	sourceYAML  string
	srcGVK      schema.GroupVersionKind
	srcNN       types.NamespacedName
	ensureNS    []string
	children    []childSpec
	// statusPruned marks a source whose CRD status schema drops mirrored fields
	// (the current ServiceFunctionChain). Such a case asserts GC/owner-refs only.
	statusPruned bool
	// expectReady asserts the source carries a mirrored Ready=True condition
	// after status-capable children are marked ready.
	expectReady bool
}

const dpfNS = "dpf-operator-system"

var cases = []conformanceCase{
	{
		name:        "ServiceFunctionChain → DPUService",
		mappingName: "servicefunctionchain",
		srcGVK:      sfcGVK,
		srcNN:       types.NamespacedName{Name: "conf-chain", Namespace: "default"},
		sourceYAML: `
apiVersion: config.openshift.io/v1
kind: ServiceFunctionChain
metadata:
  name: conf-chain
  namespace: default
spec:
  networkFunctions:
  - name: hbn
    chart:
      repository: https://helm.ngc.nvidia.com/nvidia/doca
      name: hbn
      version: v25.10.1
`,
		children: []childSpec{
			{gvk: svcGVK, name: "hbn", ns: "default", setReady: true},
		},
		statusPruned: true, // SFC CRD status is an empty object; mirror is pruned.
	},
	{
		name:        "DataProcessingUnit → DPU/DPUDevice/DPUFlavor/BFB",
		mappingName: "dataprocessingunit",
		srcGVK:      dpuGVK,
		srcNN:       types.NamespacedName{Name: "conf-bf3"},
		ensureNS:    []string{dpfNS},
		sourceYAML: `
apiVersion: config.openshift.io/v1
kind: DataProcessingUnit
metadata:
  name: conf-bf3
  annotations:
    provisioning.dpu.nvidia.com/serial-number: "MT1234CONF"
    dpu.nvidia.com/bfb-url: "https://example.invalid/fw.bfb"
spec:
  dpuProductName: BlueField-3
  isDpuSide: false
  nodeName: kind-worker
`,
		children: []childSpec{
			{gvk: devGVK, name: "conf-bf3-device", ns: dpfNS, setReady: true},
			{gvk: bfbGVK, name: "bf-bundle", ns: dpfNS, setReady: true},
			{gvk: provDPU, name: "conf-bf3", ns: dpfNS, setReady: true},
			// DPUFlavor has no status subresource: exposes no conditions and is
			// treated as non-blocking by the mapping's readiness CEL.
			{gvk: flavorGVK, name: "dpf-default-flavor", ns: dpfNS, setReady: false},
		},
		expectReady: true,
	},
}

var _ = Describe("Hybrid translation conformance", func() {
	for _, tc := range cases {
		tc := tc
		Context(tc.name, func() {
			It("stamps controller owner refs on every child (GC proxy) and mirrors status", func() {
				for _, ns := range tc.ensureNS {
					ensureNamespace(ns)
				}

				src := fromYAML(tc.sourceYAML)
				Expect(k8sClient.Create(ctx, src)).To(Succeed())
				DeferCleanup(func() {
					_ = k8sClient.Delete(ctx, src)
				})

				stored := &unstructured.Unstructured{}
				stored.SetGroupVersionKind(tc.srcGVK)
				Expect(k8sClient.Get(ctx, tc.srcNN, stored)).To(Succeed())
				uid := string(stored.GetUID())
				Expect(uid).NotTo(BeEmpty())

				reconcileOnce(tc.mappingName, tc.srcNN)

				By("asserting every emitted child is owned by the source (GC proxy)")
				for _, ch := range tc.children {
					obj := getObject(ch.gvk, types.NamespacedName{Name: ch.name, Namespace: ch.ns})
					assertControllerOwnerRef(obj, tc.srcGVK.Kind, tc.srcNN.Name, uid)
					if ch.setReady {
						markChildReady(ch.gvk, types.NamespacedName{Name: ch.name, Namespace: ch.ns})
					}
				}

				By("reconciling again to mirror child status onto the source")
				reconcileOnce(tc.mappingName, tc.srcNN)

				if tc.statusPruned {
					By("status mirroring not asserted: this source CRD declares an empty " +
						"status schema, so the apiserver prunes mirrored fields; GC/owner-refs " +
						"were asserted above and status activates once the CRD gains a status schema")
					return
				}
				if tc.expectReady {
					got := getObject(tc.srcGVK, tc.srcNN)
					cond := conditionByType(got, "Ready")
					Expect(cond).NotTo(BeNil(), "source is missing a mirrored Ready condition")
					Expect(cond["status"]).To(Equal("True"),
						"mirrored Ready condition should be True once ready children report Ready")
				}
			})
		})
	}
})

// reconcileOnce drives the translation.Reconciler for one mapping/source through
// a single Reconcile against the envtest apiserver.
func reconcileOnce(mappingName string, nn types.NamespacedName) {
	GinkgoHelper()
	spec, ok := specs[mappingName]
	Expect(ok).To(BeTrue(), "mapping %q not loaded", mappingName)
	r := &translation.Reconciler{Client: k8sClient, Scheme: scheme, Spec: spec}
	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
	Expect(err).NotTo(HaveOccurred())
}

func fromYAML(y string) *unstructured.Unstructured {
	GinkgoHelper()
	m := map[string]any{}
	Expect(yaml.Unmarshal([]byte(y), &m)).To(Succeed())
	return &unstructured.Unstructured{Object: m}
}

func ensureNamespace(name string) {
	GinkgoHelper()
	ns := &unstructured.Unstructured{}
	ns.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "Namespace"})
	ns.SetName(name)
	if err := k8sClient.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		Expect(err).NotTo(HaveOccurred())
	}
}

func getObject(gvk schema.GroupVersionKind, nn types.NamespacedName) *unstructured.Unstructured {
	GinkgoHelper()
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	Expect(k8sClient.Get(ctx, nn, obj)).To(Succeed(), "%s %s", gvk.Kind, nn)
	return obj
}

func assertControllerOwnerRef(obj *unstructured.Unstructured, kind, name, uid string) {
	GinkgoHelper()
	for _, ref := range obj.GetOwnerReferences() {
		if ref.Kind == kind && ref.Name == name {
			Expect(ref.UID).To(Equal(types.UID(uid)))
			Expect(ref.Controller).NotTo(BeNil())
			Expect(*ref.Controller).To(BeTrue())
			return
		}
	}
	Fail(fmt.Sprintf("no controller owner ref kind=%s name=%s on %s/%s; have %#v",
		kind, name, obj.GetKind(), obj.GetName(), obj.GetOwnerReferences()))
}

// markChildReady mocks the DPF operator publishing a Ready=True condition.
func markChildReady(gvk schema.GroupVersionKind, nn types.NamespacedName) {
	GinkgoHelper()
	child := getObject(gvk, nn)
	conds := []any{map[string]any{
		"type":               "Ready",
		"status":             "True",
		"reason":             "MockedReady",
		"message":            "set by conformance suite",
		"lastTransitionTime": time.Now().UTC().Format(time.RFC3339),
	}}
	Expect(unstructured.SetNestedSlice(child.Object, conds, "status", "conditions")).To(Succeed())
	Expect(k8sClient.Status().Update(ctx, child)).To(Succeed(), "status update on %s %s", gvk.Kind, nn)
}

func conditionByType(obj *unstructured.Unstructured, condType string) map[string]any {
	conds, ok, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if !ok {
		return nil
	}
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if m["type"] == condType {
			return m
		}
	}
	return nil
}

var _ = Describe("Cleanup on delete (annotation-tracked cross-namespace children)", func() {
	It("deletes annotation-tracked children via the finalizer when the source is removed", func() {
		ensureNamespace("opi")
		ensureNamespace(dpfNS)

		src := fromYAML(`
apiVersion: config.openshift.io/v1
kind: ServiceFunctionChain
metadata:
  name: xns-chain
  namespace: opi
spec:
  networkFunctions:
  - name: hbn
    chart:
      repository: https://helm.ngc.nvidia.com/nvidia/doca
      name: hbn
      version: v25.10.1
`)
		Expect(k8sClient.Create(ctx, src)).To(Succeed())
		srcNN := types.NamespacedName{Name: "xns-chain", Namespace: "opi"}
		childNN := types.NamespacedName{Name: "hbn", Namespace: dpfNS}
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, src)
			child := &unstructured.Unstructured{}
			child.SetGroupVersionKind(svcGVK)
			_ = k8sClient.Delete(ctx, child)
		})

		By("reconciling: child lands cross-namespace, annotation-tracked, source gets the finalizer")
		reconcileOnce("servicefunctionchain-xns", srcNN)

		child := getObject(svcGVK, childNN)
		Expect(child.GetOwnerReferences()).To(BeEmpty(),
			"cross-namespace child must NOT carry an owner reference (GC cannot reclaim it)")
		Expect(child.GetAnnotations()).To(
			HaveKeyWithValue(translation.AnnSource, "ServiceFunctionChain:opi/xns-chain"))

		stored := getObject(sfcGVK, srcNN)
		Expect(stored.GetFinalizers()).To(ContainElement(translation.CleanupFinalizer),
			"source must carry the cleanup finalizer so the child can be reclaimed")

		By("deleting the source and reconciling the deletion")
		Expect(k8sClient.Delete(ctx, stored)).To(Succeed())
		reconcileOnce("servicefunctionchain-xns", srcNN)

		By("the annotation-tracked child is gone")
		gotChild := &unstructured.Unstructured{}
		gotChild.SetGroupVersionKind(svcGVK)
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, childNN, gotChild))).To(BeTrue(),
			"annotation-tracked child should be deleted by the finalizer cleanup")

		By("the source finalizer is removed and the source is gone")
		gotSrc := &unstructured.Unstructured{}
		gotSrc.SetGroupVersionKind(sfcGVK)
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, srcNN, gotSrc))).To(BeTrue(),
			"source should be deleted once the cleanup finalizer is removed")
	})
})
