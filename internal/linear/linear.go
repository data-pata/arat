// Package linear is a thin shell-out wrapper around the `linear` CLI binary.
//
// We don't bind the Linear API/SDK directly — keeping the surface area to the
// `linear` CLI lets the user's existing `linear-cli` install + auth do all
// the work, and matches the conventions encoded in the team's linear-cli
// skill (default state, project-flag traps, per-subcommand flag differences).
package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// Runner runs an external command and returns stdout, stderr, and any error.
// Tests inject a fake; production uses execRunner.
type Runner func(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)

// Linear is the wrapper.
type Linear struct{ run Runner }

// New returns a Linear that shells out to the real `linear` binary on PATH.
func New() *Linear { return &Linear{run: execRunner} }

// NewWithRunner returns a Linear using the given Runner. Used by tests.
func NewWithRunner(r Runner) *Linear { return &Linear{run: r} }

func execRunner(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// Available reports whether the `linear` binary is on PATH and runs.
// Returns nil when available; otherwise the underlying error (e.g.
// exec.ErrNotFound) so callers can diagnose a missing binary versus a
// failing one.
func (l *Linear) Available(ctx context.Context) error {
	if _, _, err := l.run(ctx, "linear", "--version"); err != nil {
		return err
	}
	return nil
}

// Reader is the read-only Linear surface the interactive ticket flow consumes.
// Declared in the producer package so multiple consumers (cmd, tui) refer to
// one type rather than duplicate it.
type Reader interface {
	IssueList(ctx context.Context, opts IssueListOptions) ([]Issue, error)
}

// IssueCreateOptions controls IssueCreate.
type IssueCreateOptions struct {
	Title       string   // required
	Description string   // optional; written to a temp file if multi-line
	Team        string   // optional team key (e.g. "ABC")
	Project     string   // optional project name or slug id
	State       string   // optional workflow state name (e.g. "Backlog")
	Labels      []string // optional; repeatable
}

// IssueResult is the parsed result of IssueCreate.
type IssueResult struct {
	ID  string // e.g. "ABC-123" — parsed from `linear issue create` output; "" if not detected
	Raw string // the full stdout (and stderr if non-empty) for display
}

// IssueCreate runs `linear issue create --no-interactive ...` and parses the
// new issue ID out of the output (the linear CLI prints the identifier
// somewhere in its output on success — we extract via regex).
func (l *Linear) IssueCreate(ctx context.Context, opts IssueCreateOptions) (IssueResult, error) {
	if strings.TrimSpace(opts.Title) == "" {
		return IssueResult{}, errors.New("issue title is required")
	}

	args := []string{"issue", "create", "--no-interactive", "--title", opts.Title}
	if opts.Team != "" {
		args = append(args, "--team", opts.Team)
	}
	if opts.Project != "" {
		args = append(args, "--project", opts.Project)
	}
	if opts.State != "" {
		args = append(args, "--state", opts.State)
	}
	for _, lbl := range opts.Labels {
		args = append(args, "--label", lbl)
	}

	// Description: prefer --description-file for multi-line content (avoids
	// shell-escaping pitfalls). Inline for single-line.
	var cleanup func()
	if opts.Description != "" {
		if strings.ContainsRune(opts.Description, '\n') {
			path, c, err := writeTempFile("arat-desc-", ".md", opts.Description)
			if err != nil {
				return IssueResult{}, fmt.Errorf("write description tempfile: %w", err)
			}
			cleanup = c
			args = append(args, "--description-file", path)
		} else {
			args = append(args, "--description", opts.Description)
		}
	}
	if cleanup != nil {
		defer cleanup()
	}

	stdout, stderr, err := l.run(ctx, "linear", args...)
	out := string(stdout)
	if err != nil {
		return IssueResult{Raw: combineOutput(out, string(stderr))},
			fmt.Errorf("linear issue create: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return IssueResult{ID: parseIssueID(out), Raw: combineOutput(out, string(stderr))}, nil
}

// Issue is a Linear issue summary as returned by IssueList.
type Issue struct {
	ID    string `json:"id"` // identifier, e.g. "ABC-123"
	Title string `json:"title"`
	State string `json:"state"` // workflow state name
	URL   string `json:"url"`
}

// IssueListOptions controls IssueList.
type IssueListOptions struct {
	AssignedToMe bool   // filter to issues assigned to the authenticated viewer
	Team         string // optional team key (e.g. "ABC")
	Limit        int    // max issues to return; default 50
}

// IssueList queries Linear via the GraphQL API (`linear api`) and returns
// the matching issues.
//
// We use the GraphQL route — instead of `linear issue mine` — because the
// CLI's table output is hard to parse robustly. The GraphQL response is
// JSON we can decode directly.
func (l *Linear) IssueList(ctx context.Context, opts IssueListOptions) ([]Issue, error) {
	if opts.Limit <= 0 {
		opts.Limit = 50
	}

	// Build a filter object. Assignee.isMe gives the viewer's issues; we
	// also restrict to non-completed states (triage/backlog/unstarted/started).
	filterFields := []string{`state: { type: { in: ["triage", "backlog", "unstarted", "started"] } }`}
	if opts.AssignedToMe {
		filterFields = append(filterFields, `assignee: { isMe: { eq: true } }`)
	}
	if opts.Team != "" {
		filterFields = append(filterFields, fmt.Sprintf(`team: { key: { eq: %q } }`, opts.Team))
	}
	filter := "{ " + strings.Join(filterFields, ", ") + " }"

	query := fmt.Sprintf(`{
  issues(filter: %s, first: %d, orderBy: updatedAt) {
    nodes { identifier title state { name } url }
  }
}`, filter, opts.Limit)

	stdout, stderr, err := l.run(ctx, "linear", "api", query)
	if err != nil {
		return nil, fmt.Errorf("linear api: %w: %s", err, strings.TrimSpace(string(stderr)))
	}

	var resp struct {
		Data struct {
			Issues struct {
				Nodes []struct {
					Identifier string `json:"identifier"`
					Title      string `json:"title"`
					State      struct {
						Name string `json:"name"`
					} `json:"state"`
					URL string `json:"url"`
				} `json:"nodes"`
			} `json:"issues"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(stdout, &resp); err != nil {
		return nil, fmt.Errorf("decode linear api response: %w (output: %s)", err, strings.TrimSpace(string(stdout)))
	}
	if len(resp.Errors) > 0 {
		return nil, fmt.Errorf("linear api: %s", resp.Errors[0].Message)
	}

	out := make([]Issue, 0, len(resp.Data.Issues.Nodes))
	for _, n := range resp.Data.Issues.Nodes {
		out = append(out, Issue{
			ID:    n.Identifier,
			Title: n.Title,
			State: n.State.Name,
			URL:   n.URL,
		})
	}
	return out, nil
}

// CommentAddOptions controls CommentAdd.
type CommentAddOptions struct {
	IssueID string // required, e.g. "ABC-123"; lowercased input is upper-cased
	Body    string // required; multi-line content uses --body-file
}

// CommentAdd runs `linear issue comment add <issueId> --body[-file] ...`.
func (l *Linear) CommentAdd(ctx context.Context, opts CommentAddOptions) error {
	if strings.TrimSpace(opts.IssueID) == "" {
		return errors.New("issue id is required")
	}
	if strings.TrimSpace(opts.Body) == "" {
		return errors.New("comment body is required")
	}

	args := []string{"issue", "comment", "add", strings.ToUpper(opts.IssueID)}

	var cleanup func()
	if strings.ContainsRune(opts.Body, '\n') {
		path, c, err := writeTempFile("arat-comment-", ".md", opts.Body)
		if err != nil {
			return fmt.Errorf("write comment tempfile: %w", err)
		}
		cleanup = c
		args = append(args, "--body-file", path)
	} else {
		args = append(args, "--body", opts.Body)
	}
	if cleanup != nil {
		defer cleanup()
	}

	_, stderr, err := l.run(ctx, "linear", args...)
	if err != nil {
		return fmt.Errorf("linear issue comment add: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// issueIDRE matches Linear identifiers like ABC-1234. The CLI's create output
// includes the new identifier (and its URL) — extracting it lets callers like
// `arat new --ticket-from-stdout` chain the result.
var issueIDRE = regexp.MustCompile(`\b[A-Z][A-Z0-9]+-[0-9]+\b`)

func parseIssueID(s string) string {
	return issueIDRE.FindString(s)
}

func combineOutput(stdout, stderr string) string {
	stdout = strings.TrimRight(stdout, "\n")
	stderr = strings.TrimRight(stderr, "\n")
	switch {
	case stdout != "" && stderr != "":
		return stdout + "\n" + stderr
	case stdout != "":
		return stdout
	default:
		return stderr
	}
}

func writeTempFile(prefix, suffix, content string) (string, func(), error) {
	f, err := os.CreateTemp("", prefix+"*"+suffix)
	if err != nil {
		return "", nil, err
	}
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", nil, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", nil, err
	}
	return f.Name(), func() { _ = os.Remove(f.Name()) }, nil
}
