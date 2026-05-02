// Package shell generates shell-integration snippets that wrap the arat
// binary so `arat go <name>` actually cds the user's shell.
package shell

import (
	"fmt"
	"strings"
)

// bashZsh is the shared init for bash and zsh. Both shells parse the same
// POSIX-ish syntax for the bits we use here.
const bashZsh = `# arat shell integration ({SHELL})
# Add to your shell rc:  eval "$(arat init {SHELL})"
#
# Wraps the arat binary so "arat go <name>" cds your interactive shell
# into the resolved workspace path. All other subcommands pass through
# untouched.
arat() {
  if [ "$1" = "go" ]; then
    # If the user is asking for help, pass through — help goes to stdout
    # and must not be captured as a cd target.
    for __arat_arg in "$@"; do
      case "$__arat_arg" in
        -h|--help)
          command arat "$@"
          return $?
          ;;
      esac
    done
    shift
    local target
    target="$(command arat go --print "$@")" || return $?
    [ -n "$target" ] && cd "$target"
  else
    command arat "$@"
  fi
}

# Tab completion
eval "$(command arat completion {SHELL})"
`

// fishScript uses fish's distinct syntax.
const fishScript = `# arat shell integration (fish)
# Add to your config.fish:  arat init fish | source
#
# Wraps the arat binary so "arat go <name>" cds your interactive shell
# into the resolved workspace path. All other subcommands pass through
# untouched.
function arat
    if test "$argv[1]" = "go"
        # If the user is asking for help, pass through — help goes to stdout
        # and must not be captured as a cd target.
        for arg in $argv
            if test "$arg" = "-h" -o "$arg" = "--help"
                command arat $argv
                return $status
            end
        end
        set -l rest $argv[2..-1]
        set -l target (command arat go --print $rest)
        or return $status
        if test -n "$target"
            cd $target
        end
    else
        command arat $argv
    end
end

# Tab completion
command arat completion fish | source
`

// Init returns the integration snippet for the named shell.
func Init(shell string) (string, error) {
	switch shell {
	case "bash":
		return strings.ReplaceAll(bashZsh, "{SHELL}", "bash"), nil
	case "zsh":
		return strings.ReplaceAll(bashZsh, "{SHELL}", "zsh"), nil
	case "fish":
		return fishScript, nil
	default:
		return "", fmt.Errorf("unsupported shell %q (supported: bash, zsh, fish)", shell)
	}
}
