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
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// ErrCmd is matched (errors.Is) by every error this package returns for a
// failing `linear` tool: a subprocess that errored, a GraphQL error response,
// or output that could not be decoded. Callers use it to classify a failure
// as "the external tool failed" — arat's exit 6 — without string matching.
// Input-validation errors (empty title, empty id) do not carry it.
var ErrCmd = errors.New("linear command failed")

// cmdError carries the rendered message while matching both ErrCmd and the
// underlying error (when there is one) via the multi-error Unwrap.
type cmdError struct {
	msg string
	err error
}

func (e *cmdError) Error() string { return e.msg }
func (e *cmdError) Unwrap() []error {
	if e.err == nil {
		return []error{ErrCmd}
	}
	return []error{ErrCmd, e.err}
}

// cmdErrorf wraps a tool failure with a printf-rendered message. err may be
// nil for tool-level failures with no underlying Go error (a GraphQL error
// response, pagination that never terminates).
func cmdErrorf(err error, format string, args ...any) error {
	return &cmdError{msg: fmt.Sprintf(format, args...), err: err}
}

// Runner runs an external command and returns stdout, stderr, and any error.
// Tests inject a fake; production uses execRunner.
type Runner func(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)

// Linear is the wrapper.
type Linear struct{ run Runner }

// New returns a Linear that shells out to the real `linear` binary on PATH.
func New() *Linear { return &Linear{run: execRunner} }

// NewWithTimeout is New with a deadline on every subprocess. Zero d means no
// deadline. Per invocation: a paginated listing gets the budget per page,
// not for the whole loop.
func NewWithTimeout(d time.Duration) *Linear { return &Linear{run: timeoutRunner(execRunner, d)} }

// NewTraced is NewWithTimeout with a one-line trace per subprocess written
// to w (argv, duration, failure if any), wired from the ARAT_TRACE env var.
func NewTraced(d time.Duration, w io.Writer) *Linear {
	return &Linear{run: traceRunner(timeoutRunner(execRunner, d), w)}
}

// NewWithRunner returns a Linear using the given Runner. Used by tests.
func NewWithRunner(r Runner) *Linear { return &Linear{run: r} }

// timeoutRunner bounds each invocation of r with its own deadline.
func timeoutRunner(r Runner, d time.Duration) Runner {
	if d <= 0 {
		return r
	}
	return func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
		ctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		return r(ctx, name, args...)
	}
}

// traceRunner logs each invocation of r to w after it completes. Wrapped
// outside the timeout so the logged duration includes a deadline kill.
func traceRunner(r Runner, w io.Writer) Runner {
	return func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
		start := time.Now()
		stdout, stderr, err := r(ctx, name, args...)
		status := "ok"
		if err != nil {
			status = err.Error()
		}
		fmt.Fprintf(w, "trace: %s %s %s [%s]\n",
			name, strings.Join(args, " "), time.Since(start).Round(time.Millisecond), status)
		return stdout, stderr, err
	}
}

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
			cmdErrorf(err, "linear issue create: %s: %v", strings.TrimSpace(string(stderr)), err)
	}
	return IssueResult{ID: parseIssueID(out), Raw: combineOutput(out, string(stderr))}, nil
}

// Issue is a Linear issue summary as returned by IssueList.
type Issue struct {
	ID           string `json:"id"` // identifier, e.g. "ABC-123"
	Title        string `json:"title"`
	State        string `json:"state"`    // workflow state name
	Assignee     string `json:"assignee"` // display name; "" when unassigned
	AssigneeIsMe bool   `json:"assignee_is_me"`
	URL          string `json:"url"`
}

// IssueListOptions controls IssueList.
type IssueListOptions struct {
	Team string // optional team key (e.g. "ABC")
}

// issueMaxPages caps IssueList's pagination loop: 250 nodes per page allows
// 2000 open issues, far beyond a healthy team backlog, while bounding the
// damage of a non-advancing cursor.
const issueMaxPages = 8

// IssueList queries Linear via the GraphQL API (`linear api`) and returns
// every open issue of the team (states triage/backlog/unstarted/started),
// with assignee information so the picker built on it can rank the viewer's
// own issues first and offer self-assignment on unassigned ones. It does not
// filter by assignee: the issues one picks up when starting work are very
// often unassigned backlog items, and a picker that silently omits them is
// indistinguishable from the issue not existing.
//
// Paginated to completion for the same reason as ContainerList. We use the
// GraphQL route — instead of `linear issue list` — because the CLI's table
// output is hard to parse robustly.
func (l *Linear) IssueList(ctx context.Context, opts IssueListOptions) ([]Issue, error) {
	filterFields := []string{`state: { type: { in: ["triage", "backlog", "unstarted", "started"] } }`}
	if opts.Team != "" {
		filterFields = append(filterFields, fmt.Sprintf(`team: { key: { eq: %q } }`, opts.Team))
	}
	filter := "{ " + strings.Join(filterFields, ", ") + " }"

	var out []Issue
	cursor := ""
	for range issueMaxPages {
		after := ""
		if cursor != "" {
			after = fmt.Sprintf(", after: %q", cursor)
		}
		query := fmt.Sprintf(`{
  issues(filter: %s, first: 250%s, orderBy: updatedAt) {
    pageInfo { hasNextPage endCursor }
    nodes { identifier title state { name } url assignee { displayName isMe } }
  }
}`, filter, after)

		stdout, stderr, err := l.run(ctx, "linear", "api", query)
		if err != nil {
			return nil, cmdErrorf(err, "linear api: %s: %v", strings.TrimSpace(string(stderr)), err)
		}

		var resp struct {
			Data struct {
				Issues struct {
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []struct {
						Identifier string `json:"identifier"`
						Title      string `json:"title"`
						State      struct {
							Name string `json:"name"`
						} `json:"state"`
						URL      string `json:"url"`
						Assignee *struct {
							DisplayName string `json:"displayName"`
							IsMe        bool   `json:"isMe"`
						} `json:"assignee"`
					} `json:"nodes"`
				} `json:"issues"`
			} `json:"data"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		if err := json.Unmarshal(stdout, &resp); err != nil {
			return nil, cmdErrorf(err, "decode linear api response: %v (output: %s)", err, strings.TrimSpace(string(stdout)))
		}
		if len(resp.Errors) > 0 {
			return nil, cmdErrorf(nil, "linear api: %s", resp.Errors[0].Message)
		}

		data := resp.Data.Issues
		for _, n := range data.Nodes {
			iss := Issue{
				ID:    n.Identifier,
				Title: n.Title,
				State: n.State.Name,
				URL:   n.URL,
			}
			if n.Assignee != nil {
				iss.Assignee = n.Assignee.DisplayName
				iss.AssigneeIsMe = n.Assignee.IsMe
			}
			out = append(out, iss)
		}
		if !data.PageInfo.HasNextPage || data.PageInfo.EndCursor == "" || data.PageInfo.EndCursor == cursor {
			return out, nil
		}
		cursor = data.PageInfo.EndCursor
	}
	return nil, cmdErrorf(nil, "linear api: issues pagination did not terminate after %d pages", issueMaxPages)
}

// IssueAssignMe assigns the issue to the authenticated viewer, via
// `linear issue update <id> --assignee self`.
func (l *Linear) IssueAssignMe(ctx context.Context, id string) error {
	id = strings.ToUpper(strings.TrimSpace(id))
	if id == "" {
		return errors.New("issue id is required")
	}
	_, stderr, err := l.run(ctx, "linear", "issue", "update", id, "--assignee", "self")
	if err != nil {
		return cmdErrorf(err, "linear issue update %s --assignee self: %s: %v", id, strings.TrimSpace(string(stderr)), err)
	}
	return nil
}

// IssueTitle returns the title of a single issue, addressed by identifier
// (e.g. "REX-666", any case). Used by `arat new` to derive a workspace name
// from a --ticket flag, where the title is not otherwise in hand.
func (l *Linear) IssueTitle(ctx context.Context, id string) (string, error) {
	id = strings.ToUpper(strings.TrimSpace(id))
	if id == "" {
		return "", errors.New("issue id is required")
	}
	query := fmt.Sprintf(`{ issue(id: %q) { title } }`, id)

	stdout, stderr, err := l.run(ctx, "linear", "api", query)
	if err != nil {
		return "", cmdErrorf(err, "linear api: %s: %v", strings.TrimSpace(string(stderr)), err)
	}
	var resp struct {
		Data struct {
			Issue struct {
				Title string `json:"title"`
			} `json:"issue"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(stdout, &resp); err != nil {
		return "", cmdErrorf(err, "decode linear api response: %v (output: %s)", err, strings.TrimSpace(string(stdout)))
	}
	if len(resp.Errors) > 0 {
		return "", cmdErrorf(nil, "linear api: %s", resp.Errors[0].Message)
	}
	if resp.Data.Issue.Title == "" {
		return "", fmt.Errorf("issue %s not found (or has no title)", id)
	}
	return resp.Data.Issue.Title, nil
}

// Container is a Linear project or initiative — the two things an arat
// project workspace can be linked to.
//
// They are modelled as one type because arat treats them the same way: a
// named, slug-addressed grouping above the issue level. Linear itself does
// not nest projects (only initiatives have parentInitiative/subInitiatives),
// so arat does not tie its own nesting depth to which of the two you pick.
type Container struct {
	Kind string `json:"kind"` // "project" or "initiative"
	ID   string `json:"id"`   // slugId
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Container kinds.
const (
	ContainerProject    = "project"
	ContainerInitiative = "initiative"
)

// ContainerLister is the read surface project-workspace attaching consumes.
type ContainerLister interface {
	ContainerList(ctx context.Context, kind string) ([]Container, error)
}

// containerMaxPages caps ContainerList's pagination loop. At 250 nodes per
// page this allows 5000 containers, far beyond any real workspace, while
// bounding the damage if the API ever returned a non-advancing cursor.
const containerMaxPages = 20

// ContainerList returns every Linear project or initiative the authenticated
// viewer can see, depending on kind.
//
// The list is paginated to completion: a large workspace holds far more than
// one page of projects (Kivra has 550+), and the picker built on this list
// silently missing an entry is indistinguishable from the project not
// existing.
//
// As with IssueList this goes through `linear api` rather than
// `linear project list` — the CLI's table output is not designed for
// parsing, whereas the GraphQL response decodes directly.
func (l *Linear) ContainerList(ctx context.Context, kind string) ([]Container, error) {
	var root string
	switch kind {
	case ContainerProject:
		root = "projects"
	case ContainerInitiative:
		root = "initiatives"
	default:
		return nil, fmt.Errorf("unknown container kind %q (want %q or %q)", kind, ContainerProject, ContainerInitiative)
	}

	var out []Container
	cursor := ""
	for range containerMaxPages {
		after := ""
		if cursor != "" {
			// The cursor is an opaque token from the previous response;
			// %q escapes it safely into the query string.
			after = fmt.Sprintf(", after: %q", cursor)
		}
		query := fmt.Sprintf(`{
  %s(first: 250%s) {
    pageInfo { hasNextPage endCursor }
    nodes { slugId name url }
  }
}`, root, after)

		stdout, stderr, err := l.run(ctx, "linear", "api", query)
		if err != nil {
			return nil, cmdErrorf(err, "linear api: %s: %v", strings.TrimSpace(string(stderr)), err)
		}

		var resp struct {
			Data map[string]struct {
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []struct {
					SlugID string `json:"slugId"`
					Name   string `json:"name"`
					URL    string `json:"url"`
				} `json:"nodes"`
			} `json:"data"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		if err := json.Unmarshal(stdout, &resp); err != nil {
			return nil, cmdErrorf(err, "decode linear api response: %v (output: %s)", err, strings.TrimSpace(string(stdout)))
		}
		if len(resp.Errors) > 0 {
			return nil, cmdErrorf(nil, "linear api: %s", resp.Errors[0].Message)
		}

		data := resp.Data[root]
		for _, n := range data.Nodes {
			out = append(out, Container{Kind: kind, ID: n.SlugID, Name: n.Name, URL: n.URL})
		}
		if !data.PageInfo.HasNextPage || data.PageInfo.EndCursor == "" || data.PageInfo.EndCursor == cursor {
			return out, nil
		}
		cursor = data.PageInfo.EndCursor
	}
	return nil, cmdErrorf(nil, "linear api: %s pagination did not terminate after %d pages", root, containerMaxPages)
}

// ProjectCreateOptions controls ProjectCreate.
type ProjectCreateOptions struct {
	Name        string // required
	Team        string // required team key (e.g. "ABC") — `linear project create` demands one
	Description string // optional; Linear's API caps it at 255 characters
}

// ProjectCreate runs `linear project create --json ...` and returns the new
// project as a Container, ready to be linked to a workspace.
//
// Unlike issue create, the project subcommand offers --json, so the result is
// decoded directly instead of regex-scraped from table output.
func (l *Linear) ProjectCreate(ctx context.Context, opts ProjectCreateOptions) (Container, error) {
	if strings.TrimSpace(opts.Name) == "" {
		return Container{}, errors.New("project name is required")
	}
	if strings.TrimSpace(opts.Team) == "" {
		return Container{}, errors.New("team is required to create a linear project")
	}

	args := []string{"project", "create", "--json", "--name", opts.Name, "--team", opts.Team}

	var cleanup func()
	if opts.Description != "" {
		if strings.ContainsRune(opts.Description, '\n') {
			path, c, err := writeTempFile("arat-projdesc-", ".md", opts.Description)
			if err != nil {
				return Container{}, fmt.Errorf("write description tempfile: %w", err)
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
	if err != nil {
		return Container{}, cmdErrorf(err, "linear project create: %s: %v", strings.TrimSpace(string(stderr)), err)
	}

	// Shape per linear-cli: {"success": bool, "project": {id, slugId, name, url}}.
	var resp struct {
		Success bool `json:"success"`
		Project struct {
			SlugID string `json:"slugId"`
			Name   string `json:"name"`
			URL    string `json:"url"`
		} `json:"project"`
	}
	if err := json.Unmarshal(stdout, &resp); err != nil {
		return Container{}, cmdErrorf(err, "decode linear project create output: %v (output: %s)", err, strings.TrimSpace(string(stdout)))
	}
	if resp.Project.SlugID == "" {
		return Container{}, cmdErrorf(nil, "linear project create: no project in response (output: %s)", strings.TrimSpace(string(stdout)))
	}
	return Container{
		Kind: ContainerProject,
		ID:   resp.Project.SlugID,
		Name: resp.Project.Name,
		URL:  resp.Project.URL,
	}, nil
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
		return cmdErrorf(err, "linear issue comment add: %s: %v", strings.TrimSpace(string(stderr)), err)
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
