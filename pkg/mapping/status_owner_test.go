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
	"strings"
	"testing"
)

// mappingDir is the shipped mapping set, relative to this package.
const mappingDir = "../../config/mappings"

// TestValidateRejectsReservedCondition is the merge-block: a mapping document
// that writes the daemon-owned condition must not load at all. Without this,
// the translation controller and the per-node dpu-operator daemon would both
// upsert Ready on the same OPI object and flap it on every reconcile.
func TestValidateRejectsReservedCondition(t *testing.T) {
	for _, condType := range []string{"Ready", "ready", "READY"} {
		t.Run(condType, func(t *testing.T) {
			s := minimalSpec(condType)
			err := s.Validate()
			if err == nil {
				t.Fatalf("Validate accepted status.conditions[0].type=%q; the dpu-operator "+
					"daemon owns that condition, so a mapping must not write it", condType)
			}
			// The error has to name the offending type, or whoever hits this in
			// CI cannot tell which condition to rename.
			if !strings.Contains(err.Error(), ReservedConditionType) {
				t.Errorf("error does not name %q: %v", ReservedConditionType, err)
			}
		})
	}
}

// TestValidateAcceptsVendorScopedCondition is the negative control: the rule
// must reject only the reserved type, not condition mirroring as such.
func TestValidateAcceptsVendorScopedCondition(t *testing.T) {
	for _, condType := range []string{"DPFReady", "DSCReady", "ReadyForTraffic"} {
		t.Run(condType, func(t *testing.T) {
			if err := minimalSpec(condType).Validate(); err != nil {
				t.Fatalf("Validate rejected vendor-scoped type %q: %v", condType, err)
			}
		})
	}
}

// TestShippedMappingsOwnDistinctConditions pins the single-writer model across
// the mapping set that actually ships: nothing writes the daemon's condition,
// and no two mappings on the same source kind write the same condition type
// (they would overwrite each other, since conditions are upserted by type).
func TestShippedMappingsOwnDistinctConditions(t *testing.T) {
	specs, err := LoadDir(mappingDir)
	if err != nil {
		t.Fatalf("LoadDir(%s): %v", mappingDir, err)
	}

	// Keyed by source kind + condition type; the value is the mapping that
	// claimed it, so a collision can name both sides.
	type claim struct{ source, condition string }
	owners := map[claim]string{}
	mirroring := 0

	for _, s := range specs {
		if s.Status == nil {
			continue
		}
		for _, c := range s.Status.Conditions {
			mirroring++
			if strings.EqualFold(c.Type, ReservedConditionType) {
				t.Errorf("mapping %q writes condition %q, owned by the dpu-operator daemon",
					s.Metadata.Name, c.Type)
				continue
			}
			k := claim{source: s.Source.Kind, condition: c.Type}
			if prev, dup := owners[k]; dup {
				t.Errorf("mappings %q and %q both write condition %q on source kind %s; "+
					"they would overwrite each other on every reconcile",
					prev, s.Metadata.Name, c.Type, s.Source.Kind)
				continue
			}
			owners[k] = s.Metadata.Name
		}
	}

	// Guard against the test passing because it found nothing to check (an
	// empty or renamed mapping dir would otherwise look like a clean run).
	if mirroring == 0 {
		t.Fatalf("no mirrored conditions found across %d mapping(s) in %s; "+
			"the invariant was not exercised", len(specs), mappingDir)
	}
	t.Logf("checked %d mirrored condition(s) across %d mapping(s)", mirroring, len(specs))
}

// minimalSpec is the smallest document that passes every other Validate rule,
// so a failure can only come from the condition type under test.
func minimalSpec(condType string) *Spec {
	return &Spec{
		APIVersion: APIVersion,
		Kind:       Kind,
		Metadata:   Metadata{Name: "unit-" + strings.ToLower(condType)},
		Source: ObjectRef{
			Group:   "config.openshift.io",
			Version: "v1",
			Kind:    "DataProcessingUnit",
		},
		Emit: []Emit{{
			Target: ObjectRef{
				Group:   "provisioning.dpu.nvidia.com",
				Version: "v1alpha1",
				Kind:    "DPUDevice",
			},
			Name:   Value{From: "metadata.name"},
			Fields: []Field{{To: "spec.serialNumber", Value: "unit-serial"}},
		}},
		Status: &StatusMapping{
			Conditions: []StatusCondition{{
				Type:   condType,
				Status: "children.size() > 0",
				Reason: "UnitTest",
			}},
		},
	}
}
