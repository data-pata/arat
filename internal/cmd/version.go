package cmd

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// version is set at build time via -ldflags or, when empty, derived from
// runtime/debug.ReadBuildInfo() (vcs.revision).
var version string

func newVersionCmd(s *state) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version and build info",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintf(s.deps.Stdout, "arat %s\n", resolveVersion())
			return nil
		},
	}
}

func resolveVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, kv := range info.Settings {
			if kv.Key == "vcs.revision" && kv.Value != "" {
				return "dev (" + kv.Value[:min(len(kv.Value), 12)] + ")"
			}
		}
	}
	return "dev"
}
