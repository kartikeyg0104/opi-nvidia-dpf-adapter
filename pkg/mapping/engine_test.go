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

package mapping

import (
	"os"
	"path/filepath"
	"testing"
)

const (
	testNodeName = "kind-worker"
	testNFName   = "hbn"
)

func TestApplyNodeNameMapping(t *testing.T) {
	spec := &Spec{
		APIVersion: APIVersion,
		Kind:       Kind,
		Metadata:   Metadata{Name: "dpu"},
		Source:     ObjectRef{Group: "config.openshift.io", Version: "v1", Kind: "DataProcessingUnit"},
		Emit: []Emit{{
			Target: ObjectRef{Group: "provisioning.dpu.nvidia.com", Version: "v1alpha1", Kind: "DPU"},
			Name:   Value{From: "metadata.name"},
			Namespace: Value{CEL: `source.metadata.?namespace.orValue('') != "" ` +
				`? source.metadata.namespace : "dpf-operator-system"`},
			Fields: []Field{
				{To: "spec.dpuNodeName", From: "spec.nodeName"},
				{To: "spec.nodeEffect.noEffect", Value: true},
				{
					To:      "spec.dpuFlavor",
					CEL:     "source.metadata.?annotations['dpu.nvidia.com/flavor'].orValue('')",
					Default: "dpf-default-flavor",
				},
			},
		}},
	}
	source := object(
		map[string]any{"name": "worker-1"},
		map[string]any{"nodeName": testNodeName, "dpuProductName": "BlueField-3"},
	)
	objs, err := Apply(spec, source)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 1 {
		t.Fatalf("got %d objects, want 1", len(objs))
	}
	u := objs[0]
	if u.GetName() != "worker-1" {
		t.Errorf("name=%s", u.GetName())
	}
	if u.GetNamespace() != "dpf-operator-system" {
		t.Errorf("namespace=%s", u.GetNamespace())
	}
	if node := nested(u.Object, "spec", "dpuNodeName"); node != testNodeName {
		t.Errorf("dpuNodeName=%v", node)
	}
	if noEffect := nested(u.Object, "spec", "nodeEffect", "noEffect"); noEffect != true {
		t.Errorf("noEffect=%v", noEffect)
	}
	if flavor := nested(u.Object, "spec", "dpuFlavor"); flavor != "dpf-default-flavor" {
		t.Errorf("dpuFlavor=%v (annotation missing, default should apply)", flavor)
	}
}

func TestApplyForEachChart(t *testing.T) {
	spec := &Spec{
		APIVersion: APIVersion,
		Kind:       Kind,
		Metadata:   Metadata{Name: "sfc"},
		Source:     ObjectRef{Group: "config.openshift.io", Version: "v1", Kind: "ServiceFunctionChain"},
		Emit: []Emit{{
			Target:  ObjectRef{Group: "svc.dpu.nvidia.com", Version: "v1alpha1", Kind: "DPUService"},
			Name:    Value{CEL: "item.name"},
			When:    "'chart' in item",
			ForEach: &ForEach{In: "spec.networkFunctions"},
			Fields: []Field{
				{To: "spec.helmChart.source.repoURL", CEL: "item.chart.repository", Required: true},
				{To: "spec.helmChart.source.version", CEL: "item.chart.version", Required: true},
				{To: "spec.serviceID", From: "item.name"},
			},
		}},
	}
	source := object(
		map[string]any{"name": "chain-1", "namespace": "default"},
		map[string]any{
			"networkFunctions": []any{
				map[string]any{"name": "legacy-fw", "image": "nginx:latest"},
				map[string]any{"name": testNFName, "chart": map[string]any{
					"repository": "https://helm.ngc.nvidia.com/nvidia/doca",
					"name":       testNFName,
					"version":    "v25.10.1",
				}},
			},
		},
	)
	objs, err := Apply(spec, source)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 1 {
		t.Fatalf("got %d objects, want 1 (image-only NF skipped)", len(objs))
	}
	if objs[0].GetName() != testNFName {
		t.Errorf("name=%s", objs[0].GetName())
	}
	url := nested(objs[0].Object, "spec", "helmChart", "source", "repoURL")
	if url != "https://helm.ngc.nvidia.com/nvidia/doca" {
		t.Errorf("repoURL=%v", url)
	}
}

func TestLoadRealMappings(t *testing.T) {
	dir := filepath.Join("..", "..", "config", "mappings")
	if _, err := os.Stat(dir); err != nil {
		t.Skip(err)
	}
	specs, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) < 2 {
		t.Fatalf("expected dataprocessingunit + servicefunctionchain mappings, got %d", len(specs))
	}

	dpuSrc := object(
		map[string]any{
			"name": "worker-1",
			"annotations": map[string]any{
				"provisioning.dpu.nvidia.com/serial-number": "MT1234",
				"dpu.nvidia.com/bfb-url":                    "https://example.invalid/bf.bfb",
			},
		},
		map[string]any{"nodeName": testNodeName},
	)
	var dpuSpec *Spec
	for _, s := range specs {
		if s.Metadata.Name == "dataprocessingunit" {
			dpuSpec = s
		}
	}
	if dpuSpec == nil {
		t.Fatal("missing dataprocessingunit mapping")
	}
	objs, err := Apply(dpuSpec, dpuSrc)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 4 {
		t.Fatalf("DataProcessingUnit should emit 4 DPF objects, got %d", len(objs))
	}
}

func TestLoadDir(t *testing.T) {
	dir := t.TempDir()
	body := []byte(`
apiVersion: translation.opi.nvidia.com/v1alpha1
kind: FieldMapping
metadata:
  name: example
source:
  group: config.openshift.io
  version: v1
  kind: DataProcessingUnit
emit:
  - target:
      group: provisioning.dpu.nvidia.com
      version: v1alpha1
      kind: DPU
    name:
      from: metadata.name
    fields:
      - to: spec.dpuNodeName
        from: spec.nodeName
`)
	if err := os.WriteFile(filepath.Join(dir, "dpu.yaml"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	kustomize := []byte("configMapGenerator:\n- name: mappings\n  files:\n  - dpu.yaml\n")
	if err := os.WriteFile(filepath.Join(dir, "kustomization.yaml"), kustomize, 0o644); err != nil {
		t.Fatal(err)
	}
	specs, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 || specs[0].Metadata.Name != "example" {
		t.Fatalf("%+v", specs)
	}
}

func object(meta, spec map[string]any) map[string]any {
	return map[string]any{"metadata": meta, "spec": spec}
}

func nested(obj map[string]any, keys ...string) any {
	cur := any(obj)
	for _, k := range keys {
		m, ok := asMap(cur)
		if !ok {
			return nil
		}
		next, ok := m[k]
		if !ok {
			return nil
		}
		cur = next
	}
	return cur
}
