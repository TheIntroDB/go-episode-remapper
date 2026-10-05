package remap

import (
	"strconv"
	"strings"
	"unicode"
)

// Episode-title normalisation, shared by both mapping directions.
//
// WHY THIS EXISTS. TitlesMatch compared two titles as strings, and that fails on two
// shapes that are extremely common in real catalogues -- measured live, both were
// causing production 404s:
//
//  1. TMDB prefixes the episode number: "Episode 6 - Mum is always mum" while TVDB
//     carries just "Mum is always mum" (or its translation). The number is noise in
//     the title and has to come off before comparing.
//
//  2. Both sides sometimes carry ONLY an episode number, in different scripts and
//     numeral systems: TMDB "Episode 31" vs TVDB "第三十一集" (Chinese: episode 31).
//     These are the same episode and no string comparison reaches it, but the NUMBERS
//     they encode are equal -- which is evidence, not a guess. Measured: TVDB series
//     475408 has five episodes all aired 2026-03-30 named 第二十七集..第三十一集, so
//     the ambiguity refusal could not resolve s1e31 even though 第三十一集 is
//     literally "episode 31" sitting at s1e31.

// episodePrefixes are the leading markers a catalogue puts in front of an episode
// number. Matched case-insensitively, longest first.
var episodePrefixes = []string{"episode", "episodio", "épisode", "folge", "episod", "capítulo", "capitulo", "ep", "e"}

// stripEpisodePrefix removes a leading episode-number marker from a title, returning
// the remaining title text. "Episode 6 - Mum is always mum" -> "Mum is always mum".
//
// It only strips when a NUMBER actually follows the marker, so a genuine title that
// happens to begin with the letters "Ep" (say "Epsilon") is left alone.
func stripEpisodePrefix(title string) string {
	t := strings.TrimSpace(title)
	lower := strings.ToLower(t)

	for _, p := range episodePrefixes {
		if !strings.HasPrefix(lower, p) {
			continue
		}
		rest := strings.TrimSpace(t[len(p):])
		// Require a digit or a CJK numeral straight away; otherwise this is not an
		// episode-number prefix at all.
		if rest == "" || !isNumeralStart(rest) {
			continue
		}
		// Consume the number itself, then any separator.
		i := 0
		for i < len(rest) && !isSeparator(rune(rest[i])) {
			i++
		}
		after := strings.TrimSpace(rest[i:])
		after = strings.TrimLeft(after, "-–—:.·|")
		after = strings.TrimSpace(after)
		// "Episode 31" alone leaves nothing; that is the ordinal-only case, handled by
		// episodeOrdinalFromTitle, and stripping to empty must not happen here or the
		// caller would compare empty strings.
		if after == "" {
			return t
		}
		return after
	}
	return t
}

func isNumeralStart(s string) bool {
	r := []rune(s)[0]
	return unicode.IsDigit(r) || isCJKNumeral(r)
}

func isSeparator(r rune) bool {
	if unicode.IsDigit(r) || isCJKNumeral(r) || r == ' ' || r == '\t' {
		return false
	}
	return true
}

func isCJKNumeral(r rune) bool {
	return strings.ContainsRune("零一二三四五六七八九十百廿卅兩两", r)
}

// episodeOrdinalFromTitle returns the episode number encoded by a title that is ONLY
// an episode number, and whether the title was of that shape.
//
// Recognised: "Episode 31", "Ep 31", "EP31", "第31集", "第31話", "第三十一集",
// "31". A title with real words in it ("Episode 6 - Mum is always mum", "Pilot")
// returns false, because there the number is not the only evidence.
func episodeOrdinalFromTitle(title string) (int, bool) {
	t := strings.TrimSpace(title)
	if t == "" {
		return 0, false
	}

	// Peel an ASCII "Episode"-style prefix if present.
	lower := strings.ToLower(t)
	for _, p := range episodePrefixes {
		if strings.HasPrefix(lower, p) {
			rest := strings.TrimSpace(t[len(p):])
			if rest != "" && isNumeralStart(rest) {
				t = rest
			}
			break
		}
	}

	// Peel CJK container characters: 第...集 / 第...話 / 第...话.
	t = strings.TrimPrefix(t, "第")
	t = strings.TrimSuffix(t, "集")
	t = strings.TrimSuffix(t, "話")
	t = strings.TrimSuffix(t, "话")
	t = strings.TrimSpace(t)
	if t == "" {
		return 0, false
	}

	// ASCII digits, possibly zero-padded ("E031").
	if n, err := strconv.Atoi(t); err == nil {
		return n, true
	}

	// Chinese numerals, 1..99 -- the range episode numbers occupy.
	if n, ok := chineseNumeral(t); ok {
		return n, true
	}
	return 0, false
}

// chineseNumeral parses 一..九十九 (and the 廿/卅 shorthand for 20/30).
//
// Only 1..99 is supported: that covers every realistic episode number, and a partial
// parser that silently mis-reads a larger number would be worse than returning false.
func chineseNumeral(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	digits := map[rune]int{'零': 0, '一': 1, '二': 2, '兩': 2, '两': 2, '三': 3, '四': 4, '五': 5,
		'六': 6, '七': 7, '八': 8, '九': 9}

	runes := []rune(s)
	tens := 0
	ones := 0
	sawAny := false

	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case r == '廿':
			tens, sawAny = 2, true
		case r == '卅':
			tens, sawAny = 3, true
		case r == '十':
			if tens == 0 {
				tens = 1
			}
			sawAny = true
		case r == '百':
			// Out of the supported range; refuse rather than mis-report.
			return 0, false
		default:
			d, ok := digits[r]
			if !ok {
				return 0, false
			}
			// A digit before a 十 multiplies the tens; otherwise it is a unit.
			if i+1 < len(runes) && runes[i+1] == '十' {
				tens = d
			} else {
				ones = d
			}
			sawAny = true
		}
	}
	if !sawAny {
		return 0, false
	}
	n := tens*10 + ones
	// 十 alone is 10; a bare digit is itself; but 一十 would be a malformed 10.
	if tens == 0 && ones == 0 {
		return 0, false
	}
	if tens > 9 {
		return 0, false
	}
	return n, true
}

// TitlesMatchAny reports whether the wanted title matches the episode's canonical name
// or any of its translations.
//
// TVDB ships per-language episode names and TMDB carries whichever language it has, so
// comparing the canonical name alone misses correct matches in both directions.
// Measured: TVDB series 388150 s2e6 is "La mamma è sempre la mamma" (Italian) while
// TMDB calls it "Episode 6 - Mum is always mum" -- the same episode, and TVDB's English
// translation is exactly "Mum is always mum".
func TitlesMatchAny(names []string, want string) bool {
	if strings.TrimSpace(want) == "" {
		return false
	}
	for _, n := range names {
		if TitlesMatch(want, n) {
			return true
		}
	}
	return false
}