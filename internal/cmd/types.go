package cmd

import "github.com/data-pata/arat/internal/workspace"

// Workspace and RepoStatus are aliased into the cmd package so
// the Service interface in root.go can refer to them without an
// import cycle.
type (
	Workspace  = workspace.Workspace
	RepoStatus = workspace.RepoStatus
)
