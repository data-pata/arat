package cmd

import (
	"fmt"

	"github.com/data-pata/arat/internal/shell"
	"github.com/spf13/cobra"
)

func newInitCmd(s *state) *cobra.Command {
	return &cobra.Command{
		Use:   "init <bash|zsh|fish>",
		Short: "Print shell integration to source via eval",
		Long: `Print a shell function that wraps the arat binary so that
"arat go <name>" actually cds your interactive shell into the chosen
workspace. All other subcommands pass through unchanged.

Add to your shell rc:
  eval "$(arat init zsh)"            # bash and zsh
  arat init fish | source            # fish
`,
		Example: `  eval "$(arat init zsh)"
  arat init bash > ~/.config/arat/integration.bash
  arat init fish | source`,
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish"},
		RunE: func(cmd *cobra.Command, args []string) error {
			script, err := shell.Init(args[0])
			if err != nil {
				return &exitErr{code: ExitUsage, err: err}
			}
			fmt.Fprint(s.deps.Stdout, script)
			return nil
		},
	}
}
