package workspace

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// maxDepth caps how deep the workspace tree is walked. Nesting this deep is
// not a use case anyone has; the cap exists so a symlink loop or a
// hand-created directory cycle under workspaces_dir cannot hang `arat ls`.
const maxDepth = 8

// ErrAmbiguous means a bare workspace name matched more than one workspace in
// the tree, and the caller must disambiguate with a full ref.
type ErrAmbiguous struct {
	Query   string
	Matches []string
}

func (e *ErrAmbiguous) Error() string {
	msg := fmt.Sprintf("ambiguous workspace %q, matches %d: %s\nuse the full ref to disambiguate",
		e.Query, len(e.Matches), strings.Join(e.Matches, ", "))
	// A top-level workspace's full ref is the ambiguous name itself, so
	// "use the full ref" alone would send the user in a circle. The
	// anchored ./ form pins it.
	for _, m := range e.Matches {
		if !strings.Contains(m, "/") {
			msg += fmt.Sprintf(" (./%s for the top-level one)", m)
			break
		}
	}
	return msg
}

// JoinRef composes a child ref from a parent ref and a directory name. A
// child of the top level (empty parent) has the bare name as its ref.
func JoinRef(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "/" + name
}

// ParentRef returns the ref of the project containing ref, or "" when ref is
// at the top level.
func ParentRef(ref string) string {
	i := strings.LastIndex(ref, "/")
	if i < 0 {
		return ""
	}
	return ref[:i]
}

// Flatten returns every workspace in the tree depth-first, each parent
// immediately before its children, with Children left intact on each entry.
//
// Commands that address a single workspace (go, rm, note) work off the
// flattened list so a nested workspace is reachable without the caller having
// to walk the tree itself.
func Flatten(items []Workspace) []Workspace {
	var out []Workspace
	var walk func([]Workspace)
	walk = func(ws []Workspace) {
		for _, w := range ws {
			out = append(out, w)
			walk(w.Children)
		}
	}
	walk(items)
	return out
}

// Resolve finds one workspace in a tree by query, which is either:
//
//   - a full ref ("q3-billing/dunning/abc-20--retry"), matched exactly, or
//   - a bare directory name ("abc-20--retry"), matched against every
//     workspace in the tree.
//
// Accepting the bare name keeps `arat go abc-20--retry` working regardless of
// how deeply the workspace is nested, which is the common case. When a bare
// name is not unique, Resolve returns ErrAmbiguous listing the full refs
// rather than guessing.
//
// A top-level workspace's ref IS its bare name, so a slash-free query cannot
// say which of the two it means. It therefore always takes the bare-name
// route and participates in the ambiguity scan: silently preferring the
// top-level workspace would shadow same-named nested ones, and `arat rm`
// would delete whichever won. Slash-containing queries are unambiguous (a
// name never contains a slash) and match exactly.
//
// The anchored form "./<ref>" matches the ref exactly and skips the
// bare-name route. It exists for the shadowed top-level workspace, whose ref
// no other query can reach.
//
// Returns ErrNotFound when nothing matches.
func Resolve(items []Workspace, query string) (*Workspace, error) {
	anchored := strings.HasPrefix(strings.TrimSpace(query), "./")
	query = strings.Trim(path.Clean(query), "/")
	if query == "" || query == "." {
		return nil, fmt.Errorf("%w: empty workspace name", ErrNotFound)
	}

	all := Flatten(items)

	if anchored {
		for i := range all {
			if all[i].Ref == query {
				return &all[i], nil
			}
		}
		return nil, fmt.Errorf("%w: %s", ErrNotFound, query)
	}

	if strings.Contains(query, "/") {
		for i := range all {
			if all[i].Ref == query {
				return &all[i], nil
			}
		}
		// A failed full ref is often a right leaf under a misremembered
		// chain ("q3/abc-18--fonts" for "q3/abc-12--pdf/abc-18--fonts").
		// If the last segment names real workspaces, say where they are.
		last := query[strings.LastIndex(query, "/")+1:]
		var hints []string
		for i := range all {
			if all[i].Name == last {
				hints = append(hints, all[i].Ref)
			}
		}
		if len(hints) > 0 {
			sort.Strings(hints)
			return nil, fmt.Errorf("%w: %s — did you mean %s?", ErrNotFound, query, strings.Join(hints, " or "))
		}
		return nil, fmt.Errorf("%w: %s", ErrNotFound, query)
	}

	var matches []int
	for i := range all {
		if all[i].Name == query {
			matches = append(matches, i)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("%w: %s", ErrNotFound, query)
	case 1:
		return &all[matches[0]], nil
	default:
		refs := make([]string, 0, len(matches))
		for _, i := range matches {
			refs = append(refs, all[i].Ref)
		}
		sort.Strings(refs)
		return nil, &ErrAmbiguous{Query: query, Matches: refs}
	}
}

// Descendants returns every workspace below ws, depth-first, excluding ws.
func Descendants(ws Workspace) []Workspace {
	return Flatten(ws.Children)
}
