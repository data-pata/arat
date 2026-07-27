package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// EncodeCwdAsProjectDir mirrors Claude Code's ~/.claude/projects/<dir>
// scheme: starting from an absolute cwd, every '/' and '.' becomes '-'.
// So `/home/u/git/myorg/feat/foo` → `-home-u-git-myorg-feat-foo` and
// `/home/u/.claude` → `-home-u--claude`.
func EncodeCwdAsProjectDir(cwd string) string {
	r := strings.NewReplacer("/", "-", ".", "-")
	return r.Replace(cwd)
}

// SessionMoveWarning describes a per-dir/file failure during session
// migration. Migration is best-effort: warnings flow back to the caller so
// the user can fix things by hand, but they never abort the workspace
// operation that triggered the move.
type SessionMoveWarning struct {
	Dir    string // source project-dir name (under ClaudeProjectsDir)
	File   string // jsonl filename when the failure is per-file; empty for per-dir
	Reason string
}

// MoveSessionsForRename moves any Claude Code session-history dirs that
// belong to oldPath (the workspace itself or any subdir of it) to mirror a
// rename from oldPath to newPath. The workspace dir on disk has typically
// already been renamed by the caller; this only touches
// ~/.claude/projects/<encoded>.
//
// Best-effort. Returns warnings for individual files/dirs that couldn't be
// moved (e.g. a session with the same id already exists at the destination)
// but doesn't return an error for "nothing to move".
//
// Disambiguation: when there are sibling workspaces under WorkspacesDir
// whose encoded prefix overlaps with oldPath's (e.g. workspaces `foo` and
// `foo-extra`), candidates that belong to those siblings are skipped.
func (s *Service) MoveSessionsForRename(oldPath, newPath string) []SessionMoveWarning {
	if s.ClaudeProjectsDir == "" {
		return nil
	}
	oldPrefix := EncodeCwdAsProjectDir(oldPath)
	newPrefix := EncodeCwdAsProjectDir(newPath)
	if oldPrefix == newPrefix {
		return nil
	}

	entries, err := os.ReadDir(s.ClaudeProjectsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return []SessionMoveWarning{{Reason: fmt.Sprintf("read %s: %v", s.ClaudeProjectsDir, err)}}
	}

	siblingPrefixes := s.siblingEncodedPrefixes(oldPath, newPath)

	var warnings []SessionMoveWarning
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if name != oldPrefix && !strings.HasPrefix(name, oldPrefix+"-") {
			continue
		}
		if matchesAny(name, siblingPrefixes) {
			continue
		}
		newName := newPrefix + name[len(oldPrefix):]
		if w := moveProjectDir(s.ClaudeProjectsDir, name, newName); len(w) > 0 {
			warnings = append(warnings, w...)
		}
	}
	return warnings
}

// MoveSessionFile relocates a single session jsonl (identified by sessionID,
// which is its filename minus the .jsonl extension) into the project dir
// matching targetWorkspacePath. Used by `arat new --carry-session` to drag a
// chat that started in some other cwd (e.g. ~/git/myorg) into a freshly
// created workspace.
//
// Returns (sourcePath, destPath, nil) on success.
// Returns ErrNotFound if the session isn't found anywhere under
// ClaudeProjectsDir, ErrAlreadyExists if the destination already exists.
func (s *Service) MoveSessionFile(ctx context.Context, sessionID, targetWorkspacePath string) (srcPath, dstPath string, err error) {
	if s.ClaudeProjectsDir == "" {
		return "", "", fmt.Errorf("%w: ClaudeProjectsDir not configured", ErrInvalidInput)
	}
	if sessionID == "" {
		return "", "", fmt.Errorf("%w: session id is required", ErrInvalidInput)
	}
	fileName := sessionID + ".jsonl"

	src, err := findSessionFile(s.ClaudeProjectsDir, fileName)
	if err != nil {
		return "", "", err
	}

	targetDir := filepath.Join(s.ClaudeProjectsDir, EncodeCwdAsProjectDir(targetWorkspacePath))
	dst := filepath.Join(targetDir, fileName)
	if src == dst {
		return src, dst, nil
	}

	if _, statErr := os.Stat(dst); statErr == nil {
		return src, dst, fmt.Errorf("%w: %s", ErrAlreadyExists, dst)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return src, dst, fmt.Errorf("stat %s: %w", dst, statErr)
	}

	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return src, dst, fmt.Errorf("mkdir %s: %w", targetDir, err)
	}
	if err := moveFile(src, dst); err != nil {
		return src, dst, err
	}
	// Tidy: if the source dir is now empty, drop it. Ignore failures.
	_ = os.Remove(filepath.Dir(src))
	return src, dst, nil
}

// findSessionFile searches one level under root for a dir containing
// fileName. Returns ErrNotFound if no match. If more than one match exists
// (shouldn't happen for UUID-derived session ids), returns the first by
// alphabetical dir order.
func findSessionFile(root, fileName string) (string, error) {
	dirs, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%w: session %s", ErrNotFound, fileName)
		}
		return "", fmt.Errorf("read %s: %w", root, err)
	}
	names := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if d.IsDir() {
			names = append(names, d.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names {
		candidate := filepath.Join(root, n, fileName)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%w: session %s", ErrNotFound, fileName)
}

// siblingEncodedPrefixes returns the encoded prefixes of every directory
// alongside the one being renamed, except for the two endpoints of the rename.
// Used to avoid mis-matching a sibling workspace whose name shares a prefix
// with the one being renamed (e.g. `foo` vs `foo-extra`).
//
// Siblings are read from the renamed workspace's own parent directory, not
// from WorkspacesDir. A rename is always in place, so both endpoints share
// that parent. Scoping to WorkspacesDir instead would, for a workspace nested
// inside a project, list the *project* as a sibling — and since the project's
// encoded path is a prefix of its children's, every session dir belonging to
// the workspace being renamed would be mistaken for the project's and skipped.
// For a top-level workspace the parent is WorkspacesDir, so this is the same
// set as before.
func (s *Service) siblingEncodedPrefixes(oldPath, newPath string) []string {
	parent := filepath.Dir(oldPath)
	if parent == "" || parent == "." {
		return nil
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		return nil
	}
	oldName := filepath.Base(oldPath)
	newName := filepath.Base(newPath)
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if name == oldName || name == newName {
			continue
		}
		out = append(out, EncodeCwdAsProjectDir(filepath.Join(parent, name)))
	}
	return out
}

func matchesAny(name string, prefixes []string) bool {
	for _, p := range prefixes {
		if name == p || strings.HasPrefix(name, p+"-") {
			return true
		}
	}
	return false
}

// moveProjectDir relocates a single project dir from <root>/<src> to
// <root>/<dst>. If <dst> doesn't exist, this is a single rename. If it does
// (because the user has already started a fresh session in the new path),
// entries are moved file-by-file and per-file collisions become warnings.
func moveProjectDir(root, src, dst string) []SessionMoveWarning {
	srcPath := filepath.Join(root, src)
	dstPath := filepath.Join(root, dst)
	if srcPath == dstPath {
		return nil
	}

	if _, err := os.Stat(dstPath); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(srcPath, dstPath); err != nil {
			return []SessionMoveWarning{{Dir: src, Reason: "rename: " + err.Error()}}
		}
		return nil
	}

	entries, err := os.ReadDir(srcPath)
	if err != nil {
		return []SessionMoveWarning{{Dir: src, Reason: "read: " + err.Error()}}
	}
	var warnings []SessionMoveWarning
	for _, e := range entries {
		from := filepath.Join(srcPath, e.Name())
		to := filepath.Join(dstPath, e.Name())
		if _, err := os.Stat(to); err == nil {
			warnings = append(warnings, SessionMoveWarning{
				Dir: src, File: e.Name(),
				Reason: "destination already has a file with the same name",
			})
			continue
		}
		if err := os.Rename(from, to); err != nil {
			warnings = append(warnings, SessionMoveWarning{
				Dir: src, File: e.Name(),
				Reason: "rename: " + err.Error(),
			})
		}
	}
	_ = os.Remove(srcPath)
	return warnings
}

// moveFile relocates src to dst. Tries os.Rename first, then falls back to
// copy+remove so we keep working when the destination is on a different
// filesystem (EXDEV). Session jsonls are tiny so the fallback is cheap.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(dst)
		return fmt.Errorf("copy %s → %s: %w", src, dst, err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("close %s: %w", dst, err)
	}
	if err := os.Remove(src); err != nil {
		return fmt.Errorf("remove %s: %w", src, err)
	}
	return nil
}
