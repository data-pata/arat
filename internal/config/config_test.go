package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_minimal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	require.NoError(t, os.WriteFile(path, []byte(`
root = "/tmp/myroot"
branch_prefix = "ps"
`), 0o644))

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "/tmp/myroot", cfg.Root)
	assert.Equal(t, "/tmp/myroot/feat", cfg.WorkspacesDir, "workspaces_dir defaults to root/feat")
	assert.Equal(t, "ps", cfg.BranchPrefix)
	assert.Equal(t, `^[a-z]+-[0-9]+$`, cfg.TicketPattern)
	assert.NotNil(t, cfg.TicketRegex())
	assert.Equal(t, "auto", cfg.TUI.Theme)
	assert.Equal(t, path, cfg.Path)
}

func TestLoad_homeExpansion(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	path := filepath.Join(dir, "config.toml")
	require.NoError(t, os.WriteFile(path, []byte(`
root = "~/work"
branch_prefix = "ab"
workspaces_dir = "~/work/ws"
`), 0o644))

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "work"), cfg.Root)
	assert.Equal(t, filepath.Join(dir, "work/ws"), cfg.WorkspacesDir)
}

func TestLoad_envExpansion(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MY_BASE", dir)

	path := filepath.Join(dir, "config.toml")
	require.NoError(t, os.WriteFile(path, []byte(`
root = "$MY_BASE/code"
branch_prefix = "x"
`), 0o644))

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "code"), cfg.Root)
}

func TestLoad_missingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	require.ErrorIs(t, err, ErrNotFound)
}

func TestLoad_validationErrors(t *testing.T) {
	tests := []struct {
		name    string
		toml    string
		wantSub string
	}{
		{"missing root", `branch_prefix = "x"`, "`root` is required"},
		{"missing branch_prefix", `root = "/tmp"`, "`branch_prefix` is required"},
		{"branch_prefix has slash", `root = "/tmp"` + "\n" + `branch_prefix = "a/b"`, "branch_prefix"},
		{"bad ticket regex", `root = "/tmp"` + "\n" + `branch_prefix = "x"` + "\n" + `ticket_pattern = "["`, "ticket_pattern"},
		{"bad theme", `root = "/tmp"` + "\n" + `branch_prefix = "x"` + "\n" + `[tui]` + "\n" + `theme = "neon"`, "tui.theme"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			require.NoError(t, os.WriteFile(path, []byte(tt.toml), 0o644))
			_, err := Load(path)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantSub)
		})
	}
}

func TestResolvePath(t *testing.T) {
	t.Run("explicit wins", func(t *testing.T) {
		t.Setenv("ARAT_CONFIG", "/from/env")
		p, err := ResolvePath("/explicit")
		require.NoError(t, err)
		assert.Equal(t, "/explicit", p)
	})
	t.Run("env over xdg", func(t *testing.T) {
		t.Setenv("ARAT_CONFIG", "/from/env")
		t.Setenv("XDG_CONFIG_HOME", "/from/xdg")
		p, err := ResolvePath("")
		require.NoError(t, err)
		assert.Equal(t, "/from/env", p)
	})
	t.Run("xdg over home", func(t *testing.T) {
		t.Setenv("ARAT_CONFIG", "")
		t.Setenv("XDG_CONFIG_HOME", "/xdg")
		p, err := ResolvePath("")
		require.NoError(t, err)
		assert.Equal(t, "/xdg/arat/config.toml", p)
	})
	t.Run("home fallback", func(t *testing.T) {
		t.Setenv("ARAT_CONFIG", "")
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", "/h")
		p, err := ResolvePath("")
		require.NoError(t, err)
		assert.Equal(t, "/h/.config/arat/config.toml", p)
	})
}

func TestWriteDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "config.toml")

	require.NoError(t, WriteDefault(path, false))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.True(t, strings.Contains(string(data), "branch_prefix"))

	// Second call without force: ErrExists.
	err = WriteDefault(path, false)
	require.ErrorIs(t, err, ErrExists)

	// With force: overwrites.
	require.NoError(t, WriteDefault(path, true))

	// The default stub itself must Load() successfully (parse + minimal validation).
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.NotEmpty(t, cfg.Root)
	assert.NotEmpty(t, cfg.BranchPrefix)
}
