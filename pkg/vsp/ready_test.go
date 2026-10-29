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

package vsp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kartikeyg0104/opi-nvidia-dpf-adapter/pkg/discovery"
	"github.com/kartikeyg0104/opi-nvidia-dpf-adapter/pkg/lifecycle"
	"github.com/kartikeyg0104/opi-nvidia-dpf-adapter/pkg/mapping"
)

func TestReportReadyWritesDaemonConditionAfterHandshake(t *testing.T) {
	dpu := newReadyDPU("dh2-bluefield-2", "dh2", "MTEXAMPLE0002")
	_ = unstructured.SetNestedSlice(dpu.Object, []any{
		map[string]any{
			"type":               "DPFReady",
			"status":             "False",
			"reason":             "Rebooting",
			"message":            "NVIDIA DPU child is Rebooting",
			"lastTransitionTime": "2026-10-20T11:50:14Z",
		},
	}, "status", "conditions")
	cfg := newReadyConfig("dpu-operator-config")
	cl := fake.NewClientBuilder().WithScheme(readyScheme(t)).
		WithStatusSubresource(dpu, cfg).WithObjects(dpu, cfg).Build()

	err := ReportReady(context.Background(), cl, &HandshakeResult{
		Devices: map[string]*HandshakeDevice{
			"0000:0f:00.0": {ID: "MTEXAMPLE0002", Health: "HEALTHY"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	got := getReadyObject(t, cl, discovery.DataProcessingUnitGVK, "dh2-bluefield-2")
	ready := conditionOfType(t, got, mapping.ReservedConditionType)
	if ready["status"] != condStatusTrue || ready["reason"] != "DeviceHealthy" {
		t.Fatalf("DataProcessingUnit Ready = %#v, want True/DeviceHealthy", ready)
	}
	if conditionOfType(t, got, "DPFReady")["reason"] != "Rebooting" {
		t.Fatal("ReportReady must not overwrite mapping-owned DPFReady")
	}

	lcm := getReadyObject(t, cl, lifecycle.DpuOperatorConfigGVK, "dpu-operator-config")
	lcmReady := conditionOfType(t, lcm, mapping.ReservedConditionType)
	if lcmReady["status"] != condStatusTrue || lcmReady["reason"] != "DeviceHealthy" {
		t.Fatalf("DpuOperatorConfig Ready = %#v, want True/DeviceHealthy", lcmReady)
	}
}

func TestReportReadySkipsForeignSerial(t *testing.T) {
	dpu := newReadyDPU("other", "dh1", "AMDONLY")
	cl := fake.NewClientBuilder().WithScheme(readyScheme(t)).WithObjects(dpu).Build()
	if err := ReportReady(context.Background(), cl, &HandshakeResult{
		Devices: map[string]*HandshakeDevice{"0000:0f:00.0": {ID: "MTEXAMPLE0002", Health: "HEALTHY"}},
	}); err != nil {
		t.Fatal(err)
	}
	got := getReadyObject(t, cl, discovery.DataProcessingUnitGVK, "other")
	if raw, ok, _ := unstructured.NestedSlice(got.Object, "status", "conditions"); ok && len(raw) > 0 {
		t.Fatalf("foreign DPU gained conditions: %#v", raw)
	}
}

func TestVSPRoleCanPatchReadyStatus(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "config", "vsp", "rbac.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	if !strings.Contains(body, "dataprocessingunits/status") {
		t.Fatal("VSP/opi-daemon SA must patch dataprocessingunits/status to write Ready")
	}
	if !strings.Contains(body, "dpuoperatorconfigs/status") {
		t.Fatal("VSP/opi-daemon SA must patch dpuoperatorconfigs/status to write Ready")
	}
}

func TestIndependentDaemonUsesPrefixedVSPServiceAccount(t *testing.T) {
	ds := loadDaemonSet(t, "config", "opi-daemon", "daemonset.yaml")
	if ds.Spec.Template.Spec.ServiceAccountName != "nvidia-vsp" {
		t.Fatalf("serviceAccountName=%q, want nvidia-vsp so kustomize namePrefix becomes opi-nvidia-dpf-adapter-nvidia-vsp",
			ds.Spec.Template.Spec.ServiceAccountName)
	}
}

func readyScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	for _, gvk := range []schema.GroupVersionKind{
		discovery.DataProcessingUnitGVK,
		lifecycle.DpuOperatorConfigGVK,
	} {
		s.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		s.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
		metav1.AddToGroupVersion(s, gvk.GroupVersion())
	}
	return s
}

func newReadyDPU(name, node, serial string) *unstructured.Unstructured {
	dpu := &unstructured.Unstructured{}
	dpu.SetGroupVersionKind(discovery.DataProcessingUnitGVK)
	dpu.SetName(name)
	dpu.SetAnnotations(map[string]string{discovery.SerialNumberAnnotation: serial})
	_ = unstructured.SetNestedField(dpu.Object, node, "spec", "nodeName")
	_ = unstructured.SetNestedField(dpu.Object, "BlueField-2", "spec", "dpuProductName")
	return dpu
}

func newReadyConfig(name string) *unstructured.Unstructured {
	cfg := &unstructured.Unstructured{}
	cfg.SetGroupVersionKind(lifecycle.DpuOperatorConfigGVK)
	cfg.SetName(name)
	return cfg
}

func getReadyObject(
	t *testing.T, cl client.Client, gvk schema.GroupVersionKind, name string,
) *unstructured.Unstructured {
	t.Helper()
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(gvk)
	if err := cl.Get(context.Background(), types.NamespacedName{Name: name}, got); err != nil {
		t.Fatal(err)
	}
	return got
}

func conditionOfType(t *testing.T, obj *unstructured.Unstructured, typ string) map[string]any {
	t.Helper()
	raw, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if ok && m["type"] == typ {
			return m
		}
	}
	t.Fatalf("no %s on %s", typ, obj.GetName())
	return nil
}
