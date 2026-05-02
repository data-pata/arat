package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// codeWorkspaceFile is the JSON shape VS Code expects for a multi-root
// `.code-workspace` file.
type codeWorkspaceFile struct {
	Folders  []codeWorkspaceFolder `json:"folders"`
	Settings map[string]any        `json:"settings,omitempty"`
}

type codeWorkspaceFolder struct {
	Name string `json:"name,omitempty"`
	Path string `json:"path"`
}

// writeCodeWorkspace writes a `<name>.code-workspace` file at the workspace
// dir, with one folder entry per repo (relative path) — this is what VS
// Code's "open as workspace" expects for multi-root layouts.
func writeCodeWorkspace(workspaceDir, name string, repos []string) error {
	cw := codeWorkspaceFile{
		Folders: make([]codeWorkspaceFolder, 0, len(repos)),
	}
	for _, r := range repos {
		cw.Folders = append(cw.Folders, codeWorkspaceFolder{
			Name: r,
			Path: "./" + r,
		})
	}
	// also include the workspace root so users can browse CLAUDE.md /
	// claude_workspace from VS Code's tree without going outside the project
	cw.Folders = append(cw.Folders, codeWorkspaceFolder{Name: "(workspace root)", Path: "."})

	data, err := json.MarshalIndent(cw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal code-workspace: %w", err)
	}
	out := filepath.Join(workspaceDir, name+".code-workspace")
	return os.WriteFile(out, append(data, '\n'), 0o644)
}
