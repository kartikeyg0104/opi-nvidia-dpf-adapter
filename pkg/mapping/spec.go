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
	APIVersion string         `json:"apiVersion" yaml:"apiVersion"`
	Kind       string         `json:"kind" yaml:"kind"`
	Metadata   Metadata       `json:"metadata" yaml:"metadata"`
	Source     ObjectRef      `json:"source" yaml:"source"`
	Defaults   *Defaults      `json:"defaults,omitempty" yaml:"defaults,omitempty"`
	Emit       []Emit         `json:"emit" yaml:"emit"`
	Status     *StatusMapping `json:"status,omitempty" yaml:"status,omitempty"`
}

// Metadata names a mapping document.
type Metadata struct {
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// Defaults holds values inherited by every emit that does not set them.
// It removes per-emit boilerplate (notably the DPF namespace fallback that
// otherwise repeats as an identical CEL expression on every target).
type Defaults struct {
	// Namespace is applied to any emit whose own namespace is unset.
	Namespace Value `json:"namespace,omitempty" yaml:"namespace,omitempty"`
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
	// CEL is a CEL expression evaluated against {source, item, children}.
	CEL string `json:"cel,omitempty" yaml:"cel,omitempty"`
	// Value is a literal YAML scalar, object, or list.
	Value any `json:"value,omitempty" yaml:"value,omitempty"`
	// Default is used when From/CEL evaluates to null or empty string.
	Default any `json:"default,omitempty" yaml:"default,omitempty"`
	// Required fails the mapping if the resolved value is still empty.
	Required bool `json:"required,omitempty" yaml:"required,omitempty"`
}

// Value is a name/namespace expression. Exactly one of From, CEL, or Value.
// Default and Required mirror Field so name/namespace can carry a fallback
// and be enforced, rather than only spec.* fields.
type Value struct {
	From     string `json:"from,omitempty" yaml:"from,omitempty"`
	CEL      string `json:"cel,omitempty" yaml:"cel,omitempty"`
	Value    string `json:"value,omitempty" yaml:"value,omitempty"`
	Default  any    `json:"default,omitempty" yaml:"default,omitempty"`
	Required bool   `json:"required,omitempty" yaml:"required,omitempty"`
}

func (v Value) empty() bool {
	return v.From == "" && v.CEL == "" && v.Value == ""
}

func (v Value) asField() Field {
	return Field{From: v.From, CEL: v.CEL, Value: nonEmpty(v.Value), Default: v.Default, Required: v.Required}
}

func nonEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// StatusMapping declares how the status of emitted DPF children is mirrored
// back onto the OPI source's .status. It is the reverse direction of Emit and
// keeps status roll-up as data, not Go. Rules are evaluated with CEL variables
// source (the OPI object) and children (the list of emitted child objects,
// each an unstructured map located by the translation labels).
type StatusMapping struct {
	// Fields write computed values onto the source, addressed by dotted path
	// rooted at the object (e.g. status.observedServices).
	Fields []Field `json:"fields,omitempty" yaml:"fields,omitempty"`
	// Conditions are metav1-style conditions upserted onto status.conditions.
	Conditions []StatusCondition `json:"conditions,omitempty" yaml:"conditions,omitempty"`
}

// StatusCondition is one mirrored condition. Status is a CEL expression that
// must evaluate to bool; Type/Reason/Message are literals.
type StatusCondition struct {
	Type    string `json:"type" yaml:"type"`
	Status  string `json:"status" yaml:"status"`
	Reason  string `json:"reason,omitempty" yaml:"reason,omitempty"`
	Message string `json:"message,omitempty" yaml:"message,omitempty"`
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
	if s.Defaults != nil && !s.Defaults.Namespace.empty() {
		if err := valueSourcesOK("defaults.namespace", s.Defaults.Namespace); err != nil {
			return err
		}
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
		if err := valueSourcesOK(fmt.Sprintf("emit[%d].name", i), e.Name); err != nil {
			return err
		}
		if !e.Namespace.empty() {
			if err := valueSourcesOK(fmt.Sprintf("emit[%d].namespace", i), e.Namespace); err != nil {
				return err
			}
		}
		for j, f := range e.Fields {
			if err := validateField(fmt.Sprintf("emit[%d].fields[%d]", i, j), f); err != nil {
				return err
			}
		}
	}
	if s.Status != nil {
		for i, f := range s.Status.Fields {
			if err := validateField(fmt.Sprintf("status.fields[%d]", i), f); err != nil {
				return err
			}
		}
		for i, c := range s.Status.Conditions {
			if strings.TrimSpace(c.Type) == "" {
				return fmt.Errorf("status.conditions[%d].type is required", i)
			}
			if strings.TrimSpace(c.Status) == "" {
				return fmt.Errorf("status.conditions[%d].status (CEL) is required", i)
			}
		}
	}
	return nil
}

// validateField enforces "to is set" and "exactly one of from/cel/value".
func validateField(where string, f Field) error {
	if f.To == "" {
		return fmt.Errorf("%s.to is required", where)
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
		return fmt.Errorf("%s: exactly one of from, cel, or value must be set", where)
	}
	return nil
}

// valueSourcesOK enforces "exactly one of from/cel/value" for a Value.
func valueSourcesOK(where string, v Value) error {
	n := 0
	if v.From != "" {
		n++
	}
	if v.CEL != "" {
		n++
	}
	if v.Value != "" {
		n++
	}
	if n != 1 {
		return fmt.Errorf("%s: exactly one of from, cel, or value must be set", where)
	}
	return nil
}
