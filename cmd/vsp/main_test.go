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

import "testing"

// The daemonset sets DPU_BFB_URL and the README documents --bfb-url. Both
// resolve through the deprecated alias, so losing this fallback would stop
// the firmware URL being stamped without any error.
func TestFirstNonEmptyKeepsDeprecatedAliasWorking(t *testing.T) {
	for _, tc := range []struct {
		name                string
		preferred, fallback string
		want                string
	}{
		{"new name only", "https://new", "", "https://new"},
		{"deprecated alias only", "", "https://old", "https://old"},
		{"new name wins", "https://new", "https://old", "https://new"},
		{"neither set", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstNonEmpty(tc.preferred, tc.fallback); got != tc.want {
				t.Errorf("firstNonEmpty(%q, %q) = %q, want %q",
					tc.preferred, tc.fallback, got, tc.want)
			}
		})
	}
}
