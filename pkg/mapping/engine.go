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
		ok, err := evalBool(emit.When, source, item)
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
	name, err := resolveValue(emit.Name.asField(), source, item)
	if err != nil {
		return nil, fmt.Errorf("name: %w", err)
	}
	nameStr, ok := name.(string)
	if !ok || nameStr == "" {
		return nil, fmt.Errorf("name resolved to %v, want non-empty string", name)
	}

	ns := ""
	if !emit.Namespace.empty() {
		v, err := resolveValue(emit.Namespace.asField(), source, item)
		if err != nil {
			return nil, fmt.Errorf("namespace: %w", err)
		}
		if s, ok := v.(string); ok {
			ns = s
		}
	}

	dest := map[string]any{}
	for _, f := range emit.Fields {
		val, err := resolveValue(f, source, item)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", f.To, err)
		}
		if isEmpty(val) && f.Default != nil {
			val = f.Default
		}
		if f.Required && isEmpty(val) {
			return nil, fmt.Errorf("field %s: required value is empty", f.To)
		}
		if isEmpty(val) {
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

func resolveValue(f Field, source map[string]any, item any) (any, error) {
	switch {
	case f.Value != nil:
		return f.Value, nil
	case f.CEL != "":
		return evalCEL(f.CEL, source, item)
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
