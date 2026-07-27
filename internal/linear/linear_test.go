package linear

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recorderRunner struct {
	calls    [][]string // recorded argv (excluding the binary name)
	stdout   []byte
	stderr   []byte
	err      error
	stdoutFn func(args []string) []byte // dynamic override
}

func (r *recorderRunner) run() Runner {
	return func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
		r.calls = append(r.calls, append([]string{name}, args...))
		if r.stdoutFn != nil {
			return r.stdoutFn(args), r.stderr, r.err
		}
		return r.stdout, r.stderr, r.err
	}
}

func TestAvailable(t *testing.T) {
	rr := &recorderRunner{}
	l := NewWithRunner(rr.run())
	assert.NoError(t, l.Available(t.Context()))
	require.Len(t, rr.calls, 1)
	assert.Equal(t, []string{"linear", "--version"}, rr.calls[0])
}

func TestAvailable_returnsUnderlyingError(t *testing.T) {
	wantErr := errors.New("not found")
	l := NewWithRunner(func(context.Context, string, ...string) ([]byte, []byte, error) {
		return nil, nil, wantErr
	})
	err := l.Available(t.Context())
	assert.ErrorIs(t, err, wantErr)
}

func TestIssueCreate_argvShape(t *testing.T) {
	rr := &recorderRunner{stdout: []byte("Created issue ABC-9999 (https://linear.app/x)")}
	l := NewWithRunner(rr.run())

	res, err := l.IssueCreate(t.Context(), IssueCreateOptions{
		Title:   "Fix the thing",
		Team:    "ABC",
		Project: "Side quest",
		State:   "Backlog",
		Labels:  []string{"BE", "api"},
	})
	require.NoError(t, err)
	assert.Equal(t, "ABC-9999", res.ID)
	require.Len(t, rr.calls, 1)
	assert.Equal(t, []string{
		"linear", "issue", "create", "--no-interactive",
		"--title", "Fix the thing",
		"--team", "ABC",
		"--project", "Side quest",
		"--state", "Backlog",
		"--label", "BE",
		"--label", "api",
	}, rr.calls[0])
}

func TestIssueCreate_inlineDescription(t *testing.T) {
	rr := &recorderRunner{stdout: []byte("Created ABC-1")}
	l := NewWithRunner(rr.run())

	_, err := l.IssueCreate(t.Context(), IssueCreateOptions{
		Title:       "x",
		Description: "single line",
	})
	require.NoError(t, err)
	require.Len(t, rr.calls, 1)
	assert.Contains(t, rr.calls[0], "--description")
	assert.Contains(t, rr.calls[0], "single line")
	assert.NotContains(t, strings.Join(rr.calls[0], " "), "--description-file")
}

func TestIssueCreate_multilineDescriptionUsesFile(t *testing.T) {
	rr := &recorderRunner{stdout: []byte("ABC-1")}
	rr.stdoutFn = func(args []string) []byte {
		// Capture the description-file path so we can read its contents.
		for i, a := range args {
			if a == "--description-file" && i+1 < len(args) {
				data, _ := os.ReadFile(args[i+1])
				assert.Equal(t, "line1\nline2\n", string(data))
			}
		}
		return []byte("ABC-1")
	}
	l := NewWithRunner(rr.run())

	_, err := l.IssueCreate(t.Context(), IssueCreateOptions{
		Title:       "x",
		Description: "line1\nline2\n",
	})
	require.NoError(t, err)
	require.Len(t, rr.calls, 1)
	assert.Contains(t, rr.calls[0], "--description-file")
	assert.NotContains(t, strings.Join(rr.calls[0], " "), "--description ")
}

func TestIssueCreate_titleRequired(t *testing.T) {
	l := NewWithRunner((&recorderRunner{}).run())
	_, err := l.IssueCreate(t.Context(), IssueCreateOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "title")
}

func TestIssueCreate_runnerError(t *testing.T) {
	rr := &recorderRunner{stderr: []byte("auth required"), err: errors.New("exit 1")}
	l := NewWithRunner(rr.run())
	res, err := l.IssueCreate(t.Context(), IssueCreateOptions{Title: "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth required")
	assert.Contains(t, res.Raw, "auth required")
}

func TestIssueCreate_noIDInOutput(t *testing.T) {
	rr := &recorderRunner{stdout: []byte("just some text")}
	l := NewWithRunner(rr.run())
	res, err := l.IssueCreate(t.Context(), IssueCreateOptions{Title: "x"})
	require.NoError(t, err)
	assert.Empty(t, res.ID)
	assert.Equal(t, "just some text", res.Raw)
}

func TestCommentAdd_inlineBody(t *testing.T) {
	rr := &recorderRunner{}
	l := NewWithRunner(rr.run())

	require.NoError(t, l.CommentAdd(t.Context(), CommentAddOptions{
		IssueID: "abc-2881",
		Body:    "looks good",
	}))
	require.Len(t, rr.calls, 1)
	assert.Equal(t, []string{
		"linear", "issue", "comment", "add", "ABC-2881",
		"--body", "looks good",
	}, rr.calls[0])
}

func TestCommentAdd_multilineUsesFile(t *testing.T) {
	rr := &recorderRunner{}
	rr.stdoutFn = func(args []string) []byte {
		for i, a := range args {
			if a == "--body-file" && i+1 < len(args) {
				data, _ := os.ReadFile(args[i+1])
				assert.Equal(t, "line1\nline2", string(data))
			}
		}
		return nil
	}
	l := NewWithRunner(rr.run())

	require.NoError(t, l.CommentAdd(t.Context(), CommentAddOptions{
		IssueID: "ABC-1",
		Body:    "line1\nline2",
	}))
	require.Len(t, rr.calls, 1)
	assert.Contains(t, rr.calls[0], "--body-file")
}

func TestCommentAdd_validation(t *testing.T) {
	l := NewWithRunner((&recorderRunner{}).run())
	require.Error(t, l.CommentAdd(t.Context(), CommentAddOptions{Body: "x"}))
	require.Error(t, l.CommentAdd(t.Context(), CommentAddOptions{IssueID: "ABC-1"}))
}

func TestCommentAdd_runnerError(t *testing.T) {
	rr := &recorderRunner{stderr: []byte("forbidden"), err: errors.New("exit 1")}
	l := NewWithRunner(rr.run())
	err := l.CommentAdd(t.Context(), CommentAddOptions{IssueID: "ABC-1", Body: "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forbidden")
}

func TestIssueList_argvAndDecoding(t *testing.T) {
	rr := &recorderRunner{stdout: []byte(`{
  "data": {
    "issues": {
      "nodes": [
        {"identifier": "ABC-1", "title": "First", "state": {"name": "Backlog"}, "url": "https://linear.app/x/issue/ABC-1"},
        {"identifier": "ABC-2", "title": "Second", "state": {"name": "Started"}, "url": "https://linear.app/x/issue/ABC-2"}
      ]
    }
  }
}`)}
	l := NewWithRunner(rr.run())

	got, err := l.IssueList(t.Context(), IssueListOptions{AssignedToMe: true, Team: "ABC", Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, Issue{ID: "ABC-1", Title: "First", State: "Backlog", URL: "https://linear.app/x/issue/ABC-1"}, got[0])
	assert.Equal(t, "ABC-2", got[1].ID)

	require.Len(t, rr.calls, 1)
	assert.Equal(t, "linear", rr.calls[0][0])
	assert.Equal(t, "api", rr.calls[0][1])
	q := rr.calls[0][2]
	assert.Contains(t, q, `assignee: { isMe: { eq: true } }`)
	assert.Contains(t, q, `team: { key: { eq: "ABC" } }`)
	assert.Contains(t, q, `first: 10`)
}

func TestIssueList_defaultLimit(t *testing.T) {
	rr := &recorderRunner{stdout: []byte(`{"data":{"issues":{"nodes":[]}}}`)}
	l := NewWithRunner(rr.run())
	_, err := l.IssueList(t.Context(), IssueListOptions{})
	require.NoError(t, err)
	assert.Contains(t, rr.calls[0][2], "first: 50")
}

func TestIssueList_apiError(t *testing.T) {
	rr := &recorderRunner{stdout: []byte(`{"errors":[{"message":"forbidden"}]}`)}
	l := NewWithRunner(rr.run())
	_, err := l.IssueList(t.Context(), IssueListOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forbidden")
}

func TestIssueList_runnerError(t *testing.T) {
	rr := &recorderRunner{stderr: []byte("auth required"), err: errors.New("exit 1")}
	l := NewWithRunner(rr.run())
	_, err := l.IssueList(t.Context(), IssueListOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth required")
}

func TestIssueList_badJSON(t *testing.T) {
	rr := &recorderRunner{stdout: []byte("not json")}
	l := NewWithRunner(rr.run())
	_, err := l.IssueList(t.Context(), IssueListOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode")
}

func TestParseIssueID(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"Created issue ABC-2881", "ABC-2881"},
		{"https://linear.app/x/issue/ENG-99/foo", "ENG-99"},
		{"prefix RK1B-12 suffix", "RK1B-12"},
		{"no id here", ""},
		{"lowercase abc-1 ignored", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			assert.Equal(t, tt.want, parseIssueID(tt.in))
		})
	}
}

func TestCombineOutput(t *testing.T) {
	assert.Equal(t, "out", combineOutput("out\n", ""))
	assert.Equal(t, "err", combineOutput("", "err\n"))
	assert.Equal(t, "out\nerr", combineOutput("out\n", "err\n"))
	assert.Equal(t, "", combineOutput("", ""))
}

func TestContainerList_projects(t *testing.T) {
	rr := &recorderRunner{stdout: []byte(`{"data":{"projects":{"nodes":[
		{"slugId":"slug-1","name":"Q3 Billing","url":"https://linear.app/o/project/slug-1"},
		{"slugId":"slug-2","name":"Dunning","url":"https://linear.app/o/project/slug-2"}
	]}}}`)}
	l := NewWithRunner(rr.run())

	got, err := l.ContainerList(t.Context(), ContainerProject)
	require.NoError(t, err)
	assert.Equal(t, []Container{
		{Kind: "project", ID: "slug-1", Name: "Q3 Billing", URL: "https://linear.app/o/project/slug-1"},
		{Kind: "project", ID: "slug-2", Name: "Dunning", URL: "https://linear.app/o/project/slug-2"},
	}, got)

	require.Len(t, rr.calls, 1)
	assert.Equal(t, "linear", rr.calls[0][0])
	assert.Equal(t, "api", rr.calls[0][1])
	assert.Contains(t, rr.calls[0][2], "projects(first: 250)")
	assert.Contains(t, rr.calls[0][2], "slugId name url")
}

func TestContainerList_initiatives(t *testing.T) {
	rr := &recorderRunner{stdout: []byte(`{"data":{"initiatives":{"nodes":[
		{"slugId":"abc123","name":"Payments 2026","url":"https://linear.app/o/initiative/abc123"}
	]}}}`)}
	l := NewWithRunner(rr.run())

	got, err := l.ContainerList(t.Context(), ContainerInitiative)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "initiative", got[0].Kind)
	assert.Equal(t, "abc123", got[0].ID)
	assert.Contains(t, rr.calls[0][2], "initiatives(first: 250)")
}

func TestContainerList_unknownKind(t *testing.T) {
	rr := &recorderRunner{}
	l := NewWithRunner(rr.run())

	_, err := l.ContainerList(t.Context(), "epic")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown container kind")
	assert.Empty(t, rr.calls, "an unknown kind must not reach the linear binary")
}

func TestContainerList_graphqlErrors(t *testing.T) {
	rr := &recorderRunner{stdout: []byte(`{"errors":[{"message":"nope"}]}`)}
	l := NewWithRunner(rr.run())

	_, err := l.ContainerList(t.Context(), ContainerProject)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nope")
}
