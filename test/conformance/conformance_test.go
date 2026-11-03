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

	"github.com/kartikeyg0104/opi-nvidia-dpf-adapter/pkg/mapping"
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
	// expectReady asserts the source carries the mirrored condition named by
	// readyCondition with status True, after status-capable children are
	// marked ready.
	expectReady bool
	// readyCondition is the condition type this mapping mirrors. It has no
	// default and must be vendor-scoped: mappings that share a source kind must
	// not share a condition type, because the controller upserts conditions by
	// type and two vendors both writing one would overwrite each other on every
	// reconcile. It also may not be "Ready" -- that condition belongs to the
	// per-node dpu-operator daemon (mapping.ReservedConditionType).
	readyCondition string
}

// readyConditionType is the condition a case asserts. There is deliberately no
// default: a case that asserts readiness must name the condition it owns, so a
// mapping silently falling back onto the daemon's Ready cannot pass.
func (c conformanceCase) readyConditionType() string {
	GinkgoHelper()
	Expect(c.readyCondition).NotTo(BeEmpty(),
		"case %q sets expectReady but names no readyCondition", c.name)
	Expect(c.readyCondition).NotTo(Equal(mapping.ReservedConditionType),
		"case %q mirrors %q, which the dpu-operator daemon owns", c.name, mapping.ReservedConditionType)
	return c.readyCondition
}

const (
	dpfNS = "dpf-operator-system"
	amdNS = "amd-dpu-system"
)

var cases = []conformanceCase{
	{
		name:        "ServiceFunctionChain → DPUService + DPUServiceInterface + DPUServiceChain",
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
      version: v26.4.0
`,
		children: []childSpec{
			{
				gvk: svcGVK, name: "hbn", ns: "default", setReady: true,
				wantFields: map[string]any{
					"spec.serviceID":                "hbn",
					"spec.helmChart.source.repoURL": "https://helm.ngc.nvidia.com/nvidia/doca",
					"spec.helmChart.source.chart":   "hbn",
					"spec.helmChart.source.version": "v26.4.0",
					"spec.security.privileged":      true,
				},
			},
			{
				gvk: ifcGVK, name: "hbn-ifc", ns: "default", setReady: true,
				wantFields: map[string]any{
					"spec.template.spec.template.spec.interfaceType":         "service",
					"spec.template.spec.template.spec.service.serviceID":     "hbn",
					"spec.template.spec.template.spec.service.interfaceName": "net0",
					"spec.template.spec.template.spec.service.network":       "default",
				},
			},
			{
				gvk: chainGVK, name: "conf-chain", ns: "default", setReady: true,
			},
		},
		statusPruned: false, // pinned OPI SFC CRD now keeps status.conditions.
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
    discovery.opiproject.org/product-name: "BlueField-3"
    provisioning.dpu.nvidia.com/serial-number: "MT1234CONF"
    provisioning.dpu.nvidia.com/pci-address: "0000-03-00"
    dpu.nvidia.com/bfb-url: "https://example.invalid/fw.bfb"
spec:
  dpuProductName: BlueField-3
  isDpuSide: false
  nodeName: kind-worker
`,
		children: []childSpec{
			{gvk: devGVK, name: "mt1234conf", ns: dpfNS, setReady: true},
			{gvk: bfbGVK, name: "bf-bundle", ns: dpfNS, setReady: true},
			{gvk: provDPU, name: "conf-bf3", ns: dpfNS, setReady: true},
			// DPUFlavor has no status subresource: exposes no conditions and is
			// treated as non-blocking by the mapping's readiness CEL.
			{gvk: flavorGVK, name: "dpf-default-flavor", ns: dpfNS, setReady: false},
		},
		expectReady:    true,
		readyCondition: "DPFReady",
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
    dpu.amd.com/serial-number: "DSCEXAMPLE0001"
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
					"spec.deviceSerial": "DSCEXAMPLE0001",
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

					By("leaving the daemon-owned condition untouched")
					// Single-writer status model. Validate blocks a mapping that
					// declares Ready; this asserts the running controller never
					// puts one on the source by any other route, so the per-node
					// dpu-operator daemon stays the only writer of Ready.
					Expect(conditionByType(got, mapping.ReservedConditionType)).To(BeNil(),
						"controller wrote %q on the source; that condition belongs to the "+
							"dpu-operator daemon and the two would flap it every reconcile",
						mapping.ReservedConditionType)
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
      version: v26.4.0
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
    dpu.amd.com/serial-number: "DSCEXAMPLE0001"
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
    discovery.opiproject.org/product-name: "BlueField-3"
    provisioning.dpu.nvidia.com/serial-number: "MT1234ISO"
    provisioning.dpu.nvidia.com/pci-address: "0000-03-00"
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
		getObject(devGVK, types.NamespacedName{Name: "mt1234iso", Namespace: dpfNS})
		getObject(provDPU, types.NamespacedName{Name: "iso-bf3", Namespace: dpfNS})
		expectAbsent(dscDeviceGVK, types.NamespacedName{Name: "iso-bf3-dsc", Namespace: amdNS})
		expectAbsent(dscPolicyGVK, types.NamespacedName{Name: "iso-bf3", Namespace: amdNS})

		By("the shared singletons stayed with their own vendor")
		// Both mappings default a flavor/profile name. They must not collide,
		// and neither may appear in the other vendor's namespace.
		expectAbsent(flavorGVK, types.NamespacedName{Name: "dpf-default-flavor", Namespace: amdNS})
		expectAbsent(dscProfileGVK, types.NamespacedName{Name: "dsc-default-profile", Namespace: dpfNS})

		By("neither card carries the other vendor's condition")
		// The status.when ownership gate. Before it, a mapping whose emits were
		// all guarded off still evaluated its readiness roll-up -- over zero
		// children -- and parked a False condition on the other vendor's card.
		// Objects isolating correctly while status did not is exactly the gap
		// this closes, so it is asserted here rather than only in unit tests.
		amdGot := getObject(dpuGVK, amdNN)
		Expect(conditionByType(amdGot, "DPFReady")).To(BeNil(),
			"the DPF mapping wrote DPFReady onto an AMD card it emitted nothing for")

		bfGot := getObject(dpuGVK, bfNN)
		Expect(conditionByType(bfGot, "DSCReady")).To(BeNil(),
			"the AMD mapping wrote DSCReady onto a BlueField card it emitted nothing for")

		By("each card still carries its own vendor's condition")
		// The gate must suppress only foreign mappings, not all mirroring.
		Expect(conditionByType(amdGot, "DSCReady")).NotTo(BeNil(),
			"the AMD card lost its own DSCReady condition; the gate is too aggressive")
		Expect(conditionByType(bfGot, "DPFReady")).NotTo(BeNil(),
			"the BlueField card lost its own DPFReady condition; the gate is too aggressive")
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

// Task 04.04's DONE WHEN in executable form: create, update, shrink, rename
// and delete must each converge to exactly the expected child set, with no
// orphaned member of a chain that was shrunk or renamed. Kubernetes GC only
// reclaims children when the source itself dies, so every spec change needs
// active pruning; this is asserted against the real apiserver so server-side
// apply and live UIDs are in play.
var _ = Describe("Stale child convergence (create, update, shrink, rename, delete)", func() {
	// setNFs rewrites spec.networkFunctions on the stored source and persists it.
	setNFs := func(nn types.NamespacedName, names ...string) {
		GinkgoHelper()
		src := getObject(sfcGVK, nn)
		nfs := make([]any, 0, len(names))
		for _, n := range names {
			nfs = append(nfs, map[string]any{"name": n, "chart": map[string]any{
				"repository": "https://helm.ngc.nvidia.com/nvidia/doca", "name": n, "version": "v26.4.0"}})
		}
		Expect(unstructured.SetNestedField(src.Object, nfs, "spec", "networkFunctions")).To(Succeed())
		Expect(k8sClient.Update(ctx, src)).To(Succeed())
	}

	It("converges the child set on every spec change (owner-reference track)", func() {
		ensureNamespace("opi")
		ownNN := types.NamespacedName{Name: "conv-chain", Namespace: "opi"}

		src := fromYAML(`
apiVersion: config.openshift.io/v1
kind: ServiceFunctionChain
metadata:
  name: conv-chain
  namespace: opi
spec:
  networkFunctions:
  - name: alpha
    chart: {repository: "https://helm.ngc.nvidia.com/nvidia/doca", name: alpha, version: v26.3.1}
  - name: beta
    chart: {repository: "https://helm.ngc.nvidia.com/nvidia/doca", name: beta, version: v26.3.1}
  - name: gamma
    chart: {repository: "https://helm.ngc.nvidia.com/nvidia/doca", name: gamma, version: v26.3.1}
`)
		Expect(k8sClient.Create(ctx, src)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, src)
			for _, n := range []string{"alpha", "beta", "gamma", "delta"} {
				child := &unstructured.Unstructured{}
				child.SetGroupVersionKind(svcGVK)
				child.SetName(n)
				child.SetNamespace("opi")
				_ = k8sClient.Delete(ctx, child)
				ifc := &unstructured.Unstructured{}
				ifc.SetGroupVersionKind(ifcGVK)
				ifc.SetName(n + "-ifc")
				ifc.SetNamespace("opi")
				_ = k8sClient.Delete(ctx, ifc)
			}
			chain := &unstructured.Unstructured{}
			chain.SetGroupVersionKind(chainGVK)
			chain.SetName("conv-chain")
			chain.SetNamespace("opi")
			_ = k8sClient.Delete(ctx, chain)
		})

		By("creating three children for three chain members")
		reconcileOnce("servicefunctionchain", ownNN)
		for _, n := range []string{"alpha", "beta", "gamma"} {
			getObject(svcGVK, types.NamespacedName{Name: n, Namespace: "opi"})
		}

		By("updating alpha in place: same name, new chart version, still one child")
		setNFs(ownNN, "alpha", "beta", "gamma")
		reconcileOnce("servicefunctionchain", ownNN)
		alpha := getObject(svcGVK, types.NamespacedName{Name: "alpha", Namespace: "opi"})
		ver, found, err := unstructured.NestedString(alpha.Object, "spec", "helmChart", "source", "version")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(ver).To(Equal("v26.4.0"))

		By("shrinking: gamma is gone in the same reconcile, alpha and beta remain")
		setNFs(ownNN, "alpha", "beta")
		reconcileOnce("servicefunctionchain", ownNN)
		expectAbsent(svcGVK, types.NamespacedName{Name: "gamma", Namespace: "opi"})
		expectAbsent(ifcGVK, types.NamespacedName{Name: "gamma-ifc", Namespace: "opi"})
		getObject(svcGVK, types.NamespacedName{Name: "beta", Namespace: "opi"})

		By("renaming beta to delta: beta's child is pruned, delta's is created")
		setNFs(ownNN, "alpha", "delta")
		reconcileOnce("servicefunctionchain", ownNN)
		expectAbsent(svcGVK, types.NamespacedName{Name: "beta", Namespace: "opi"})
		expectAbsent(ifcGVK, types.NamespacedName{Name: "beta-ifc", Namespace: "opi"})
		getObject(svcGVK, types.NamespacedName{Name: "delta", Namespace: "opi"})
		getObject(ifcGVK, types.NamespacedName{Name: "delta-ifc", Namespace: "opi"})

		By("the surviving children still carry the source as controller owner")
		assertControllerOwnerRef(alpha, "ServiceFunctionChain", "conv-chain",
			string(getObject(sfcGVK, ownNN).GetUID()))
	})

	It("prunes on the annotation track and fully cleans up on delete", func() {
		ensureNamespace("opi")
		ensureNamespace(dpfNS)
		xnn := types.NamespacedName{Name: "conv-xns", Namespace: "opi"}

		src := fromYAML(`
apiVersion: config.openshift.io/v1
kind: ServiceFunctionChain
metadata:
  name: conv-xns
  namespace: opi
spec:
  networkFunctions:
  - name: alpha
    chart: {repository: "https://helm.ngc.nvidia.com/nvidia/doca", name: alpha, version: v26.4.0}
  - name: beta
    chart: {repository: "https://helm.ngc.nvidia.com/nvidia/doca", name: beta, version: v26.4.0}
`)
		Expect(k8sClient.Create(ctx, src)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, src) })

		reconcileOnce("servicefunctionchain-xns", xnn)
		getObject(svcGVK, types.NamespacedName{Name: "beta", Namespace: dpfNS})

		By("renaming beta to delta: the annotation-tracked child is pruned, not leaked")
		xsrc := getObject(sfcGVK, xnn)
		Expect(unstructured.SetNestedField(xsrc.Object, []any{
			map[string]any{"name": "alpha", "chart": map[string]any{
				"repository": "https://helm.ngc.nvidia.com/nvidia/doca", "name": "alpha", "version": "v26.4.0"}},
			map[string]any{"name": "delta", "chart": map[string]any{
				"repository": "https://helm.ngc.nvidia.com/nvidia/doca", "name": "delta", "version": "v26.4.0"}},
		}, "spec", "networkFunctions")).To(Succeed())
		Expect(k8sClient.Update(ctx, xsrc)).To(Succeed())
		reconcileOnce("servicefunctionchain-xns", xnn)

		expectAbsent(svcGVK, types.NamespacedName{Name: "beta", Namespace: dpfNS})
		getObject(svcGVK, types.NamespacedName{Name: "delta", Namespace: dpfNS})

		By("deleting the source: the finalizer reclaims every remaining child")
		Expect(k8sClient.Delete(ctx, getObject(sfcGVK, xnn))).To(Succeed())
		reconcileOnce("servicefunctionchain-xns", xnn)
		expectAbsent(svcGVK, types.NamespacedName{Name: "alpha", Namespace: dpfNS})
		expectAbsent(svcGVK, types.NamespacedName{Name: "delta", Namespace: dpfNS})
		expectAbsent(sfcGVK, xnn)
	})
})
