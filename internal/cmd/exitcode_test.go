package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/data-pata/arat/internal/config"
	"github.com/data-pata/arat/internal/git"
	"github.com/data-pata/arat/internal/linear"
	"github.com/stretchr/testify/assert"
)

// Exit 6 is the one code wrapper scripts may treat as retryable, so it must
// fire exactly when git or linear failed and never for arat's own errors.
func TestExitExternal_firesOnlyForToolFailures(t *testing.T) {
	t.Run("git failure exits external", func(t *testing.T) {
		svc := &fakeService{removeErr: fmt.Errorf("teardown: %w", git.ErrCmd)}
		r := run(t, []string{"rm", "x"}, nil, svc)
		assert.Equal(t, ExitExternal, r.exit)
	})
	t.Run("linear failure exits external", func(t *testing.T) {
		svc := &fakeService{newErr: fmt.Errorf("attach: %w", linear.ErrCmd)}
		r := run(t, []string{"new", "x", "--no-ticket"}, nil, svc)
		assert.Equal(t, ExitExternal, r.exit)
	})
	t.Run("unclassified failure exits generic", func(t *testing.T) {
		svc := &fakeService{newErr: errors.New("mkdir: permission denied")}
		r := run(t, []string{"new", "x", "--no-ticket"}, nil, svc)
		assert.Equal(t, ExitGeneric, r.exit)
	})
}

// Service construction failing (config produced an unusable service) must
// exit through the ordinary error path with the config code, not short-
// circuit the process from inside the wiring factory.
func TestServiceConstructionFailureExitsConfig(t *testing.T) {
	deps := Deps{
		Stdout: io.Discard, Stderr: io.Discard,
		NewConfig:  func(string) (*config.Config, error) { return &config.Config{Root: "/tmp", BranchPrefix: "ps"}, nil },
		NewService: func(*config.Config) (Service, error) { return nil, errors.New("workspace: Git is required") },
	}
	assert.Equal(t, ExitConfig, Execute(context.Background(), deps, []string{"ls"}))
}

// A run whose root context was cancelled reports the interruption (128+INT)
// rather than whichever downstream failure the cancellation caused.
func TestInterruptedRunExits130(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc := &fakeService{listErr: context.Canceled}
	deps := Deps{
		Stdout: io.Discard, Stderr: io.Discard,
		NewConfig:  func(string) (*config.Config, error) { return &config.Config{Root: "/tmp", BranchPrefix: "ps"}, nil },
		NewService: func(*config.Config) (Service, error) { return svc, nil },
	}
	assert.Equal(t, ExitInterrupted, Execute(ctx, deps, []string{"ls"}))
}
