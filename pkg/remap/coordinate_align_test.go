package remap

import (
	"testing"

	"github.com/Pasithea0/go-tmdb-episodes-fix/pkg/tmdb"
	"github.com/Pasithea0/go-tmdb-episodes-fix/pkg/tvdb"
)

// seasonsNumberAlike is the gate on coordinate identity: it decides whether the
// coordinate match is evidence or an assumption. Each clause needs a case that shows it
// actually bites, or the rule silently degenerates into allowCoordinateIdentityFallback.
func TestSeasonsNumberAlike(t *testing.T) {
	// A clean 36-episode season, dates agreeing, TVDB a day off as it often is.
	build := func(n int, tmdbDates, tvdbDates []string) ([]tmdb.SeasonEpisode, []tvdb.EpisodeBaseRecord) {
		var te []tmdb.SeasonEpisode
		var ve []tvdb.EpisodeBaseRecord
		for i := 1; i <= n; i++ {
			d := ""
			if i-1 < len(tmdbDates) {
				d = tmdbDates[i-1]
			}
			vd := ""
			if i-1 < len(tvdbDates) {
				vd = tvdbDates[i-1]
			}
			te = append(te, tmdb.SeasonEpisode{EpisodeNumber: i, AirDate: d, ID: i})
			ve = append(ve, tvdb.EpisodeBaseRecord{Number: i, Aired: vd, ID: int64(i)})
		}
		return te, ve
	}

	t.Run("clean alignment passes", func(t *testing.T) {
		te, ve := build(36, nil, nil)
		// Give both sides agreeing dates every 6 days.
		for i := range te {
			te[i].AirDate = "2008-01-" + pad(i+1)
			ve[i].Aired = "2008-01-" + pad(i+1)
		}
		if !seasonsNumberAlike(te, ve) {
			t.Fatal("a clean 36-for-36 alignment with agreeing dates must pass")
		}
	})

	t.Run("count mismatch fails", func(t *testing.T) {
		te, ve := build(36, nil, nil)
		if seasonsNumberAlike(te, ve[:35]) {
			t.Fatal("different episode counts must not pass")
		}
	})

	t.Run("gap in the numbering fails", func(t *testing.T) {
		te, ve := build(10, nil, nil)
		ve[4].Number = 99 // no longer a contiguous 1..N run
		if seasonsNumberAlike(te, ve) {
			t.Fatal("a gap in TVDB numbering must not pass")
		}
	})

	t.Run("a shifted season fails even though the counts match", func(t *testing.T) {
		// Same count and same numbering, but every episode is a week off: this is the
		// off-by-one-in-content case that equal counts alone would happily accept.
		te, ve := build(10, nil, nil)
		for i := range te {
			te[i].AirDate = "2020-03-01"
			ve[i].Aired = "2020-03-15"
		}
		if seasonsNumberAlike(te, ve) {
			t.Fatal("a congruent shift in dates must not pass")
		}
	})

	t.Run("null TMDB dates pass on numbering alone -- the Dork Hunters case", func(t *testing.T) {
		// TMDB series 54487 has air_date NULL for every episode but the first, so
		// there is nothing to contradict the numbering claim. Counts and contiguity
		// are the only evidence available, and they are enough.
		te, ve := build(36, nil, nil)
		ve[0].Aired = "2008-11-23"
		if !seasonsNumberAlike(te, ve) {
			t.Fatal("with no comparable dates, clean counts and numbering must pass")
		}
	})

	t.Run("a single stale date does not veto an otherwise clean season", func(t *testing.T) {
		te, ve := build(36, nil, nil)
		for i := range te {
			te[i].AirDate = "2008-01-" + pad(i+1)
			ve[i].Aired = "2008-01-" + pad(i+1)
		}
		ve[17].Aired = "1999-01-01" // one bad row
		if !seasonsNumberAlike(te, ve) {
			t.Fatal("one stale date must not veto a clean alignment")
		}
	})

	t.Run("zero/negative numbering fails", func(t *testing.T) {
		te, ve := build(10, nil, nil)
		ve[3].Number = 0
		if seasonsNumberAlike(te, ve) {
			t.Fatal("a zero episode number must not pass")
		}
	})
}

func pad(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}