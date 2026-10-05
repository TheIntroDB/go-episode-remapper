package remap

import "testing"

// Both of these shapes were causing production 404s, measured live.
func TestTitlesMatchEpisodeOrdinals(t *testing.T) {
	cases := []struct {
		name string
		want string
		got  string
		exp  bool
	}{
		// Case 1, measured: TVDB series 475408 s1e31 is 第三十一集, TMDB calls it
		// "Episode 31". Same episode, different script and numeral system.
		{"Chinese numeral vs Episode N", "Episode 31", "第三十一集", true},
		{"Arabic in CJK container", "Episode 31", "第31集", true},
		{"traditional 話 container", "Episode 31", "第31話", true},
		{"bare number", "Episode 31", "31", true},
		{"zero padded", "Episode 31", "E031", true},
		{"ten", "Episode 10", "第十集", true},
		{"eleven", "Episode 11", "第十一集", true},
		{"twenty", "Episode 20", "第二十集", true},
		{"ninety nine", "Episode 99", "第九十九集", true},

		// MUST NOT match: a different episode number is not a match.
		{"different number", "Episode 31", "第二十七集", false},
		{"off by one", "Episode 31", "Episode 30", false},

		// Case 3, measured: TMDB prefixes the number, TVDB does not.
		{"Episode N prefix stripped", "Episode 6 - Mum is always mum", "Mum is always mum", true},
		{"prefix with colon", "Episode 6: Mum is always mum", "Mum is always mum", true},

		// MUST NOT match: the title part still differs, so this needs TVDB's
		// translations (covered by TestTitlesMatchAnyUsesTranslations below).
		{"prefix stripped but title untranslated", "Episode 6 - Mum is always mum", "La mamma è sempre la mamma", false},

		// A real title that merely begins with the letters of a marker must not be
		// mangled: only a number following the marker counts.
		{"Epsilon is not an episode prefix", "Epsilon", "Ep 5", false},
		{"real title vs ordinal", "Pilot", "Episode 5", false},

		// Pre-existing behaviour must be preserved.
		{"The End is not The Beginning of the End", "The End", "The Beginning of the End", false},
		{"colon prefix still tolerated", "Show: The End", "The End", true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := TitlesMatch(c.want, c.got); got != c.exp {
				t.Fatalf("TitlesMatch(%q, %q) = %v, want %v", c.want, c.got, got, c.exp)
			}
		})
	}
}

// The full case 3: the prefix comes off, and TVDB's ENGLISH TRANSLATION supplies the
// matching title. Comparing the canonical name alone cannot reach this.
func TestTitlesMatchAnyUsesTranslations(t *testing.T) {
	tvdbNames := []string{"La mamma è sempre la mamma", "Mum is always mum"}

	if !TitlesMatchAny(tvdbNames, "Episode 6 - Mum is always mum") {
		t.Fatal("expected a match via the episode's English translation")
	}
	if TitlesMatchAny(tvdbNames, "Episode 6 - Nothing like this exists") {
		t.Fatal("an unrelated title must not match")
	}
	if TitlesMatchAny(nil, "Episode 6") {
		t.Fatal("no names at all must not match")
	}
	if TitlesMatchAny(tvdbNames, "   ") {
		t.Fatal("a blank wanted name must not match")
	}
}

func TestChineseNumeralRange(t *testing.T) {
	// Beyond 99 the parser must refuse rather than mis-report, because a wrong
	// number here would silently pick the wrong episode.
	for _, s := range []string{"一百", "第一百集", "abc", "", "二十一世纪"} {
		if n, ok := episodeOrdinalFromTitle(s); ok {
			t.Fatalf("episodeOrdinalFromTitle(%q) = %d, true; want false", s, n)
		}
	}
}