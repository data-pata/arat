package workspace

import (
	"context"
	"fmt"
)

// LinkOptions controls Service.LinkLinear.
type LinkOptions struct {
	Ref    string    // workspace ref; must resolve to a project workspace
	Linear LinearRef // the Linear project or initiative to attach
}

// LinkLinear attaches a Linear project or initiative to a project workspace,
// replacing any existing attachment.
//
// Linking is optional: a project workspace is fully usable without it, and
// the workspaces nested inside one are unaffected either way. This exists so
// that a project that *does* correspond to something in Linear can carry the
// reference where `arat ls` and CLAUDE.md can surface it.
//
// Errors with ErrInvalidInput when the target is a task workspace — a task
// attaches to an issue via AttachTicket instead.
func (s *Service) LinkLinear(ctx context.Context, opts LinkOptions) (*Workspace, error) {
	if !ValidLinearKind(opts.Linear.Kind) {
		return nil, fmt.Errorf("%w: linear kind %q must be %q or %q", ErrInvalidInput, opts.Linear.Kind, LinearKindProject, LinearKindInitiative)
	}
	if opts.Linear.ID == "" {
		return nil, fmt.Errorf("%w: linear id is required", ErrInvalidInput)
	}

	ws, err := s.Get(ctx, opts.Ref)
	if err != nil {
		return nil, err
	}
	if !ws.IsProject() {
		return nil, fmt.Errorf("%w: %s is a task workspace — it attaches to an issue (`arat attach <ticket>`), not to a Linear project", ErrInvalidInput, ws.Ref)
	}

	ref := opts.Linear
	if err := writeMeta(ws.Path, Meta{Kind: KindProject, Linear: &ref}); err != nil {
		return nil, err
	}
	ws.Linear = &ref
	return ws, nil
}

// UnlinkLinear removes a project workspace's Linear attachment. Unlinking an
// already-unlinked project is a no-op rather than an error, so the command is
// safe to re-run.
func (s *Service) UnlinkLinear(ctx context.Context, ref string) (*Workspace, error) {
	ws, err := s.Get(ctx, ref)
	if err != nil {
		return nil, err
	}
	if !ws.IsProject() {
		return nil, fmt.Errorf("%w: %s is a task workspace, which has no Linear project link", ErrInvalidInput, ws.Ref)
	}
	if ws.Linear == nil {
		return ws, nil
	}
	if err := writeMeta(ws.Path, Meta{Kind: KindProject}); err != nil {
		return nil, err
	}
	ws.Linear = nil
	return ws, nil
}
