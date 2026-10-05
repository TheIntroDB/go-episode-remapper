package remap

import (
	"context"
	"strings"

	"github.com/Pasithea0/go-tmdb-episodes-fix/pkg/tmdb"
	"github.com/Pasithea0/go-tmdb-episodes-fix/pkg/tvdb"
)

// verifiedCoordinateMatch returns the TVDB episode sitting at the caller's own
// coordinates -- but ONLY when the two seasons can be SHOWN to number the same episodes
// the same way. It is evidence, not the assumption that allowCoordinateIdentityFallback
// makes, so it is always on and does not need opting into.
//
// WHY IT EXISTS. Two measured production failures cannot be resolved by comparing names
// or dates at all:
//
//   - TMDB series 54487 (Dork Hunters) has air_date NULL for every episode but the
//     first, so no air-date candidate can ever be found, and the one title that differs
//     is a spelling variant ("Wild Dork Kingdom" vs TVDB "Wild Dorkingdom"). The two
//     seasons are a clean 36-for-36 with identical numbering.
//   - TMDB series 110283 s2e6 ties with s2e5 on air date, and TVDB carries no
//     translation that matches TMDB's English title, so the tie is unbreakable by
//     wording. The candidate at the caller's own coordinate is the answer.
//
// WHY IT IS NOT JUST allowCoordinateIdentityFallback. That flag asserts the two schemes
// number identically for every show, which is false exactly where this mapper is needed
// (TMDB Futurama S6E1 is "Bender's Big Score (1)" while TVDB default S6E1 is "Rebirth").
// This function makes the same move only after checking that it holds for this season,
// so a show that diverges simply keeps its existing error.
//
// The check is deliberately conservative -- all of:
//   - the season exists on both sides, and
//   - the episode counts are equal, and
//   - both sides number them contiguously 1..N, and
//   - where BOTH sides have a date, they agree within the usual one-day tolerance for a
//     large majority (a genuine off-by-one shift shows up here),
//
// and then the episode AT the caller's coordinate must actually exist.
func (m *Mapper) verifiedCoordinateMatch(
	ctx context.Context,
	tvdbSeriesID, tmdbSeriesID int,
	normalizedOrder string,
	season, episode int,
	lookupUsed string,
) *TmdbToTvdbResult {
	tmdbEps, err := m.tmdb.GetSeasonEpisodes(ctx, tmdbSeriesID, season)
	if err != nil || len(tmdbEps) == 0 {
		return nil
	}

	tvdbEps := m.fetchTvdbSeason(ctx, tvdbSeriesID, normalizedOrder, season)
	if len(tvdbEps) == 0 {
		return nil
	}

	if !seasonsNumberAlike(tmdbEps, tvdbEps) {
		return nil
	}

	for i := range tvdbEps {
		if tvdbEps[i].Number == episode {
			return &TmdbToTvdbResult{
				InputTMDBSeriesID: tmdbSeriesID,
				InputSeason:       season,
				InputEpisode:      episode,
				TVDBSeriesID:      tvdbSeriesID,
				TVDBEpisodeID:     tvdbEps[i].ID,
				TVDBSeason:        tvdbEps[i].SeasonNumber,
				TVDBEpisode:       tvdbEps[i].Number,
				TVDBEpisodeName:   tvdbEps[i].Name,
				MatchedBy:         "verified_coordinate",
				TVDBSeriesLookup:  lookupUsed,
			}
		}
	}
	return nil
}

// fetchTvdbSeason reads a whole TVDB season, paging at the API's page size.
func (m *Mapper) fetchTvdbSeason(ctx context.Context, tvdbSeriesID int, order string, season int) []tvdb.EpisodeBaseRecord {
	var out []tvdb.EpisodeBaseRecord
	s := season
	for page := 0; page < m.maxTVDBPages; page++ {
		eps, err := m.tvdb.GetSeriesEpisodes(ctx, tvdbSeriesID, order, page, &s, nil, nil)
		if err != nil || len(eps) == 0 {
			break
		}
		out = append(out, eps...)
	}
	return out
}

// seasonsNumberAlike reports whether two season listings are the same season, numbered
// the same way. See verifiedCoordinateMatch for why each clause is there.
func seasonsNumberAlike(tmdbEps []tmdb.SeasonEpisode, tvdbEps []tvdb.EpisodeBaseRecord) bool {
	if len(tmdbEps) != len(tvdbEps) {
		return false
	}

	// Both sides must number contiguously 1..N. A gap on either side means the season
	// is not a plain numbered run, and the coordinate claim stops being safe.
	want := make(map[int]bool, len(tmdbEps))
	for _, e := range tmdbEps {
		if e.EpisodeNumber <= 0 {
			return false
		}
		want[e.EpisodeNumber] = true
	}
	for i := 1; i <= len(tmdbEps); i++ {
		if !want[i] {
			return false
		}
	}
	have := make(map[int]bool, len(tvdbEps))
	for _, e := range tvdbEps {
		if e.Number <= 0 {
			return false
		}
		have[e.Number] = true
	}
	for i := 1; i <= len(tvdbEps); i++ {
		if !have[i] {
			return false
		}
	}

	// Where both sides carry a date, they must mostly agree. This is what catches a
	// genuine numbering shift, which equal counts alone would happily accept. TVDB
	// often records the broadcast date a day off TMDB's, so the comparison is the
	// usual one-day tolerance.
	comparable, agreeing := 0, 0
	for _, t := range tmdbEps {
		if strings.TrimSpace(t.AirDate) == "" {
			continue
		}
		for _, v := range tvdbEps {
			if v.Number != t.EpisodeNumber || strings.TrimSpace(v.Aired) == "" {
				continue
			}
			comparable++
			if datesWithinOneDay(v.Aired, t.AirDate) {
				agreeing++
			}
			break
		}
	}
	// With no comparable dates at all there is nothing to contradict the numbering
	// claim, and the counts plus contiguity stand as the evidence -- that is exactly
	// the Dork Hunters case, where TMDB has null dates throughout.
	if comparable == 0 {
		return true
	}
	// A majority is required, not unanimity: a single stale date should not veto an
	// otherwise clean alignment, but a shifted season fails this easily.
	return agreeing*100/comparable >= 80
}