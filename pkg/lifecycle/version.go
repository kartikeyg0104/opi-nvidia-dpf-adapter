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

package lifecycle

import (
	"fmt"
	"strconv"
	"strings"
)

// Skew classifies an installed vendor-operator version against the window this
// adapter is actually tested against.
//
// Version skew is the failure this exists to make visible. The adapter writes
// the vendor's CRs, so the vendor operator's API is a hard dependency: a DPF
// release that renames a field or bumps a CRD version turns every translation
// into a silent no-op or an apply error, and the only symptom is objects that
// never become ready. Detecting it is the difference between "the adapter says
// DPF 26.4 is newer than it supports" and an afternoon of debugging.
type Skew string

const (
	// SkewSupported means the installed version is inside the tested window.
	SkewSupported Skew = "Supported"
	// SkewTooOld means it predates the window: expect missing fields.
	SkewTooOld Skew = "TooOld"
	// SkewTooNew means it postdates the window: the API may have moved.
	SkewTooNew Skew = "TooNew"
	// SkewUnknown means no version could be determined, or it did not parse.
	// Deliberately distinct from TooOld/TooNew: "we cannot tell" is not the
	// same claim as "it is wrong", and reporting it as a failure would train
	// operators to ignore the condition.
	SkewUnknown Skew = "Unknown"
	// SkewUnpinned means the vendor has no declared support window here.
	SkewUnpinned Skew = "Unpinned"
)

// SupportedRange is a half-open version window: MinInclusive <= v < MaxExclusive.
// Half-open because vendor operators move on minor releases, so the useful
// statement is "tested up to, but not including, the next major".
type SupportedRange struct {
	MinInclusive string
	MaxExclusive string
}

// supportedVersions is the pinned window per vendor. Update it in the same
// change that updates the vendored CRDs under test/ -- the window is a claim
// about what the conformance suite ran against, not an aspiration.
var supportedVersions = map[string]SupportedRange{
	// NVIDIA DPF (doca-platform).
	"nvidia": {MinInclusive: "v25.1.0", MaxExclusive: "v26.0.0"},
	// AMD Pensando has no published operator to pin yet; see
	// docs/multi-vendor.md. Absent from this map on purpose, which classifies
	// as SkewUnpinned rather than pretending a window is known.
}

// SupportedRangeFor reports the pinned window for a vendor, if there is one.
func SupportedRangeFor(vendor string) (SupportedRange, bool) {
	r, ok := supportedVersions[strings.ToLower(strings.TrimSpace(vendor))]
	return r, ok
}

// ClassifySkew places an installed version in or outside the vendor's window.
// An empty or unparseable version is SkewUnknown, never an error: the LCM has
// to report something useful on a cluster it cannot fully inspect.
func ClassifySkew(vendor, installed string) Skew {
	r, ok := SupportedRangeFor(vendor)
	if !ok {
		return SkewUnpinned
	}
	v, err := parseVersion(installed)
	if err != nil {
		return SkewUnknown
	}
	// Both bounds are authored in this file, so a parse failure here is a bug
	// in supportedVersions rather than cluster state -- report Unknown instead
	// of claiming the installed version is at fault.
	min, err := parseVersion(r.MinInclusive)
	if err != nil {
		return SkewUnknown
	}
	max, err := parseVersion(r.MaxExclusive)
	if err != nil {
		return SkewUnknown
	}
	switch {
	case v.compare(min) < 0:
		return SkewTooOld
	case v.compare(max) >= 0:
		return SkewTooNew
	default:
		return SkewSupported
	}
}

// version is major.minor.patch. Build metadata and pre-release suffixes are
// parsed and then ignored for ordering: pinning a vendor operator to a
// pre-release is not a case worth encoding, and treating v25.1.0-rc1 as
// equivalent to v25.1.0 is the behaviour an operator expects.
type version struct{ major, minor, patch int }

func (v version) compare(o version) int {
	for _, p := range [][2]int{{v.major, o.major}, {v.minor, o.minor}, {v.patch, o.patch}} {
		if p[0] != p[1] {
			if p[0] < p[1] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// parseVersion accepts "v25.1.0", "25.1", "25.1.0-rc1" and "25.1.0+sha.abc".
// Missing minor/patch default to 0, so a vendor reporting "v26" compares as
// v26.0.0 rather than failing to parse.
func parseVersion(s string) (version, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return version{}, fmt.Errorf("empty version")
	}
	s = strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	// Drop pre-release and build metadata.
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) > 3 {
		return version{}, fmt.Errorf("version %q has more than three components", s)
	}
	out := version{}
	dst := []*int{&out.major, &out.minor, &out.patch}
	for i, p := range parts {
		if p == "" {
			return version{}, fmt.Errorf("version %q has an empty component", s)
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return version{}, fmt.Errorf("version %q: component %q is not a number", s, p)
		}
		if n < 0 {
			return version{}, fmt.Errorf("version %q: negative component", s)
		}
		*dst[i] = n
	}
	return out, nil
}
