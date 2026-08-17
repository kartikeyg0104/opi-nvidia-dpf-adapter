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

package discovery

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestAnnotatorStampsSerialOnMatchingNode(t *testing.T) {
	scheme := newDPUScheme(t)
	dpu := newDPU("bf3-worker", "kind-worker")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(dpu).Build()

	a := &Annotator{
		Client:     c,
		Scheme:     scheme,
		Enumerator: MockEnumerator{Devices: []Device{StaticDevice("MT25066004A1", "0000:03:00.0", "BlueField-3")}},
		NodeName:   "kind-worker",
		BFBURL:     "https://example.invalid/fw.bfb",
	}

	if _, err := a.Reconcile(context.Background(), requestFor(dpu)); err != nil {
		t.Fatal(err)
	}

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(DataProcessingUnitGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Name: "bf3-worker"}, got); err != nil {
		t.Fatal(err)
	}
	ann := got.GetAnnotations()
	if ann[SerialNumberAnnotation] != "MT25066004A1" {
		t.Fatalf("serial annotation=%q", ann[SerialNumberAnnotation])
	}
	if ann[BFBURLAnnotation] != "https://example.invalid/fw.bfb" {
		t.Fatalf("bfb-url annotation=%q", ann[BFBURLAnnotation])
	}
}

func TestAnnotatorSkipsOtherNodes(t *testing.T) {
	scheme := newDPUScheme(t)
	dpu := newDPU("bf3-other", "other-worker")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(dpu).Build()

	a := &Annotator{
		Client:     c,
		Scheme:     scheme,
		Enumerator: MockEnumerator{Devices: []Device{StaticDevice("MT25066004A1", "0000:03:00.0", "BlueField-3")}},
		NodeName:   "kind-worker",
	}

	if _, err := a.Reconcile(context.Background(), requestFor(dpu)); err != nil {
		t.Fatal(err)
	}

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(DataProcessingUnitGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Name: "bf3-other"}, got); err != nil {
		t.Fatal(err)
	}
	if got.GetAnnotations()[SerialNumberAnnotation] != "" {
		t.Fatalf("stamped a DPU on a different node: %v", got.GetAnnotations())
	}
}

func TestAnnotatorIsIdempotent(t *testing.T) {
	scheme := newDPUScheme(t)
	dpu := newDPU("bf3-worker", "kind-worker")
	dpu.SetAnnotations(map[string]string{SerialNumberAnnotation: "MT25066004A1"})
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(dpu).Build()

	a := &Annotator{
		Client:     c,
		Scheme:     scheme,
		Enumerator: MockEnumerator{Devices: []Device{StaticDevice("MT25066004A1", "0000:03:00.0", "BlueField-3")}},
		NodeName:   "kind-worker",
	}

	if _, err := a.Reconcile(context.Background(), requestFor(dpu)); err != nil {
		t.Fatal(err)
	}
}

func TestAnnotatorRejectsEmptySerial(t *testing.T) {
	scheme := newDPUScheme(t)
	dpu := newDPU("bf3-worker", "kind-worker")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(dpu).Build()

	a := &Annotator{
		Client:     c,
		Scheme:     scheme,
		Enumerator: MockEnumerator{Devices: []Device{{PCIAddress: "0000:03:00.0"}}},
		NodeName:   "kind-worker",
	}

	if _, err := a.Reconcile(context.Background(), requestFor(dpu)); err == nil {
		t.Fatal("expected error for empty serial")
	}
}

func TestMappingYAMLUsesDiscoveryAnnotationKeys(t *testing.T) {
	path := filepath.Join("..", "..", "config", "mappings", "dataprocessingunit.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, key := range []string{SerialNumberAnnotation, BFBURLAnnotation, FlavorAnnotation, BFBNameAnnotation} {
		if !strings.Contains(body, key) {
			t.Errorf("mapping YAML missing annotation key %s", key)
		}
	}
}

func newDPUScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(DataProcessingUnitGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(DataProcessingUnitGVK.GroupVersion().WithKind("DataProcessingUnitList"), &unstructured.UnstructuredList{})
	metav1.AddToGroupVersion(s, DataProcessingUnitGVK.GroupVersion())
	return s
}

func newDPU(name, node string) *unstructured.Unstructured {
	dpu := &unstructured.Unstructured{}
	dpu.SetGroupVersionKind(DataProcessingUnitGVK)
	dpu.SetName(name)
	_ = unstructured.SetNestedField(dpu.Object, node, "spec", "nodeName")
	_ = unstructured.SetNestedField(dpu.Object, "BlueField-3", "spec", "dpuProductName")
	_ = unstructured.SetNestedField(dpu.Object, false, "spec", "isDpuSide")
	return dpu
}

func requestFor(obj *unstructured.Unstructured) reconcile.Request {
	return reconcile.Request{NamespacedName: types.NamespacedName{Name: obj.GetName(), Namespace: obj.GetNamespace()}}
}
