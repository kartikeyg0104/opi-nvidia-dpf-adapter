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

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	labelSourceKind = "translation.opi.nvidia.com/source-kind"
	labelSourceName = "translation.opi.nvidia.com/source-name"
	labelMapping    = "translation.opi.nvidia.com/mapping"
)

// Label keys used to locate emitted children for status roll-up. Exported so
// the controller builds the same selector the engine stamps.
const (
	LabelSourceKind = labelSourceKind
	LabelSourceName = labelSourceName
	LabelMapping    = labelMapping
)

// Apply interprets spec against an OPI source object and returns the DPF
// objects it should emit. source is the unstructured.Object map.
func Apply(spec *Spec, source map[string]any) ([]*unstructured.Unstructured, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	var out []*unstructured.Unstructured
	for i, emit := range spec.Emit {
		objs, err := applyEmit(spec, emit, source)
		if err != nil {
			return nil, fmt.Errorf("emit[%d] %s: %w", i, emit.Target.Kind, err)
		}
		out = append(out, objs...)
	}
	return out, nil
}

func applyEmit(spec *Spec, emit Emit, source map[string]any) ([]*unstructured.Unstructured, error) {
	items := []any{nil}
	if emit.ForEach != nil {
		raw, ok := Get(source, emit.ForEach.In)
		if !ok {
			return nil, nil
		}
		list, ok := asList(raw)
		if !ok {
			return nil, fmt.Errorf("forEach.in %q is not a list", emit.ForEach.In)
		}
		items = list
	}

	var out []*unstructured.Unstructured
	for _, item := range items {
		ok, err := evalBool(emit.When, celVars(source, item, nil))
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		obj, err := buildObject(spec, emit, source, item)
		if err != nil {
			return nil, err
		}
		out = append(out, obj)
	}
	return out, nil
}

func buildObject(spec *Spec, emit Emit, source map[string]any, item any) (*unstructured.Unstructured, error) {
	name, err := resolveValue(emit.Name.asField(), source, item, nil)
	if err != nil {
		return nil, fmt.Errorf("name: %w", err)
	}
	if v, ok := name.(string); !ok || v == "" {
		if emit.Name.Default != nil {
			name = emit.Name.Default
		}
	}
	nameStr, ok := name.(string)
	if !ok || nameStr == "" {
		return nil, fmt.Errorf("name resolved to %v, want non-empty string", name)
	}

	ns, err := resolveNamespace(spec, emit, source, item)
	if err != nil {
		return nil, err
	}

	dest := map[string]any{}
	for _, f := range emit.Fields {
		val, set, err := fieldValue(f, source, item, nil)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", f.To, err)
		}
		if !set {
			continue
		}
		if err := Set(dest, f.To, val); err != nil {
			return nil, err
		}
	}

	u := &unstructured.Unstructured{Object: dest}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   emit.Target.Group,
		Version: emit.Target.Version,
		Kind:    emit.Target.Kind,
	})
	u.SetName(nameStr)
	if ns != "" {
		u.SetNamespace(ns)
	}

	labels := u.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[labelMapping] = spec.Metadata.Name
	labels[labelSourceKind] = spec.Source.Kind
	if srcName, ok := Get(source, "metadata.name"); ok {
		if s, ok := srcName.(string); ok {
			labels[labelSourceName] = s
		}
	}
	u.SetLabels(labels)
	return u, nil
}

// resolveNamespace uses the emit's own namespace, else the spec-level default.
func resolveNamespace(spec *Spec, emit Emit, source map[string]any, item any) (string, error) {
	nsVal := emit.Namespace
	if nsVal.empty() && spec.Defaults != nil && !spec.Defaults.Namespace.empty() {
		nsVal = spec.Defaults.Namespace
	}
	if nsVal.empty() {
		return "", nil
	}
	v, set, err := fieldValue(nsVal.asField(), source, item, nil)
	if err != nil {
		return "", fmt.Errorf("namespace: %w", err)
	}
	if !set {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("namespace resolved to %v, want string", v)
	}
	return s, nil
}

// ApplyStatus rolls the status of emitted children back onto the OPI source.
// It returns the .status subtree to merge (fields plus a conditions list whose
// entries carry no lastTransitionTime; the controller stamps that so unchanged
// conditions keep their original time). Returns nil when the spec declares no
// status mapping.
func ApplyStatus(spec *Spec, source map[string]any, children []any) (map[string]any, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if spec.Status == nil {
		return nil, nil
	}
	if children == nil {
		children = []any{}
	}

	obj := map[string]any{}
	for _, f := range spec.Status.Fields {
		val, set, err := fieldValue(f, source, nil, children)
		if err != nil {
			return nil, fmt.Errorf("status field %s: %w", f.To, err)
		}
		if !set {
			continue
		}
		if err := Set(obj, f.To, val); err != nil {
			return nil, err
		}
	}

	statusMap := map[string]any{}
	if raw, ok := Get(obj, "status"); ok {
		if m, ok := asMap(raw); ok {
			statusMap = m
		}
	}

	var conds []any
	for _, c := range spec.Status.Conditions {
		ok, err := evalBool(c.Status, celVars(source, nil, children))
		if err != nil {
			return nil, fmt.Errorf("status condition %s: %w", c.Type, err)
		}
		status := "False"
		if ok {
			status = "True"
		}
		cond := map[string]any{"type": c.Type, "status": status}
		if c.Reason != "" {
			cond["reason"] = c.Reason
		}
		if c.Message != "" {
			cond["message"] = c.Message
		}
		conds = append(conds, cond)
	}
	if len(conds) > 0 {
		statusMap["conditions"] = conds
	}
	if len(statusMap) == 0 {
		return nil, nil
	}
	return statusMap, nil
}

// fieldValue resolves one field and applies default/required/empty rules.
// set is false when the field should be skipped (empty, no default, not required).
func fieldValue(f Field, source map[string]any, item any, children []any) (any, bool, error) {
	val, err := resolveValue(f, source, item, children)
	if err != nil {
		return nil, false, err
	}
	if isEmpty(val) && f.Default != nil {
		val = f.Default
	}
	if f.Required && isEmpty(val) {
		return nil, false, fmt.Errorf("required value is empty")
	}
	if isEmpty(val) {
		return nil, false, nil
	}
	return val, true, nil
}

func resolveValue(f Field, source map[string]any, item any, children []any) (any, error) {
	switch {
	case f.Value != nil:
		return f.Value, nil
	case f.CEL != "":
		return evalCEL(f.CEL, celVars(source, item, children))
	case f.From != "":
		path := f.From
		root := source
		if len(path) > 5 && path[:5] == "item." {
			m, ok := asMap(item)
			if !ok {
				return nil, fmt.Errorf("from %q: item is not an object", f.From)
			}
			root = m
			path = path[5:]
		}
		v, ok := Get(root, path)
		if !ok {
			return nil, nil
		}
		return v, nil
	default:
		return nil, nil
	}
}

func asList(v any) ([]any, bool) {
	switch t := v.(type) {
	case []any:
		return t, true
	default:
		return nil, false
	}
}
