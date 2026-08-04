// Package config loads arat's TOML configuration.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Config is the resolved, validated configuration.
type Config struct {
	Root                  string   `toml:"root"`
	WorkspacesDir         string   `toml:"workspaces_dir"`
	BranchPrefix          string   `toml:"branch_prefix"`
	TicketPattern         string   `toml:"ticket_pattern"`
	TicketURL             string   `toml:"ticket_url"`
	DefaultRepos          []string `toml:"default_repos"`
	AutoReposGlob         []string `toml:"auto_repos_glob"`
	GenerateCodeWorkspace bool     `toml:"generate_code_workspace"`
	// CommandTimeout bounds each git / linear subprocess, as a Go duration
	// string ("5m", "90s"); "0" disables the bound. Defaults to 5m: generous
	// enough that a slow fetch is never killed, finite so a network black
	// hole cannot hang arat forever. Per subprocess, not per arat command —
	// a 25-repo `arat new` gives every fetch its own budget.
	CommandTimeout string       `toml:"command_timeout"`
	Linear         LinearConfig `toml:"linear"`
	TUI            TUIConfig    `toml:"tui"`

	// Path is the file the config was loaded from. Empty if defaults-only.
	Path string `toml:"-"`

	ticketRE       *regexp.Regexp
	commandTimeout time.Duration
}

type LinearConfig struct {
	Enabled     bool   `toml:"enabled"`
	DefaultTeam string `toml:"default_team"`
	URLTemplate string `toml:"url_template"`
}

type TUIConfig struct {
	Theme string `toml:"theme"` // auto|light|dark
}

// TicketRegex returns the compiled ticket pattern.
func (c *Config) TicketRegex() *regexp.Regexp { return c.ticketRE }

// CommandTimeoutDuration returns the parsed per-subprocess timeout. Zero
// means no bound. Only configs that went through Load carry the 5m default;
// a zero-value Config (tests) runs unbounded.
func (c *Config) CommandTimeoutDuration() time.Duration { return c.commandTimeout }

// ResolvePath returns the config file path arat would load given the env, in order:
//  1. explicit (when non-empty) — returned as-is
//  2. $ARAT_CONFIG
//  3. $XDG_CONFIG_HOME/arat/config.toml
//  4. $HOME/.config/arat/config.toml
func ResolvePath(explicit string) (string, error) {
	if explicit != "" {
		return expand(explicit)
	}
	if v := os.Getenv("ARAT_CONFIG"); v != "" {
		return expand(v)
	}
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "arat", "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	return filepath.Join(home, ".config", "arat", "config.toml"), nil
}

// Load reads the config at path, applies defaults, and validates. If the file
// doesn't exist, returns ErrNotFound (path is still set).
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.Path = path
	if err := cfg.applyDefaultsAndValidate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// ErrNotFound is returned when the config file does not exist.
var ErrNotFound = errors.New("config not found")

func (c *Config) applyDefaultsAndValidate() error {
	if c.Root == "" {
		return errors.New("config: `root` is required")
	}
	root, err := expand(c.Root)
	if err != nil {
		return fmt.Errorf("expand root: %w", err)
	}
	c.Root = root

	if c.WorkspacesDir == "" {
		c.WorkspacesDir = filepath.Join(c.Root, "feat")
	} else {
		c.WorkspacesDir, err = expand(c.WorkspacesDir)
		if err != nil {
			return fmt.Errorf("expand workspaces_dir: %w", err)
		}
	}

	if c.BranchPrefix == "" {
		return errors.New("config: `branch_prefix` is required")
	}
	if strings.ContainsAny(c.BranchPrefix, " \t/") {
		return fmt.Errorf("config: branch_prefix %q contains whitespace or `/`", c.BranchPrefix)
	}

	if c.TicketPattern == "" {
		c.TicketPattern = `^[a-z]+-[0-9]+$`
	}
	re, err := regexp.Compile(c.TicketPattern)
	if err != nil {
		return fmt.Errorf("config: ticket_pattern %q invalid: %w", c.TicketPattern, err)
	}
	c.ticketRE = re

	if c.CommandTimeout == "" {
		c.commandTimeout = 5 * time.Minute
	} else {
		d, err := time.ParseDuration(c.CommandTimeout)
		if err != nil || d < 0 {
			return fmt.Errorf("config: command_timeout %q invalid: want a Go duration like \"5m\" (\"0\" disables)", c.CommandTimeout)
		}
		c.commandTimeout = d
	}

	if c.TUI.Theme == "" {
		c.TUI.Theme = "auto"
	}
	switch c.TUI.Theme {
	case "auto", "light", "dark":
	default:
		return fmt.Errorf("config: tui.theme %q must be auto|light|dark", c.TUI.Theme)
	}
	return nil
}

// DefaultTOML is the commented stub written by `arat config init`.
//
// The stub is intentionally generic — placeholders below (`<your-org>`,
// `<your-initials>`, etc.) must be edited before arat is useful. The stub
// does parse and pass minimal validation as-is, so commands that don't
// need a real workspace (e.g. ` + "`arat config path`" + `) keep working.
const DefaultTOML = `# arat configuration. See https://github.com/data-pata/arat for details.
#
# This file was generated by ` + "`arat config init`" + `. Edit the placeholders
# (<your-org>, <your-initials>, ...) for your setup before using arat.

# Where canonical clones live (git fetch happens here). Required.
root = "~/git/<your-org>"

# Where workspaces are created. Defaults to "$root/feat".
# workspaces_dir = "~/git/<your-org>/feat"

# Branch prefix. arat creates branches named "<branch_prefix>--<short>--<ticket>".
# Conventionally the user's initials. Required.
branch_prefix = "<your-initials>"

# Regex a ticket id must match (lowercased before matching). Used to validate
# --ticket on "arat new" / "arat attach" and to recognise the ticket in
# existing workspace directory names.
# ticket_pattern = "^[a-z]+-[0-9]+$"

# Ticket URL template. {TICKET} -> "abc-123"; {TICKET_UPPER} -> "ABC-123".
# ticket_url = "https://linear.app/<your-org>/issue/{TICKET_UPPER}"

# Default repo set when none is given to ` + "`arat new`" + `. Each must exist as a clone under root.
default_repos = []

# Globs (relative to root) to auto-include. e.g. "core-*" picks up every core-* clone.
# auto_repos_glob = []

# Generate a .code-workspace file alongside the workspace dir.
generate_code_workspace = false

# Timeout for each git / linear subprocess (Go duration; "0" disables).
# Generous by default so a slow fetch survives, finite so a hung network
# call cannot hang arat forever.
# command_timeout = "5m"

[linear]
enabled       = false
# default_team  = "ABC"
# url_template  = "https://linear.app/<your-org>/issue/{TICKET_UPPER}"

[tui]
theme = "auto"   # auto|light|dark
`

// WriteDefault writes the commented stub to path. Returns ErrExists if the file
// already exists and force is false.
func WriteDefault(path string, force bool) error {
	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%w: %s", ErrExists, path)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	return os.WriteFile(path, []byte(DefaultTOML), 0o644)
}

// ErrExists indicates `arat config init` was called on an existing file without --force.
var ErrExists = errors.New("config already exists")

func expand(p string) (string, error) {
	p = os.ExpandEnv(p)
	// Only the caller's own home is expanded: "~" and "~/x". A "~user/x"
	// path is left untouched — mapping it onto $HOME/user/x (what a blind
	// TrimPrefix does) would silently point the config at the wrong place.
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return filepath.Clean(p), nil
}
