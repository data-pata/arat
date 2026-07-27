package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// MetaFile is the name of the per-workspace marker file arat writes at the
// root of every workspace it creates.
//
// The file does two jobs:
//
//  1. It marks a directory as a workspace. Inside a project, this is what
//     distinguishes a child workspace from a repo worktree and from an
//     unrelated directory the user happened to create.
//  2. It stores the state that the directory name cannot encode — the
//     workspace kind, and a project's Linear project/initiative reference
//     (which, unlike an issue id, is a slug that has no place in a branch
//     name).
//
// A workspace directory without this file is treated as a task workspace, so
// every workspace created before projects existed keeps working untouched.
const MetaFile = ".arat.toml"

// Meta is the on-disk content of MetaFile.
type Meta struct {
	Kind   Kind       `toml:"kind"`
	Linear *LinearRef `toml:"linear,omitempty"`
}

// LinearRef is a workspace's attachment to a Linear project or initiative.
//
// This is deliberately separate from Workspace.Ticket. A task workspace
// attaches to an *issue*, whose identifier (abc-123) is encoded in the
// directory and branch names. A project workspace attaches to a Linear
// project or initiative, which is identified by a slug and carries no issue
// number, so it is stored here instead.
type LinearRef struct {
	// Kind is "project" or "initiative". Linear projects do not nest —
	// only initiatives do (via parentInitiative/subInitiatives) — so arat
	// does not tie its own nesting depth to which of the two a workspace
	// points at. A nested arat project may reference either.
	Kind string `toml:"kind" json:"kind"`
	// ID is the Linear slug id (Project.slugId / Initiative.slugId).
	ID string `toml:"id" json:"id"`
	// Name is the human-readable name, cached so `arat ls` can render it
	// without a network round-trip.
	Name string `toml:"name,omitempty" json:"name,omitempty"`
	// URL is the canonical Linear URL.
	URL string `toml:"url,omitempty" json:"url,omitempty"`
}

// Linear reference kinds.
const (
	LinearKindProject    = "project"
	LinearKindInitiative = "initiative"
)

// ValidLinearKind reports whether k is a supported LinearRef.Kind.
func ValidLinearKind(k string) bool {
	return k == LinearKindProject || k == LinearKindInitiative
}

// readMeta loads MetaFile from a workspace directory.
//
// A missing file is not an error: it yields (nil, nil), which callers treat
// as "task workspace, no Linear reference". A malformed file *is* an error —
// silently downgrading a project to a task would make `arat rm` skip the
// child-workspace safety checks.
func readMeta(dir string) (*Meta, error) {
	data, err := os.ReadFile(filepath.Join(dir, MetaFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", filepath.Join(dir, MetaFile), err)
	}
	var m Meta
	if err := toml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Join(dir, MetaFile), err)
	}
	if m.Kind == "" {
		m.Kind = KindTask
	}
	if m.Kind != KindTask && m.Kind != KindProject {
		return nil, fmt.Errorf("%s: kind %q must be %q or %q", filepath.Join(dir, MetaFile), m.Kind, KindTask, KindProject)
	}
	if m.Linear != nil && !ValidLinearKind(m.Linear.Kind) {
		return nil, fmt.Errorf("%s: linear.kind %q must be %q or %q", filepath.Join(dir, MetaFile), m.Linear.Kind, LinearKindProject, LinearKindInitiative)
	}
	return &m, nil
}

// writeMeta writes MetaFile into a workspace directory, replacing any
// existing file. The write goes through a temp file plus rename: a malformed
// marker is a loud error on every subsequent read (see readMeta), so a crash
// mid-write must not be able to leave a truncated one behind.
func writeMeta(dir string, m Meta) error {
	if m.Kind == "" {
		m.Kind = KindTask
	}
	data, err := toml.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode %s: %w", MetaFile, err)
	}
	header := "# arat workspace marker. Managed by arat; safe to read.\n"

	tmp, err := os.CreateTemp(dir, MetaFile+".tmp-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", MetaFile, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append([]byte(header), data...)); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", MetaFile, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", MetaFile, err)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", MetaFile, err)
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, MetaFile))
}

// hasMeta reports whether dir carries the workspace marker file. Used when
// walking a project's children, where the marker is what separates a child
// workspace from a repo worktree.
func hasMeta(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, MetaFile))
	return err == nil
}
