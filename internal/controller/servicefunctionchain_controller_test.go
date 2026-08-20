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

package controller

import (
	"context"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kartikeyg0104/opi-nvidia-dpf-adapter/pkg/mapping"
)

const (
	sfcNamespace     = "default"
	sfcName          = "chain-1"
	chartNFName      = "hbn"
	imageNFName      = "legacy-fw"
	helmRepoURL      = "https://helm.ngc.nvidia.com/nvidia/doca"
	helmChartVersion = "v25.10.1"
)

var _ = Describe("ServiceFunctionChain translation", func() {
	var (
		spec          *mapping.Spec
		scheme        *runtime.Scheme
		fakeClient    client.Client
		dpuServiceGVK schema.GroupVersionKind
	)

	BeforeEach(func() {
		var err error
		spec, err = mapping.LoadFile(filepath.Join("..", "..", "config", "mappings", "servicefunctionchain.yaml"))
		Expect(err).NotTo(HaveOccurred())

		// This adapter does not vendor typed OPI or DPF Go APIs. Production
		// registers the same GVKs as unstructured (see cmd/main.go).
		scheme = runtime.NewScheme()
		Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
		registerUnstructuredGVK(scheme, spec.Source.GVK())
		Expect(spec.Emit).NotTo(BeEmpty())
		dpuServiceGVK = spec.Emit[0].Target.GVK()
		registerUnstructuredGVK(scheme, dpuServiceGVK)
	})

	Context("NetworkFunction with a Chart", func() {
		It("emits a DPUService with helmChart.source repoURL and version", func() {
			sfc := newSFC(sfcName, []any{
				map[string]any{
					"name": chartNFName,
					"chart": map[string]any{
						"repository": helmRepoURL,
						"name":       chartNFName,
						"version":    helmChartVersion,
					},
				},
			})
			fakeClient = fake.NewClientBuilder().WithScheme(scheme).WithObjects(sfc).Build()
			Expect(reconcileSFC(fakeClient, scheme, spec, sfc)).To(Succeed())

			svc := &unstructured.Unstructured{}
			svc.SetGroupVersionKind(dpuServiceGVK)
			err := fakeClient.Get(context.Background(), types.NamespacedName{
				Name: chartNFName, Namespace: sfcNamespace,
			}, svc)
			Expect(err).NotTo(HaveOccurred())

			repo, _, err := unstructured.NestedString(svc.Object, "spec", "helmChart", "source", "repoURL")
			Expect(err).NotTo(HaveOccurred())
			Expect(repo).To(Equal(helmRepoURL))
			version, _, err := unstructured.NestedString(svc.Object, "spec", "helmChart", "source", "version")
			Expect(err).NotTo(HaveOccurred())
			Expect(version).To(Equal(helmChartVersion))
		})
	})

	Context("NetworkFunction with only an Image", func() {
		It("skips the NF and does not create a DPUService", func() {
			sfc := newSFC(sfcName, []any{
				map[string]any{
					"name":  imageNFName,
					"image": "nginx:latest",
				},
			})
			fakeClient = fake.NewClientBuilder().WithScheme(scheme).WithObjects(sfc).Build()
			Expect(reconcileSFC(fakeClient, scheme, spec, sfc)).To(Succeed())

			svc := &unstructured.Unstructured{}
			svc.SetGroupVersionKind(dpuServiceGVK)
			err := fakeClient.Get(context.Background(), types.NamespacedName{
				Name: imageNFName, Namespace: sfcNamespace,
			}, svc)
			Expect(err).To(HaveOccurred())
			Expect(errors.IsNotFound(err)).To(BeTrue())
		})
	})
})

func reconcileSFC(c client.Client, scheme *runtime.Scheme, spec *mapping.Spec, sfc *unstructured.Unstructured) error {
	r := &TranslationReconciler{Client: c, Scheme: scheme, Spec: spec}
	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: sfc.GetName(), Namespace: sfc.GetNamespace()},
	})
	return err
}

func newSFC(name string, networkFunctions []any) *unstructured.Unstructured {
	sfc := &unstructured.Unstructured{Object: map[string]any{}}
	sfc.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "config.openshift.io", Version: "v1", Kind: "ServiceFunctionChain",
	})
	sfc.SetName(name)
	sfc.SetNamespace(sfcNamespace)
	Expect(unstructured.SetNestedSlice(sfc.Object, networkFunctions, "spec", "networkFunctions")).To(Succeed())
	return sfc
}

func registerUnstructuredGVK(s *runtime.Scheme, gvk schema.GroupVersionKind) {
	s.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
	metav1.AddToGroupVersion(s, gvk.GroupVersion())
}
