package warn

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/bazelbuild/buildtools/build"
	"github.com/bazelbuild/buildtools/bzlenv"
	"github.com/bazelbuild/buildtools/tables"
)

func symbolLoadLocationWarning(f *build.File, fileReader *FileReader) []*LinterFinding {
	var findings []*LinterFinding

	// First, check for usages of restricted symbols (with exactly one canonical
	// location) that aren't loaded from anywhere, and offer to insert the load.
	// This runs before the wrong-load check so that nil placeholders are inserted
	// into f.Stmt before any pointers into f.Stmt are captured below.
	locToSymbols := make(map[string][]string)
	for sym, locations := range tables.AllowedSymbolLoadLocations {
		if len(locations) == 1 {
			for loc := range locations {
				locToSymbols[loc] = append(locToSymbols[loc], sym)
			}
		}
	}
	// Process in deterministic order so multiple insertions are predictable.
	locs := make([]string, 0, len(locToSymbols))
	for loc := range locToSymbols {
		locs = append(locs, loc)
	}
	sort.Strings(locs)
	for _, loc := range locs {
		syms := locToSymbols[loc]
		sort.Strings(syms)
		findings = append(findings, unloadedRestrictedSymbolCheck(f, fileReader, syms, loc)...)
	}

	// Check existing load statements for wrong locations.
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

// unloadedRestrictedSymbolCheck finds usages of restricted symbols (with a single
// canonical load path) that are not loaded from anywhere, and proposes a fix to
// insert the correct load statement.
func unloadedRestrictedSymbolCheck(f *build.File, fileReader *FileReader, globals []string, loadFrom string) []*LinterFinding {
	toLoad := make(map[string]bool)
	var findings []*LinterFinding

	resolvedLoadFrom := useApparentRepoNameIfExternal(loadFrom, fileReader)

	var walk func(expr *build.Expr, env *bzlenv.Environment)
	walk = func(expr *build.Expr, env *bzlenv.Environment) {
		defer bzlenv.WalkOnceWithEnvironment(*expr, env, walk)

		call, ok := (*expr).(*build.CallExpr)
		if !ok {
			return
		}
		ident, ok := call.X.(*build.Ident)
		if !ok {
			return
		}
		if env.Get(ident.Name) != nil {
			return // already loaded or bound in scope
		}
		for _, global := range globals {
			if ident.Name == global {
				toLoad[global] = true
				findings = append(findings,
					makeLinterFinding(call.X, fmt.Sprintf("Symbol %q must be loaded from %q.", global, resolvedLoadFrom)))
				break
			}
		}
	}
	var expr build.Expr = f
	walk(&expr, bzlenv.NewEnvironment())

	if len(toLoad) == 0 {
		return nil
	}

	loads := make([]string, 0, len(toLoad))
	for l := range toLoad {
		loads = append(loads, l)
	}
	sort.Strings(loads)
	replacement := insertLoad(f, resolvedLoadFrom, loads)
	if replacement != nil {
		for _, finding := range findings {
			finding.Replacement = append(finding.Replacement, *replacement)
		}
	}
	return findings
}
