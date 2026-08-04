package cmd

import (
	"errors"
	"fmt"

	"github.com/data-pata/arat/internal/config"
	"github.com/spf13/cobra"
)

func newConfigCmd(s *state) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "View or initialize configuration",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newConfigInitCmd(s), newConfigPathCmd(s))
	return cmd
}

func newConfigInitCmd(s *state) *cobra.Command {
	var force bool
	c := &cobra.Command{
		Use:   "init",
		Short: "Write a default config file",
		Long: `Write a commented default configuration to the resolved config path.
Refuses to overwrite an existing file unless --force is given.

The resolved path is (in order): --config flag, $ARAT_CONFIG,
$XDG_CONFIG_HOME/arat/config.toml, $HOME/.config/arat/config.toml.
`,
		Example: "  arat config init\n  arat config init --force",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := config.ResolvePath(s.configPath)
			if err != nil {
				return &exitErr{code: ExitConfig, err: err}
			}
			if err := config.WriteDefault(path, force); err != nil {
				if errors.Is(err, config.ErrExists) {
					return &exitErr{code: ExitConflict, err: fmt.Errorf("%s already exists; pass --force to overwrite", path)}
				}
				return &exitErr{code: ExitConfig, err: err}
			}
			fmt.Fprintf(s.deps.Stdout, "%s\n", path)
			fmt.Fprintf(s.deps.Stderr, "wrote default config; edit it to fit your setup\n")
			return nil
		},
	}
	c.Flags().BoolVarP(&force, "force", "f", false, "overwrite if exists")
	return c
}

func newConfigPathCmd(s *state) *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the resolved config path",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := config.ResolvePath(s.configPath)
			if err != nil {
				return &exitErr{code: ExitConfig, err: err}
			}
			fmt.Fprintf(s.deps.Stdout, "%s\n", path)
			return nil
		},
	}
}
