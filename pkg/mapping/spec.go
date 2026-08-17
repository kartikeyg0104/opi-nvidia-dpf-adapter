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
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// APIVersion is the mapping-spec apiVersion this interpreter understands.
const APIVersion = "translation.opi.nvidia.com/v1alpha1"

// Kind is the mapping-spec kind.
const Kind = "FieldMapping"

// Spec is a data-driven OPI→DPF translation document. The controller
// interprets this file; it does not contain Go switch statements on Kind.
type Spec struct {
	APIVersion string    `json:"apiVersion" yaml:"apiVersion"`
	Kind       string    `json:"kind" yaml:"kind"`
	Metadata   Metadata  `json:"metadata" yaml:"metadata"`
	Source     ObjectRef `json:"source" yaml:"source"`
	Emit       []Emit    `json:"emit" yaml:"emit"`
}

// Metadata names a mapping document.
type Metadata struct {
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// ObjectRef identifies a Kubernetes GVK the mapping reads or writes.
type ObjectRef struct {
	Group   string `json:"group" yaml:"group"`
	Version string `json:"version" yaml:"version"`
	Kind    string `json:"kind" yaml:"kind"`
}

// GVK returns the GroupVersionKind for this ref.
func (o ObjectRef) GVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: o.Group, Version: o.Version, Kind: o.Kind}
}

// Emit describes one DPF object produced from the OPI source.
// If ForEach is set, one object is emitted per list element.
type Emit struct {
	Target    ObjectRef `json:"target" yaml:"target"`
	Name      Value     `json:"name" yaml:"name"`
	Namespace Value     `json:"namespace,omitempty" yaml:"namespace,omitempty"`
	When      string    `json:"when,omitempty" yaml:"when,omitempty"`
	ForEach   *ForEach  `json:"forEach,omitempty" yaml:"forEach,omitempty"`
	Fields    []Field   `json:"fields" yaml:"fields"`
}

// ForEach expands an emit rule across a list on the source object.
type ForEach struct {
	// In is a JSONPath (dotted) selecting a list, e.g. spec.networkFunctions.
	In string `json:"in" yaml:"in"`
}

// Field copies or computes one destination field.
// Exactly one of From, CEL, or Value must be set.
type Field struct {
	// To is a dotted JSONPath on the destination object, e.g. spec.dpuNodeName.
	To string `json:"to" yaml:"to"`
	// From is a dotted JSONPath on the source object (or current forEach item
	// when prefixed with "item.").
	From string `json:"from,omitempty" yaml:"from,omitempty"`
	// CEL is a CEL expression evaluated against {source, item}.
	CEL string `json:"cel,omitempty" yaml:"cel,omitempty"`
	// Value is a literal YAML scalar, object, or list.
	Value any `json:"value,omitempty" yaml:"value,omitempty"`
	// Default is used when From/CEL evaluates to null or empty string.
	Default any `json:"default,omitempty" yaml:"default,omitempty"`
	// Required fails the mapping if the resolved value is still empty.
	Required bool `json:"required,omitempty" yaml:"required,omitempty"`
}

// Value is a name/namespace expression. Exactly one of From, CEL, or Value.
type Value struct {
	From  string `json:"from,omitempty" yaml:"from,omitempty"`
	CEL   string `json:"cel,omitempty" yaml:"cel,omitempty"`
	Value string `json:"value,omitempty" yaml:"value,omitempty"`
}

func (v Value) empty() bool {
	return v.From == "" && v.CEL == "" && v.Value == ""
}

func (v Value) asField() Field {
	return Field{From: v.From, CEL: v.CEL, Value: nonEmpty(v.Value)}
}

func nonEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Validate reports schema errors in the mapping document itself.
func (s *Spec) Validate() error {
	if s.APIVersion != APIVersion {
		return fmt.Errorf("apiVersion: want %s, got %q", APIVersion, s.APIVersion)
	}
	if s.Kind != Kind {
		return fmt.Errorf("kind: want %s, got %q", Kind, s.Kind)
	}
	if strings.TrimSpace(s.Metadata.Name) == "" {
		return fmt.Errorf("metadata.name is required")
	}
	if s.Source.Kind == "" || s.Source.Version == "" {
		return fmt.Errorf("source.group/version/kind is required")
	}
	if len(s.Emit) == 0 {
		return fmt.Errorf("emit: at least one target is required")
	}
	for i, e := range s.Emit {
		if e.Target.Kind == "" || e.Target.Version == "" {
			return fmt.Errorf("emit[%d].target is incomplete", i)
		}
		if e.Name.empty() {
			return fmt.Errorf("emit[%d].name is required", i)
		}
		if err := sourcesOK("emit[%d].name", i, e.Name.From, e.Name.CEL, e.Name.Value != ""); err != nil {
			return err
		}
		for j, f := range e.Fields {
			if f.To == "" {
				return fmt.Errorf("emit[%d].fields[%d].to is required", i, j)
			}
			n := 0
			if f.From != "" {
				n++
			}
			if f.CEL != "" {
				n++
			}
			if f.Value != nil {
				n++
			}
			if n != 1 {
				return fmt.Errorf("emit[%d].fields[%d]: exactly one of from, cel, or value must be set", i, j)
			}
		}
	}
	return nil
}

func sourcesOK(fmtStr string, i int, from, cel string, hasValue bool) error {
	n := 0
	if from != "" {
		n++
	}
	if cel != "" {
		n++
	}
	if hasValue {
		n++
	}
	if n != 1 {
		return fmt.Errorf(fmtStr+": exactly one of from, cel, or value must be set", i)
	}
	return nil
}
