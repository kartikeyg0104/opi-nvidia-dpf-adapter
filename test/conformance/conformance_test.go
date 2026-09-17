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
	"strings"
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
	// wantFields are dotted paths on the emitted child mapped to the value the
	// mapping must have written there. This is what turns the suite from an
	// owner-ref check into a translation check: it pins the destination shape,
	// so a vendor whose CRs nest what another vendor flattens is actually
	// exercised rather than assumed.
	wantFields map[string]any
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
	// readyCondition is the condition type this mapping mirrors, defaulting to
	// Ready. Mappings that share a source kind must not share a condition type:
	// the controller upserts conditions by type, so two vendors both writing
	// Ready would overwrite each other on every reconcile.
	readyCondition string
}

// readyConditionType is the condition a case asserts, defaulting to Ready.
func (c conformanceCase) readyConditionType() string {
	if c.readyCondition != "" {
		return c.readyCondition
	}
	return "Ready"
}

const (
	dpfNS = "dpf-operator-system"
	amdNS = "amd-dpu-system"
)

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
	{
		// Second vendor, same source kind, same engine, zero Go. Driven by the
		// real VPD product string off dh1 (the isolation spec below uses the
		// normalised "DSC2-100" form, so both spellings stay covered). The expected
		// destination shapes below are deliberately not the DPF ones: nested
		// refs instead of name strings, a nested firmware image, an inverted
		// drain flag, and a mode derived from spec.isDpuSide.
		name:        "DataProcessingUnit → DSCDevice/DSCProfile/DSCFirmware/DSCNodePolicy (AMD Pensando)",
		mappingName: "amd-dsc200",
		srcGVK:      dpuGVK,
		srcNN:       types.NamespacedName{Name: "conf-dsc2"},
		ensureNS:    []string{amdNS},
		sourceYAML: `
apiVersion: config.openshift.io/v1
kind: DataProcessingUnit
metadata:
  name: conf-dsc2
  annotations:
    dpu.amd.com/serial-number: "MYFLEPK31D02ZH"
    dpu.amd.com/pci-address: "0000:1b:00.0"
    dpu.amd.com/firmware-url: "https://example.invalid/dsc/1.46.0-E-28.tar"
spec:
  dpuProductName: Pensando DSC2-100 100G 2p QSFP56 DPU
  isDpuSide: false
  nodeName: dh1
`,
		children: []childSpec{
			{
				gvk: dscDeviceGVK, name: "conf-dsc2-dsc", ns: amdNS, setReady: true,
				wantFields: map[string]any{
					// hardware identity is carried, never invented
					"spec.deviceSerial": "MYFLEPK31D02ZH",
					"spec.pciAddress":   "0000:1b:00.0",
					// 1:1 JSONPath into a differently named destination.
					// The raw PCI VPD Product Name from dh1, not a tidied form:
					// the vendor guard must match what the hardware reports.
					"spec.productName": "Pensando DSC2-100 100G 2p QSFP56 DPU",
					// literal defaults with no OPI counterpart
					"spec.management.driver":    "ionic",
					"spec.management.interface": "oob",
				},
			},
			{
				gvk: dscFirmwareGVK, name: "dsc-fw-bundle", ns: amdNS, setReady: true,
				wantFields: map[string]any{
					// DPF puts this at the flat BFB.spec.url; AMD nests it.
					"spec.image.url":     "https://example.invalid/dsc/1.46.0-E-28.tar",
					"spec.image.version": "1.46.0-E-28",
				},
			},
			{
				gvk: dscPolicyGVK, name: "conf-dsc2", ns: amdNS, setReady: true,
				wantFields: map[string]any{
					"spec.nodeName": "dh1",
					// object refs where DPF uses bare name strings
					"spec.deviceRef.name":   "conf-dsc2-dsc",
					"spec.profileRef.name":  "dsc-default-profile",
					"spec.firmwareRef.name": "dsc-fw-bundle",
					// inverted sense of DPU.spec.nodeEffect.noEffect: true
					"spec.drain.enabled": false,
				},
			},
			{
				// DSCProfile has no status subresource, the same quirk DPUFlavor
				// has: it publishes no conditions and must not block readiness.
				gvk: dscProfileGVK, name: "dsc-default-profile", ns: amdNS, setReady: false,
				wantFields: map[string]any{
					// derived from spec.isDpuSide, a field the DPF mapping ignores
					"spec.mode":           "smartnic",
					"spec.hostInterfaces": int64(2),
				},
			},
		},
		expectReady:    true,
		readyCondition: "DSCReady",
	},
}

var _ = Describe("Hybrid translation conformance", func() {
	for _, tc := range cases {
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
					assertFields(obj, ch.wantFields)
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
					condType := tc.readyConditionType()
					got := getObject(tc.srcGVK, tc.srcNN)
					cond := conditionByType(got, condType)
					Expect(cond).NotTo(BeNil(), "source is missing a mirrored %s condition", condType)
					Expect(cond["status"]).To(Equal("True"),
						"mirrored %s condition should be True once ready children report Ready", condType)
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

// assertFields checks every dotted path in want against the emitted object.
// Values come back from the apiserver already JSON-normalised, so integers are
// int64 and a case states them that way.
func assertFields(obj *unstructured.Unstructured, want map[string]any) {
	GinkgoHelper()
	for path, expected := range want {
		got, found, err := unstructured.NestedFieldNoCopy(obj.Object, strings.Split(path, ".")...)
		Expect(err).NotTo(HaveOccurred(), "%s %s: reading %s", obj.GetKind(), obj.GetName(), path)
		Expect(found).To(BeTrue(), "%s %s: mapping did not write %s", obj.GetKind(), obj.GetName(), path)
		Expect(got).To(Equal(expected), "%s %s: %s", obj.GetKind(), obj.GetName(), path)
	}
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

// This is the Phase 2 claim in executable form. Two FieldMapping documents
// watch the same OPI kind (DataProcessingUnit) and target two different vendor
// APIs. In a mixed cluster cmd/main.go starts one controller per mapping, so
// every DataProcessingUnit is reconciled by both. Each must translate only its
// own hardware, decided by spec.dpuProductName in the mapping's `when` guard --
// no vendor branch anywhere in pkg/ or cmd/.
var _ = Describe("Multi-vendor isolation (two mappings, one OPI kind)", func() {
	It("routes each DataProcessingUnit to exactly one vendor's object set", func() {
		ensureNamespace(dpfNS)
		ensureNamespace(amdNS)

		amdNN := types.NamespacedName{Name: "iso-dsc2"}
		bfNN := types.NamespacedName{Name: "iso-bf3"}

		amdSrc := fromYAML(`
apiVersion: config.openshift.io/v1
kind: DataProcessingUnit
metadata:
  name: iso-dsc2
  annotations:
    dpu.amd.com/serial-number: "MYFLEPK31D02ZH"
    dpu.amd.com/firmware-url: "https://example.invalid/dsc.tar"
spec:
  dpuProductName: DSC2-100
  isDpuSide: false
  nodeName: dh1
`)
		bfSrc := fromYAML(`
apiVersion: config.openshift.io/v1
kind: DataProcessingUnit
metadata:
  name: iso-bf3
  annotations:
    provisioning.dpu.nvidia.com/serial-number: "MT1234ISO"
    dpu.nvidia.com/bfb-url: "https://example.invalid/fw.bfb"
spec:
  dpuProductName: BlueField-3
  isDpuSide: false
  nodeName: kind-worker
`)
		Expect(k8sClient.Create(ctx, amdSrc)).To(Succeed())
		Expect(k8sClient.Create(ctx, bfSrc)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, amdSrc)
			_ = k8sClient.Delete(ctx, bfSrc)
		})

		By("running every mapping against every source, as a mixed cluster would")
		for _, m := range []string{"dataprocessingunit", "amd-dsc200"} {
			reconcileOnce(m, amdNN)
			reconcileOnce(m, bfNN)
		}

		By("the AMD card produced AMD CRs and no DPF CRs")
		getObject(dscDeviceGVK, types.NamespacedName{Name: "iso-dsc2-dsc", Namespace: amdNS})
		getObject(dscPolicyGVK, types.NamespacedName{Name: "iso-dsc2", Namespace: amdNS})
		expectAbsent(devGVK, types.NamespacedName{Name: "iso-dsc2-device", Namespace: dpfNS})
		expectAbsent(provDPU, types.NamespacedName{Name: "iso-dsc2", Namespace: dpfNS})

		By("the BlueField card produced DPF CRs and no AMD CRs")
		getObject(devGVK, types.NamespacedName{Name: "iso-bf3-device", Namespace: dpfNS})
		getObject(provDPU, types.NamespacedName{Name: "iso-bf3", Namespace: dpfNS})
		expectAbsent(dscDeviceGVK, types.NamespacedName{Name: "iso-bf3-dsc", Namespace: amdNS})
		expectAbsent(dscPolicyGVK, types.NamespacedName{Name: "iso-bf3", Namespace: amdNS})

		By("the shared singletons stayed with their own vendor")
		// Both mappings default a flavor/profile name. They must not collide,
		// and neither may appear in the other vendor's namespace.
		expectAbsent(flavorGVK, types.NamespacedName{Name: "dpf-default-flavor", Namespace: amdNS})
		expectAbsent(dscProfileGVK, types.NamespacedName{Name: "dsc-default-profile", Namespace: dpfNS})
	})
})

// expectAbsent asserts no such object exists -- the negative half of the
// vendor-isolation proof.
func expectAbsent(gvk schema.GroupVersionKind, nn types.NamespacedName) {
	GinkgoHelper()
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	err := k8sClient.Get(ctx, nn, obj)
	Expect(apierrors.IsNotFound(err)).To(BeTrue(),
		"%s %s should not exist: the other vendor's mapping claimed this source", gvk.Kind, nn)
}
