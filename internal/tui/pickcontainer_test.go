package tui

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/data-pata/arat/internal/linear"
)

func TestContainerItem_rendersKindAndURL(t *testing.T) {
	i := containerItem(linear.Container{
		Kind: "initiative", ID: "slug-i", Name: "Payments 2026",
		URL: "https://linear.app/o/initiative/slug-i",
	})
	assert.Equal(t, "Payments 2026", i.Title())
	assert.Contains(t, i.Description(), "initiative")
	assert.Contains(t, i.Description(), "https://linear.app/o/initiative/slug-i")
}

func TestContainerItem_filterMatchesKind(t *testing.T) {
	// Typing "initiative" in the filter should narrow to initiatives even
	// though the visible title is just the name.
	i := containerItem(linear.Container{Kind: "initiative", Name: "Payments 2026"})
	assert.Contains(t, i.FilterValue(), "Payments 2026")
	assert.Contains(t, i.FilterValue(), "initiative")
}
