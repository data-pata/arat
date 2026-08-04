package workspace

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
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
			// The dir also belongs to another workspace's encoded path.
			// When it is the renamed workspace's own root dir, sessions
			// started there are being left behind — say so, since the
			// encoding is ambiguous and arat cannot split the dir.
			if name == oldPrefix {
				warnings = append(warnings, SessionMoveWarning{
					Dir:    name,
					Reason: "another workspace's path encodes to the same session dir (Claude's cwd encoding is ambiguous); left in place — move individual sessions with `arat new --carry-session <id>` if needed",
				})
			}
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
// ctx is unused — the work is pure os calls — but kept so the method sits in
// the same shape as every other Service operation on the cmd interface.
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

// ForkSessionFile copies a session jsonl (identified by sessionID) into the
// project dir matching targetWorkspacePath under a freshly generated session
// id, rewriting every embedded occurrence of the old id to the new one. That
// is the transformation `claude --fork-session` applies to the lines it
// copies, and a transcript resumed under the new id picks up the full
// conversation. The source file is read, never written or removed, so forking
// a session that is still running is safe: the original keeps its id and its
// place, the copy continues independently inside the workspace. Used by
// `arat new --fork-session`.
//
// Returns (sourcePath, destPath, newSessionID, nil) on success.
// Returns ErrNotFound if the session isn't found anywhere under
// ClaudeProjectsDir.
// ctx is unused (the work is pure os calls) but kept so the method sits in
// the same shape as every other Service operation on the cmd interface.
func (s *Service) ForkSessionFile(ctx context.Context, sessionID, targetWorkspacePath string) (srcPath, dstPath, newID string, err error) {
	if s.ClaudeProjectsDir == "" {
		return "", "", "", fmt.Errorf("%w: ClaudeProjectsDir not configured", ErrInvalidInput)
	}
	if sessionID == "" {
		return "", "", "", fmt.Errorf("%w: session id is required", ErrInvalidInput)
	}

	src, err := findSessionFile(s.ClaudeProjectsDir, sessionID+".jsonl")
	if err != nil {
		return "", "", "", err
	}
	newID, err = newSessionID()
	if err != nil {
		return src, "", "", err
	}

	targetDir := filepath.Join(s.ClaudeProjectsDir, EncodeCwdAsProjectDir(targetWorkspacePath))
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return src, "", "", fmt.Errorf("mkdir %s: %w", targetDir, err)
	}
	dst := filepath.Join(targetDir, newID+".jsonl")
	if err := copyWithIDRewrite(src, dst, sessionID, newID); err != nil {
		return src, dst, "", err
	}
	return src, dst, newID, nil
}

// newSessionID returns a random UUIDv4, the format Claude Code uses for
// session ids.
func newSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// copyWithIDRewrite writes src to dst with every occurrence of oldID replaced
// by newID. Line by line: transcripts can be large, and ids never span lines.
func copyWithIDRewrite(src, dst, oldID, newID string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	fail := func(step string, cause error) error {
		out.Close()
		_ = os.Remove(dst)
		return fmt.Errorf("%s: %w", step, cause)
	}
	w := bufio.NewWriter(out)
	r := bufio.NewReader(in)
	oldB, newB := []byte(oldID), []byte(newID)
	for {
		line, readErr := r.ReadBytes('\n')
		if len(line) > 0 {
			if _, err := w.Write(bytes.ReplaceAll(line, oldB, newB)); err != nil {
				return fail("write "+dst, err)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return fail("read "+src, readErr)
		}
	}
	if err := w.Flush(); err != nil {
		return fail("write "+dst, err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("close %s: %w", dst, err)
	}
	return nil
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

// siblingEncodedPrefixes returns the encoded prefixes of directories whose
// session dirs must NOT be swept up by the rename: everything in the tree
// except the renamed workspace itself (its children move with it — they are
// subdirectories, so the rename changed their cwd too).
//
// Two sources feed the list.
//
// Same-parent siblings, read from the renamed workspace's own parent
// directory (not WorkspacesDir: for a nested workspace that would list the
// containing project, whose encoded path is a prefix of the children's, and
// everything would be skipped). This guards the name-prefix overlap case,
// `foo` vs `foo-extra`.
//
// The rest of the workspace tree, because Claude's cwd encoding collapses
// both "/" and "." to "-", so a nested path and a hyphenated name collide:
// <ws>/p/foo and <ws>/p-foo encode identically. A workspace anywhere in the
// tree whose encoded path equals or extends the renamed one's would have its
// session dirs hijacked by the move. arat cannot split a genuinely shared
// encoded dir (the encoding is lossy), but it can refuse to take it.
func (s *Service) siblingEncodedPrefixes(oldPath, newPath string) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(p string) {
		enc := EncodeCwdAsProjectDir(p)
		if _, ok := seen[enc]; !ok {
			seen[enc] = struct{}{}
			out = append(out, enc)
		}
	}

	oldName := filepath.Base(oldPath)
	newName := filepath.Base(newPath)
	if parent := filepath.Dir(oldPath); parent != "" && parent != "." {
		if entries, err := os.ReadDir(parent); err == nil {
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				if e.Name() == oldName || e.Name() == newName {
					continue
				}
				add(filepath.Join(parent, e.Name()))
			}
		}
	}

	for _, dir := range s.workspaceDirs() {
		// The endpoints and everything below them move — that is the point
		// of the rename. Ancestors must be excluded too: an ancestor's
		// encoded path is a prefix of the renamed workspace's, so listing
		// it would classify every candidate as the ancestor's and skip the
		// whole migration.
		if dir == oldPath || dir == newPath ||
			isUnderDir(dir, oldPath) || isUnderDir(dir, newPath) ||
			isUnderDir(oldPath, dir) || isUnderDir(newPath, dir) {
			continue
		}
		add(dir)
	}
	return out
}

// workspaceDirs lists every workspace directory in the tree by walking the
// marker files — no git calls. Used only for encoding-collision exclusion,
// so best-effort: an unreadable directory contributes nothing.
func (s *Service) workspaceDirs() []string {
	if s.WorkspacesDir == "" {
		return nil
	}
	var out []string
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth >= maxDepth {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || e.Name() == claudeWorkspaceDir {
				continue
			}
			sub := filepath.Join(dir, e.Name())
			// Top-level dirs are workspaces by definition (legacy layout);
			// deeper ones only when they carry the marker.
			if depth == 0 || hasMeta(sub) {
				out = append(out, sub)
				walk(sub, depth+1)
			}
		}
	}
	walk(s.WorkspacesDir, 0)
	return out
}

// isUnderDir reports whether p is strictly inside dir.
func isUnderDir(p, dir string) bool {
	return strings.HasPrefix(p, dir+string(os.PathSeparator))
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
