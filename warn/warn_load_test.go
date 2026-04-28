/*
Copyright 2020 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package warn

import (
	"testing"

	"github.com/bazelbuild/buildtools/tables"
)

func TestWarnLoadLocation(t *testing.T) {
	tables.AllowedSymbolLoadLocations["s1"] = map[string]bool{":z.bzl": true}
	tables.AllowedSymbolLoadLocations["s3"] = map[string]bool{":x.bzl": true, ":y.bzl": true}
	tables.AllowedSymbolLoadLocations["s4"] = map[string]bool{":a.bzl": true}
	checkFindingsAndFix(t, "allowed-symbol-load-locations", `
load(":f.bzl", "s1", "s2")
load(":a.bzl", "s3")
load(":a.bzl", "s4")
`, `
load(":f.bzl", "s1", "s2")
load(":a.bzl", "s3")
load(":a.bzl", "s4")
`,
		[]string{
			":1: Symbol \"s1\" must be loaded from :z.bzl.",
			":2: Symbol \"s3\" must be loaded from one of the allowed locations: :x.bzl, :y.bzl.",
		},
		scopeEverywhere)
}

// TestWarnLoadLocationFix verifies that when a load statement contains only symbols
// that all require the same single canonical location, the fix rewrites the module path.
func TestWarnLoadLocationFix(t *testing.T) {
	tables.AllowedSymbolLoadLocations["sym1"] = map[string]bool{":canonical.bzl": true}
	tables.AllowedSymbolLoadLocations["sym2"] = map[string]bool{":canonical.bzl": true}

	// Single symbol in load requiring a single location: fix changes module.
	checkFindingsAndFix(t, "allowed-symbol-load-locations", `
load(":wrong.bzl", "sym1")
`, `
load(":canonical.bzl", "sym1")
`,
		[]string{
			`:1: Symbol "sym1" must be loaded from :canonical.bzl.`,
		},
		scopeEverywhere)

	// Multiple symbols in one load all requiring the same location: fix changes module.
	checkFindingsAndFix(t, "allowed-symbol-load-locations", `
load(":wrong.bzl", "sym1", "sym2")
`, `
load(":canonical.bzl", "sym1", "sym2")
`,
		[]string{
			`:1: Symbol "sym1" must be loaded from :canonical.bzl.`,
			`:1: Symbol "sym2" must be loaded from :canonical.bzl.`,
		},
		scopeEverywhere)

	// Mixed load (one restricted symbol + one unrestricted): no fix, only warn.
	checkFindingsAndFix(t, "allowed-symbol-load-locations", `
load(":wrong.bzl", "sym1", "unrestricted")
`, `
load(":wrong.bzl", "sym1", "unrestricted")
`,
		[]string{
			`:1: Symbol "sym1" must be loaded from :canonical.bzl.`,
		},
		scopeEverywhere)
}

// TestWarnLoadLocationCcRules verifies that the built-in AllowedSymbolLoadLocations
// entries for rules_cc symbols cause per-rule loads to be rewritten to defs.bzl.
func TestWarnLoadLocationCcRules(t *testing.T) {
	checkFindingsAndFix(t, "allowed-symbol-load-locations", `
load("@rules_cc//cc:cc_binary.bzl", "cc_binary")
load("@rules_cc//cc:cc_library.bzl", "cc_library")
load("@rules_cc//cc:cc_test.bzl", "cc_test")
`, `
load("@rules_cc//cc:defs.bzl", "cc_binary")
load("@rules_cc//cc:defs.bzl", "cc_library")
load("@rules_cc//cc:defs.bzl", "cc_test")
`,
		[]string{
			`:1: Symbol "cc_binary" must be loaded from @rules_cc//cc:defs.bzl.`,
			`:2: Symbol "cc_library" must be loaded from @rules_cc//cc:defs.bzl.`,
			`:3: Symbol "cc_test" must be loaded from @rules_cc//cc:defs.bzl.`,
		},
		scopeEverywhere)

	// A load already using defs.bzl should generate no warning and no fix.
	checkFindingsAndFix(t, "allowed-symbol-load-locations", `
load("@rules_cc//cc:defs.bzl", "cc_binary", "cc_library")
`, `
load("@rules_cc//cc:defs.bzl", "cc_binary", "cc_library")
`,
		[]string{},
		scopeEverywhere)
}
