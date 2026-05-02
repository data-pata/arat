package shell

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInit_supported(t *testing.T) {
	for _, sh := range []string{"bash", "zsh", "fish"} {
		t.Run(sh, func(t *testing.T) {
			out, err := Init(sh)
			require.NoError(t, err)
			assert.Contains(t, out, "arat go")
			assert.Contains(t, out, "command arat")
			assert.Contains(t, out, "arat completion "+sh)
		})
	}
}

func TestInit_shellNameSubstituted(t *testing.T) {
	bash, _ := Init("bash")
	zsh, _ := Init("zsh")
	assert.Contains(t, bash, "arat init bash")
	assert.Contains(t, zsh, "arat init zsh")
	assert.NotEqual(t, bash, zsh, "bash and zsh outputs should differ in the SHELL marker")
}

func TestInit_unknown(t *testing.T) {
	_, err := Init("nu")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported shell")
}
