package warn

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/bazelbuild/buildtools/build"
	"github.com/bazelbuild/buildtools/bzlenv"
	"github.com/bazelbuild/buildtools/edit"
	"github.com/bazelbuild/buildtools/tables"
)

func symbolLoadLocationWarning(f *build.File, fileReader *FileReader) []*LinterFinding {
	var findings []*LinterFinding

	// Phase 1: For each canonical location, walk the file to find usages of
	// restricted symbols (those with exactly one canonical load path) that are
	// not loaded from anywhere. Replacements that can be satisfied by merging
	// into an existing load statement are applied immediately. Replacements that
	// require inserting a brand-new load statement are deferred so that all nil
	// placeholders can be inserted into f.Stmt in a single operation, avoiding
	// pointer invalidation caused by multiple backing-array reallocations.
	locToSymbols := make(map[string][]string)
	for sym, locations := range tables.AllowedSymbolLoadLocations {
		if len(locations) == 1 {
			for loc := range locations {
				locToSymbols[loc] = append(locToSymbols[loc], sym)
			}
		}
	}

	// Process in deterministic order.
	locs := make([]string, 0, len(locToSymbols))
	for loc := range locToSymbols {
		locs = append(locs, loc)
	}
	sort.Strings(locs)

	// pendingEntry holds info about a location that needs a brand-new load stmt.
	type pendingEntry struct {
		resolvedLoc string
		symbols     []string
		findings    []*LinterFinding
	}
	var pending []pendingEntry

	for _, loc := range locs {
		syms := locToSymbols[loc]
		sort.Strings(syms)
		resolvedLoc := useApparentRepoNameIfExternal(loc, fileReader)

		unloaded, locFindings := collectUnloadedSymbols(f, syms, resolvedLoc)
		if len(unloaded) == 0 {
			continue
		}
		findings = append(findings, locFindings...)

		// Try to merge into an existing load statement for this location.
		merged := false
		for i, stmt := range f.Stmt {
			load, ok := stmt.(*build.LoadStmt)
			if !ok || load.Module.Value != resolvedLoc {
				continue
			}
			newLoad := *load
			if !edit.AppendToLoad(&newLoad, unloaded, unloaded) {
				break
			}
			r := LinterReplacement{&f.Stmt[i], &newLoad}
			for _, finding := range locFindings {
				finding.Replacement = append(finding.Replacement, r)
			}
			merged = true
			break
		}
		if !merged {
			pending = append(pending, pendingEntry{resolvedLoc, unloaded, locFindings})
		}
	}

	// Phase 2: Batch-insert nil placeholders for all new load statements at once.
	// This avoids the pointer-invalidation bug that occurs when insertLoad is
	// called multiple times (each call reallocates f.Stmt's backing array).
	if len(pending) > 0 {
		// Find insert position (first non-comment, non-docstring statement).
		insertAt := 0
		for insertAt = range f.Stmt {
			stmt := f.Stmt[insertAt]
			if _, isComment := stmt.(*build.CommentBlock); isComment {
				continue
			}
			if _, isDocString := stmt.(*build.StringExpr); !isDocString {
				break
			}
		}

		// Build a new f.Stmt with all nil placeholders inserted at once.
		newStmts := make([]build.Expr, 0, len(f.Stmt)+len(pending))
		newStmts = append(newStmts, f.Stmt[:insertAt]...)
		for range pending {
			newStmts = append(newStmts, nil)
		}
		newStmts = append(newStmts, f.Stmt[insertAt:]...)
		f.Stmt = newStmts

		// Assign each nil's replacement to the correct finding group. All
		// pointers are into the same backing array, so no invalidation.
		for j, p := range pending {
			r := LinterReplacement{&f.Stmt[insertAt+j], edit.NewLoad(p.resolvedLoc, p.symbols, p.symbols)}
			for _, finding := range p.findings {
				finding.Replacement = append(finding.Replacement, r)
			}
		}
	}

	// Phase 3: Check existing load statements for wrong locations.
	// This runs after phase 1&2 so stmtIndex correctly reflects any nil
	// placeholders that were inserted above.
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

// collectUnloadedSymbols walks the file and returns the subset of globals that
// are called as functions but not loaded (not present in the file's environment).
// It also returns a LinterFinding for each such usage.
// Does NOT modify f.Stmt.
func collectUnloadedSymbols(f *build.File, globals []string, loadFrom string) ([]string, []*LinterFinding) {
	toLoad := make(map[string]bool)
	var findings []*LinterFinding

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
					makeLinterFinding(call.X, fmt.Sprintf("Symbol %q must be loaded from %q.", global, loadFrom)))
				break
			}
		}
	}
	var expr build.Expr = f
	walk(&expr, bzlenv.NewEnvironment())

	if len(toLoad) == 0 {
		return nil, nil
	}

	loads := make([]string, 0, len(toLoad))
	for l := range toLoad {
		loads = append(loads, l)
	}
	sort.Strings(loads)
	return loads, findings
}
