/*
Copyright 2026 Kartikey Gupta.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing limitations under the License.
*/

package mapping

import "strings"

// SchemaHasPath reports whether a dotted OpenAPI path exists on a CRD
// openAPIV3Schema. Slice steps use a [] suffix (spec.networkFunctions[].chart).
func SchemaHasPath(root map[string]any, dotted string) bool {
	// Kubernetes supplies these ObjectMeta maps even when controller-gen
	// emits only metadata: {type: object}. Keep other paths schema-checked.
	if dotted == "metadata.labels" || dotted == "metadata.annotations" {
		return SchemaHasPath(root, "metadata")
	}
	parts := strings.Split(dotted, ".")
	node := root
	for _, part := range parts {
		if part == "" {
			continue
		}
		isSlice := strings.HasSuffix(part, "[]")
		name := strings.TrimSuffix(part, "[]")
		props, _ := node["properties"].(map[string]any)
		if props == nil {
			return false
		}
		child, ok := props[name].(map[string]any)
		if !ok {
			return false
		}
		if isSlice {
			items, _ := child["items"].(map[string]any)
			if items == nil {
				return false
			}
			node = items
			continue
		}
		node = child
	}
	return true
}
