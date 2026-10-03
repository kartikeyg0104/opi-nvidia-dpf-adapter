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
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

const schemaPath = "../../config/mappings/fieldmapping.schema.json"

// schemaDoc is just enough of JSON Schema to walk properties by $defs name.
type schemaDoc struct {
	Properties map[string]json.RawMessage `json:"properties"`
	Defs       map[string]schemaNode      `json:"$defs"`
}

type schemaNode struct {
	Properties map[string]json.RawMessage `json:"properties"`
	Required   []string                   `json:"required"`
}

func loadSchema(t *testing.T) schemaDoc {
	t.Helper()
	b, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("reading schema: %v", err)
	}
	var d schemaDoc
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	return d
}

// TestSchemaMatchesGoStructs is the drift guard.
//
// The schema is hand-written, so without this it would silently fall behind the
// Go structs the controller actually uses -- and a vendor's editor would report
// a valid field as an error, or accept one the engine ignores. Checked by
// reflection over the json tags rather than by validating documents, which
// keeps it dependency-free and makes a missing field name the failure message.
func TestSchemaMatchesGoStructs(t *testing.T) {
	schema := loadSchema(t)

	// Each Go struct paired with the $defs entry that describes it. Spec itself
	// lives at the document root rather than in $defs.
	for _, tc := range []struct {
		def string
		typ reflect.Type
	}{
		{"", reflect.TypeOf(Spec{})},
		{"metadata", reflect.TypeOf(Metadata{})},
		{"objectRef", reflect.TypeOf(ObjectRef{})},
		{"defaults", reflect.TypeOf(Defaults{})},
		{"emit", reflect.TypeOf(Emit{})},
		{"forEach", reflect.TypeOf(ForEach{})},
		{"field", reflect.TypeOf(Field{})},
		{"value", reflect.TypeOf(Value{})},
		{"statusMapping", reflect.TypeOf(StatusMapping{})},
		{"statusCondition", reflect.TypeOf(StatusCondition{})},
	} {
		name := tc.def
		if name == "" {
			name = "(root)"
		}
		t.Run(name, func(t *testing.T) {
			props := schema.Properties
			if tc.def != "" {
				node, ok := schema.Defs[tc.def]
				if !ok {
					t.Fatalf("schema has no $defs/%s for Go type %s", tc.def, tc.typ.Name())
				}
				props = node.Properties
			}

			goFields := jsonFieldNames(tc.typ)

			for f := range goFields {
				if _, ok := props[f]; !ok {
					t.Errorf("%s.%s exists in Go but not in the schema", tc.typ.Name(), f)
				}
			}
			for p := range props {
				if !goFields[p] {
					t.Errorf("schema declares %q for %s, which the Go struct does not have",
						p, tc.typ.Name())
				}
			}
		})
	}
}

// TestSchemaReservesReadyCondition checks the schema encodes the same rule
// Validate enforces, so an editor rejects the reserved type before the
// controller ever refuses to start.
func TestSchemaReservesReadyCondition(t *testing.T) {
	b, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	defs, _ := raw["$defs"].(map[string]any)
	cond, _ := defs["statusCondition"].(map[string]any)
	props, _ := cond["properties"].(map[string]any)
	typ, _ := props["type"].(map[string]any)

	not, ok := typ["not"].(map[string]any)
	if !ok {
		t.Fatalf("statusCondition.type has no `not` constraint; the schema does not "+
			"reserve %q the way Spec.Validate does", ReservedConditionType)
	}
	pat, _ := not["pattern"].(string)
	// Case-insensitive, matching Validate's strings.EqualFold.
	for _, spelling := range []string{"Ready", "ready", "READY"} {
		if !strings.Contains(strings.ToLower(pat), strings.ToLower(spelling[:1])) {
			t.Errorf("the reserved-type pattern %q does not look like it covers %q", pat, spelling)
		}
	}
}

// TestShippedMappingsDeclareTheSchema keeps editor validation switched on: a
// mapping without the modeline gets no completion or error highlighting, which
// is most of the point of shipping a schema.
func TestShippedMappingsDeclareTheSchema(t *testing.T) {
	for _, p := range []string{
		mappingDir + "/dataprocessingunit.yaml",
		mappingDir + "/servicefunctionchain.yaml",
		mappingDir + "/amd-dsc200.yaml",
		"../../config/vendor-template/fieldmapping.yaml",
	} {
		t.Run(p, func(t *testing.T) {
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("%v", err)
			}
			if !strings.Contains(string(b), "yaml-language-server: $schema=") {
				t.Errorf("%s has no `# yaml-language-server: $schema=` modeline", p)
			}
		})
	}
}

// jsonFieldNames returns the json tag names of a struct's exported fields,
// ignoring "-" and stripping options like ",omitempty".
func jsonFieldNames(t reflect.Type) map[string]bool {
	out := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue // unexported
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name
		}
		out[name] = true
	}
	return out
}
