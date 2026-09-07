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
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kartikeyg0104/opi-nvidia-dpf-adapter/pkg/mapping"
)

var (
	dpuGVK = schema.GroupVersionKind{Group: "config.openshift.io", Version: "v1", Kind: "DataProcessingUnit"}
	sfcGVK = schema.GroupVersionKind{Group: "config.openshift.io", Version: "v1", Kind: "ServiceFunctionChain"}
	devGVK = schema.GroupVersionKind{Group: "provisioning.dpu.nvidia.com", Version: "v1alpha1", Kind: "DPUDevice"}
	svcGVK = schema.GroupVersionKind{Group: "svc.dpu.nvidia.com", Version: "v1alpha1", Kind: "DPUService"}
)

func registerUnstructured(s *runtime.Scheme, gvk schema.GroupVersionKind) {
	s.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
	metav1.AddToGroupVersion(s, gvk.GroupVersion())
}

func testScheme(gvks ...schema.GroupVersionKind) *runtime.Scheme {
	s := runtime.NewScheme()
	Expect(clientgoscheme.AddToScheme(s)).To(Succeed())
	for _, gvk := range gvks {
		registerUnstructured(s, gvk)
	}
	return s
}

func ownerOf(obj *unstructured.Unstructured, kind, name, uid string) metav1.OwnerReference {
	GinkgoHelper()
	refs := obj.GetOwnerReferences()
	Expect(refs).NotTo(BeEmpty(), "expected OwnerReferences on %s/%s", obj.GetKind(), obj.GetName())
	var found *metav1.OwnerReference
	for i := range refs {
		if refs[i].Kind == kind && refs[i].Name == name {
			found = &refs[i]
			break
		}
	}
	Expect(found).NotTo(BeNil(), "no OwnerReference kind=%s name=%s on %s; have %#v", kind, name, obj.GetName(), refs)
	Expect(found.UID).To(Equal(types.UID(uid)))
	Expect(found.Controller).NotTo(BeNil())
	Expect(*found.Controller).To(BeTrue())
	Expect(found.APIVersion).To(HavePrefix("config.openshift.io/"))
	return *found
}

var _ = Describe("Reconciler owner references", func() {
	var specs map[string]*mapping.Spec

	BeforeEach(func() {
		loaded, err := mapping.LoadDir(filepath.Join("..", "..", "config", "mappings"))
		Expect(err).NotTo(HaveOccurred())
		specs = map[string]*mapping.Spec{}
		for _, s := range loaded {
			specs[s.Metadata.Name] = s
		}
		Expect(specs).To(HaveKey("dataprocessingunit"))
		Expect(specs).To(HaveKey("servicefunctionchain"))
	})

	Describe("stampOwner", func() {
		It("lets a cluster-scoped DataProcessingUnit own a namespaced DPUDevice", func() {
			src := &unstructured.Unstructured{Object: map[string]any{}}
			src.SetGroupVersionKind(dpuGVK)
			src.SetName("bf3-worker")
			src.SetUID("dpu-uid-1")

			obj := &unstructured.Unstructured{Object: map[string]any{}}
			obj.SetGroupVersionKind(devGVK)
			obj.SetName("bf3-worker-device")
			obj.SetNamespace("dpf-operator-system")

			stampOwner(src, obj, testScheme(dpuGVK, devGVK))
			ref := ownerOf(obj, "DataProcessingUnit", "bf3-worker", "dpu-uid-1")
			Expect(ref.APIVersion).To(Equal("config.openshift.io/v1"))
		})

		It("lets a namespaced ServiceFunctionChain own a same-namespace DPUService", func() {
			src := &unstructured.Unstructured{Object: map[string]any{}}
			src.SetGroupVersionKind(sfcGVK)
			src.SetName("chain-1")
			src.SetNamespace("default")
			src.SetUID("sfc-uid-1")

			obj := &unstructured.Unstructured{Object: map[string]any{}}
			obj.SetGroupVersionKind(svcGVK)
			obj.SetName("hbn")
			obj.SetNamespace("default")

			stampOwner(src, obj, testScheme(sfcGVK, svcGVK))
			ownerOf(obj, "ServiceFunctionChain", "chain-1", "sfc-uid-1")
		})

		It("does not attach a cross-namespace owner reference", func() {
			src := &unstructured.Unstructured{Object: map[string]any{}}
			src.SetGroupVersionKind(sfcGVK)
			src.SetName("chain-1")
			src.SetNamespace("opi")
			src.SetUID("sfc-uid-x")

			obj := &unstructured.Unstructured{Object: map[string]any{}}
			obj.SetGroupVersionKind(svcGVK)
			obj.SetName("hbn")
			obj.SetNamespace("dpf-operator-system")

			stampOwner(src, obj, testScheme(sfcGVK, svcGVK))
			Expect(obj.GetOwnerReferences()).To(BeEmpty())
			Expect(obj.GetAnnotations()).To(HaveKeyWithValue(annSource, "ServiceFunctionChain:opi/chain-1"))
		})
	})

	Describe("Reconcile", func() {
		It("emits DPUService objects owned by the parent ServiceFunctionChain", func() {
			scheme := testScheme(sfcGVK, svcGVK)
			src := &unstructured.Unstructured{Object: map[string]any{
				"spec": map[string]any{
					"networkFunctions": []any{
						map[string]any{
							"name": "hbn",
							"chart": map[string]any{
								"repository": "https://helm.ngc.nvidia.com/nvidia/doca",
								"name":       "hbn",
								"version":    "v25.10.1",
							},
						},
					},
				},
			}}
			src.SetGroupVersionKind(sfcGVK)
			src.SetName("chain-1")
			src.SetNamespace("default")
			src.SetUID("sfc-uid-1")

			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(src).WithStatusSubresource(src).Build()
			r := &Reconciler{Client: cl, Scheme: scheme, Spec: specs["servicefunctionchain"]}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "chain-1", Namespace: "default"}})
			Expect(err).NotTo(HaveOccurred())

			got := &unstructured.Unstructured{}
			got.SetGroupVersionKind(svcGVK)
			Expect(cl.Get(ctx, types.NamespacedName{Name: "hbn", Namespace: "default"}, got)).To(Succeed())
			ownerOf(got, "ServiceFunctionChain", "chain-1", "sfc-uid-1")
		})

		It("emits DPUDevice objects owned by the parent DataProcessingUnit", func() {
			scheme := testScheme(dpuGVK, devGVK,
				schema.GroupVersionKind{Group: "provisioning.dpu.nvidia.com", Version: "v1alpha1", Kind: "DPUFlavor"},
				schema.GroupVersionKind{Group: "provisioning.dpu.nvidia.com", Version: "v1alpha1", Kind: "BFB"},
				schema.GroupVersionKind{Group: "provisioning.dpu.nvidia.com", Version: "v1alpha1", Kind: "DPU"},
			)
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
				"provisioning.dpu.nvidia.com/serial-number": "MT1234",
				"dpu.nvidia.com/bfb-url":                    "https://example.invalid/bf.bfb",
			})

			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(src).WithStatusSubresource(src).Build()
			r := &Reconciler{Client: cl, Scheme: scheme, Spec: specs["dataprocessingunit"]}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "bf3-worker"}})
			Expect(err).NotTo(HaveOccurred())

			for _, nameKind := range []struct {
				name string
				gvk  schema.GroupVersionKind
			}{
				{"bf3-worker-device", devGVK},
				{"dpf-default-flavor", schema.GroupVersionKind{Group: "provisioning.dpu.nvidia.com", Version: "v1alpha1", Kind: "DPUFlavor"}},
				{"bf-bundle", schema.GroupVersionKind{Group: "provisioning.dpu.nvidia.com", Version: "v1alpha1", Kind: "BFB"}},
				{"bf3-worker", schema.GroupVersionKind{Group: "provisioning.dpu.nvidia.com", Version: "v1alpha1", Kind: "DPU"}},
			} {
				got := &unstructured.Unstructured{}
				got.SetGroupVersionKind(nameKind.gvk)
				Expect(cl.Get(ctx, types.NamespacedName{Name: nameKind.name, Namespace: "dpf-operator-system"}, got)).To(Succeed(), nameKind.name)
				ownerOf(got, "DataProcessingUnit", "bf3-worker", "dpu-uid-1")
			}
		})
	})
})
