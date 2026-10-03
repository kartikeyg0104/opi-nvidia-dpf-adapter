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

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kartikeyg0104/opi-nvidia-dpf-adapter/pkg/mapping"
)

func TestPluralize(t *testing.T) {
	for _, tc := range []struct{ kind, want string }{
		// The kinds actually in play, with their real API plurals.
		{"DataProcessingUnit", "dataprocessingunits"},
		{"ServiceFunctionChain", "servicefunctionchains"},
		{"DPUService", "dpuservices"},
		{"DPUDevice", "dpudevices"},
		{"DPUFlavor", "dpuflavors"},
		{"DPU", "dpus"},
		{"BFB", "bfbs"},
		{"DSCDevice", "dscdevices"},
		{"DSCFirmware", "dscfirmwares"},
		{"DSCProfile", "dscprofiles"},
		// Consonant + y pluralizes to -ies, which is the case a naive +"s"
		// gets wrong: dscnodepolicys would be silently invalid RBAC.
		{"DSCNodePolicy", "dscnodepolicies"},
		{"NetworkPolicy", "networkpolicies"},
		// Vowel + y keeps the y.
		{"Gateway", "gateways"},
		// Sibilant endings take -es.
		{"Ingress", "ingresses"},
		{"Box", "boxes"},
		{"Switch", "switches"},
		{"", ""},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			if got := pluralize(tc.kind); got != tc.want {
				t.Errorf("pluralize(%q) = %q, want %q", tc.kind, got, tc.want)
			}
		})
	}
}

// TestRenderIsDeterministic matters because -check compares bytes: output that
// reordered between runs would fail the build at random.
func TestRenderIsDeterministic(t *testing.T) {
	specs, err := mapping.LoadDir(filepath.Join("..", "..", "config", "mappings"))
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	first, err := render(specs, "translation", "config/mappings")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		again, err := render(specs, "translation", "config/mappings")
		if err != nil {
			t.Fatal(err)
		}
		if string(again) != string(first) {
			t.Fatalf("render is not deterministic (run %d differed)", i)
		}
	}
}

// TestRenderCoversEveryMappedGroup is the claim this generator exists to make:
// a vendor drops in a mapping and its RBAC appears, with no Go edit.
func TestRenderCoversEveryMappedGroup(t *testing.T) {
	specs, err := mapping.LoadDir(filepath.Join("..", "..", "config", "mappings"))
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	out, err := render(specs, "translation", "config/mappings")
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)

	for _, s := range specs {
		// The source kind, its status and its finalizers.
		src := pluralize(s.Source.Kind)
		for _, want := range []string{src, src + "/status", src + "/finalizers"} {
			if !mentionsResource(got, s.Source.Group, want) {
				t.Errorf("mapping %q: no RBAC for %s/%s", s.Metadata.Name, s.Source.Group, want)
			}
		}
		// Every emitted target kind.
		for _, e := range s.Emit {
			res := pluralize(e.Target.Kind)
			if !mentionsResource(got, e.Target.Group, res) {
				t.Errorf("mapping %q: no RBAC for emitted %s/%s",
					s.Metadata.Name, e.Target.Group, res)
			}
		}
	}
}

// TestRenderPicksUpANewVendorWithNoGoEdit proves the property end to end by
// synthesising a vendor the repo has never seen.
func TestRenderPicksUpANewVendorWithNoGoEdit(t *testing.T) {
	dir := t.TempDir()
	const doc = `apiVersion: translation.opi.nvidia.com/v1alpha1
kind: FieldMapping
metadata:
  name: acme
source:
  group: config.openshift.io
  version: v1
  kind: DataProcessingUnit
emit:
  - target:
      group: dpu.acme.com
      version: v1alpha1
      kind: ACMENodePolicy
    name:
      from: metadata.name
    fields:
      - to: spec.serialNumber
        value: placeholder
`
	if err := os.WriteFile(filepath.Join(dir, "acme.yaml"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	specs, err := mapping.LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	out, err := render(specs, "translation", dir)
	if err != nil {
		t.Fatal(err)
	}
	// Note the plural: a new group AND the -ies pluralization, both derived.
	if !mentionsResource(string(out), "dpu.acme.com", "acmenodepolicies") {
		t.Errorf("a new vendor's group did not appear in the generated RBAC:\n%s", out)
	}
}

// mentionsResource reports whether a marker grants `resource` in `group`.
// Resources are joined with ';' in one marker per group, so a substring check
// has to respect those boundaries or "dpus" would match "dpuservices".
func mentionsResource(generated, group, resource string) bool {
	for _, line := range strings.Split(generated, "\n") {
		if !strings.Contains(line, "groups="+group+",") {
			continue
		}
		_, rest, ok := strings.Cut(line, "resources=")
		if !ok {
			continue
		}
		list, _, ok := strings.Cut(rest, ",verbs=")
		if !ok {
			continue
		}
		for _, r := range strings.Split(list, ";") {
			if r == resource {
				return true
			}
		}
	}
	return false
}
