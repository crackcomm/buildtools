package warn

import (
	"fmt"
	"slices"
	"strings"

	"github.com/bazelbuild/buildtools/build"
	"github.com/bazelbuild/buildtools/tables"
)

func symbolLoadLocationWarning(f *build.File) []*LinterFinding {
	var findings []*LinterFinding

	for stmtIndex := 0; stmtIndex < len(f.Stmt); stmtIndex++ {
		load, ok := f.Stmt[stmtIndex].(*build.LoadStmt)
		if !ok {
			continue
		}

		// Determine whether all symbols in this load statement should move to the
		// same single canonical location. A fix (changing the module path in place)
		// is safe only when every symbol has exactly one allowed location and that
		// location is the same for all of them.
		canonicalLoc := ""
		canFix := true
		for _, from := range load.From {
			expected, hasRestriction := tables.AllowedSymbolLoadLocations[from.Name]
			if !hasRestriction {
				// No restriction: symbol is allowed anywhere; changing the module
				// could break it if it isn't exported from the new location.
				canFix = false
				break
			}
			if expected[load.Module.Value] {
				// Symbol is already OK at the current location; no violation here,
				// but we can't change the module without potentially breaking it.
				canFix = false
				break
			}
			if len(expected) != 1 {
				// Multiple allowed locations: no single canonical destination.
				canFix = false
				break
			}
			var loc string
			for l := range expected {
				loc = l
			}
			if canonicalLoc == "" {
				canonicalLoc = loc
			} else if canonicalLoc != loc {
				// Symbols require different destinations; can't fix with one module change.
				canFix = false
				break
			}
		}
		if canonicalLoc == "" {
			canFix = false
		}

		for i := 0; i < len(load.From); i++ {
			from := load.From[i]

			expected, ok := tables.AllowedSymbolLoadLocations[from.Name]
			if !ok || expected[load.Module.Value] {
				continue
			}

			var finding *LinterFinding
			if len(expected) == 1 {
				var loc string
				for l := range expected {
					loc = l
					break
				}
				finding = makeLinterFinding(from, fmt.Sprintf("Symbol %q must be loaded from %s.", from.Name, loc))
				if canFix {
					newModule := *load.Module
					newModule.Value = loc
					newLoad := *load
					newLoad.Module = &newModule
					finding.Replacement = []LinterReplacement{{Old: &f.Stmt[stmtIndex], New: &newLoad}}
				}
			} else {
				locs := make([]string, 0, len(expected))
				for l := range expected {
					locs = append(locs, l)
				}
				slices.Sort(locs)
				finding = makeLinterFinding(from, fmt.Sprintf("Symbol %q must be loaded from one of the allowed locations: %s.", from.Name, strings.Join(locs, ", ")))
			}
			findings = append(findings, finding)
		}

	}
	return findings
}
