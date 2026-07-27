package workspace

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSlugFromTitle(t *testing.T) {
	tests := []struct {
		name   string
		title  string
		ticket string
		want   string
	}{
		{"plain", "Fix postal lookup race", "", "fix-postal-lookup-race"},
		{"punctuation collapses", "BE: tighten validation (v2)!", "", "be-tighten-validation-v2"},
		{"leading ticket id stripped", "REX-666: Fix postal race", "rex-666", "fix-postal-race"},
		{"ticket only stripped at start", "Fix REX-666 postal race", "rex-666", "fix-rex-666-postal-race"},
		{"nordic transliteration", "Lägg till kvittoflöde", "", "lagg-till-kvittoflode"},
		{"empty title", "   ", "", ""},
		{"symbols only", "!!! ???", "", ""},
		{"capped at word boundary", "A very long issue title that keeps going and going far beyond the cap", "",
			"a-very-long-issue-title-that-keeps"},
		{"no leading or trailing hyphens", "  --Fix thing--  ", "", "fix-thing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SlugFromTitle(tt.title, tt.ticket)
			assert.Equal(t, tt.want, got)
			if got != "" {
				assert.Regexp(t, `^[a-z0-9]+(?:-[a-z0-9]+)*$`, got, "slug must satisfy the short-name rules")
				assert.LessOrEqual(t, len(got), slugMaxLen)
			}
		})
	}
}
