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
)

// Get walks a dotted JSONPath (subset) such as spec.dpuNodeName.
// Map keys that contain dots are not supported; use CEL for those.
func Get(obj map[string]any, path string) (any, bool) {
	path = strings.TrimPrefix(strings.TrimSpace(path), "$.")
	path = strings.Trim(path, "{}")
	path = strings.TrimPrefix(path, ".")
	if path == "" {
		return obj, true
	}
	var cur any = obj
	for _, p := range strings.Split(path, ".") {
		m, ok := asMap(cur)
		if !ok {
			return nil, false
		}
		next, ok := m[p]
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

// Set writes val at a dotted JSONPath, creating intermediate objects.
func Set(obj map[string]any, path string, val any) error {
	path = strings.TrimPrefix(strings.TrimSpace(path), "$.")
	path = strings.Trim(path, "{}")
	path = strings.TrimPrefix(path, ".")
	if path == "" {
		return fmt.Errorf("empty destination path")
	}
	parts := strings.Split(path, ".")
	cur := obj
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p]
		if !ok {
			child := map[string]any{}
			cur[p] = child
			cur = child
			continue
		}
		m, ok := asMap(next)
		if !ok {
			return fmt.Errorf("path %q: %q is not an object", path, p)
		}
		cur = m
	}
	cur[parts[len(parts)-1]] = val
	return nil
}

func asMap(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

func isEmpty(v any) bool {
	if v == nil {
		return true
	}
	s, ok := v.(string)
	return ok && s == ""
}
