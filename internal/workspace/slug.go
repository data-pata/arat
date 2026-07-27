package workspace

import "strings"

// slugMaxLen caps derived short names well under shortNameMaxLen: issue
// titles run long, and the slug is repeated in the directory name and every
// branch name.
const slugMaxLen = 40

// slugTranslit maps letters that would otherwise be dropped wholesale to
// their ASCII look-alikes. Deliberately small: common Nordic characters plus
// a few accents, not a general transliteration scheme.
var slugTranslit = map[rune]string{
	'å': "a", 'ä': "a", 'ö': "o", 'ø': "o", 'æ': "ae",
	'é': "e", 'è': "e", 'ê': "e", 'ü': "u", 'ß': "ss",
}

// SlugFromTitle derives a workspace short name from an issue title, for
// `arat new` without a name argument. Lowercases, drops a leading occurrence
// of the ticket id itself (titles like "REX-666: Fix postal race" would
// otherwise double the id, since the directory name prefixes it again),
// collapses every non-alphanumeric run to a single hyphen, and caps the
// result at slugMaxLen on a word boundary. Returns "" when nothing usable
// remains; callers treat that as "no default".
func SlugFromTitle(title, ticket string) string {
	s := strings.ToLower(strings.TrimSpace(title))
	if ticket != "" {
		if rest, ok := strings.CutPrefix(s, strings.ToLower(ticket)); ok {
			s = rest
		}
	}

	var b strings.Builder
	pendingHyphen := false
	for _, r := range s {
		if t, ok := slugTranslit[r]; ok {
			if pendingHyphen && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteString(t)
			pendingHyphen = false
			continue
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if pendingHyphen && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			pendingHyphen = false
			continue
		}
		pendingHyphen = true
	}
	out := b.String()

	if len(out) > slugMaxLen {
		cut := out[:slugMaxLen]
		if i := strings.LastIndexByte(cut, '-'); i > 0 {
			cut = cut[:i]
		}
		out = strings.TrimRight(cut, "-")
	}
	return out
}
