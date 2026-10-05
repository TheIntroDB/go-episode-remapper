package remap

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Pasithea0/go-tmdb-episodes-fix/pkg/tmdb"
	"github.com/Pasithea0/go-tmdb-episodes-fix/pkg/tvdb"
)

// This file covers the tmdb2tvdb direction. Every fixture value below was read
// from live TMDB and TVDB on 2026-09-25 and is reproduced verbatim, because the
// whole point of this direction is *which real episode* a coordinate means.
//
//	TMDB 615 Futurama (external_ids tvdb_id=73871)
//	  S1E1 id 35076  "Space Pilot 3000"       1999-03-28
//	  S6E1 id 35077  "Rebirth"                2010-06-24
//	  S7E1 id 35107  "The Bots and the Bees"  2012-06-20
//	TVDB 73871 "default"
//	  s1e1 id 131174   "Space Pilot 3000"       1999-03-28
//	  s6e1 id 1051911  "Rebirth"                2010-06-24
//	  s7e1 id 4319164  "The Bots and the Bees"  2012-06-20
//	TVDB 73871 "alternate"
//	  s6e1 id 8234611  "Bender's Big Score (1)" 2008-03-23
//	  s7e1 id 1051911  "Rebirth"                2010-06-24
//
// The critical property to keep in mind while reading the fixtures: TVDB
// "default" s6e1 and "alternate" s6e1 carry the SAME numbers and are DIFFERENT
// episodes, and the same episode ("Rebirth") lives at default s6e1 but at
// alternate s7e1.

// --- fakes ------------------------------------------------------------------
//
// The mapping rules are the part that silently hands back the wrong episode, so
// they are tested directly against fixtures rather than through a live client.

type fakeTVDB struct {
	// seriesByTMDB models FindSeriesByTMDBID, including TVDB's bare-number
	// remote-id search, which returns a wrong series when a number collides.
	seriesByTMDB map[int]*tvdb.SeriesBaseRecord
	// seriesByRemote models SearchSeriesByRemoteID, keyed by the raw search term.
	seriesByRemote map[string]*tvdb.SeriesBaseRecord
	// episodes maps series id -> order -> full episode list.
	episodes map[int]map[string][]tvdb.EpisodeBaseRecord
	// episodesByID indexes the same records by episode id.
	episodesByID map[int64]tvdb.EpisodeBaseRecord
	// seriesByID backs GetSeriesExtended, used to read a series name when
	// corroborating a link.
	seriesByID map[int]*tvdb.SeriesBaseRecord
	// aliasesByID backs the aliases on GetSeriesExtended. TVDB files many shows
	// under their original-language title and lists the English one here, so a
	// corroboration that reads only the canonical name rejects correct links.
	aliasesByID map[int][]tvdb.Alias
	// seriesRemoteIDs backs the series-level remote ids on GetSeriesExtended,
	// which is how the tvdb2tmdb direction learns the TMDB series id.
	seriesRemoteIDs map[int][]tvdb.RemoteID
	// episodeRemoteIDs backs the remote ids on GetEpisodeExtended, which is how
	// that direction learns the TMDB episode id. TVDB's values here go stale --
	// the Futurama specials point at TMDB ids that no longer exist -- so the
	// path has to tolerate a dead one.
	episodeRemoteIDs map[int64][]tvdb.RemoteID

	lookupUsed string

	// forbidAirDateFilter makes any use of TVDB's server-side airDate query
	// parameter fail loudly. That parameter is unreliable and must never decide
	// an episode -- measured live 2026-09-25 on Futurama (73871, order=default):
	// airDate=2007-11-27 returned ONE of the two episodes that aired that day,
	// airDate=2008-06-30 returned NONE though s0e3 aired exactly then, and
	// airDate=2008-06-24 returned s0e3 (aired 2008-06-30) anyway. Candidates must
	// be computed locally from a complete list instead.
	forbidAirDateFilter bool
}

func (f *fakeTVDB) SearchSeriesByRemoteID(_ context.Context, remoteID string) (*tvdb.SeriesBaseRecord, error) {
	if s, ok := f.seriesByRemote[remoteID]; ok {
		return s, nil
	}
	return nil, fmt.Errorf("no tvdb series for remote id %q", remoteID)
}

func (f *fakeTVDB) FindSeriesByIMDbID(_ context.Context, imdbID string) (*tvdb.SeriesBaseRecord, string, error) {
	if s, ok := f.seriesByRemote["imdb:"+imdbID]; ok {
		return s, "imdb", nil
	}
	return nil, "", fmt.Errorf("no tvdb series for imdb id %q", imdbID)
}

func (f *fakeTVDB) FindSeriesByTMDBID(_ context.Context, tmdbID int) (*tvdb.SeriesBaseRecord, string, error) {
	if s, ok := f.seriesByTMDB[tmdbID]; ok {
		return s, f.lookupUsed, nil
	}
	return nil, "", fmt.Errorf("no tvdb series for tmdb id %d", tmdbID)
}

func (f *fakeTVDB) GetSeriesExtended(_ context.Context, seriesID int) (*tvdb.SeriesExtendedRecord, error) {
	if s, ok := f.seriesByID[seriesID]; ok {
		return &tvdb.SeriesExtendedRecord{
			ID:        s.ID,
			Name:      s.Name,
			Aliases:   f.aliasesByID[seriesID],
			RemoteIDs: f.seriesRemoteIDs[seriesID],
		}, nil
	}
	return nil, fmt.Errorf("no tvdb series %d", seriesID)
}

func (f *fakeTVDB) GetEpisodeExtended(_ context.Context, id int64) (*tvdb.EpisodeExtendedRecord, error) {
	if ep, ok := f.episodesByID[id]; ok {
		return &tvdb.EpisodeExtendedRecord{
			ID:           ep.ID,
			Name:         ep.Name,
			Aired:        ep.Aired,
			SeriesID:     ep.SeriesID,
			SeasonNumber: ep.SeasonNumber,
			Number:       ep.Number,
			RemoteIDs:    f.episodeRemoteIDs[id],
		}, nil
	}
	return nil, fmt.Errorf("no tvdb episode %d", id)
}

// GetSeriesEpisodes applies the season/episode/airDate filters and paginates at
// 500, matching TVDB's real page size (measured: One Piece 500/500/242).
func (f *fakeTVDB) GetSeriesEpisodes(
	_ context.Context, seriesID int, seasonType string, page int,
	season *int, episodeNumber *int, airDate *string,
) ([]tvdb.EpisodeBaseRecord, error) {
	orders, ok := f.episodes[seriesID]
	if !ok {
		return nil, fmt.Errorf("no episodes for tvdb series %d", seriesID)
	}
	if airDate != nil && f.forbidAirDateFilter {
		return nil, fmt.Errorf(
			"GetSeriesEpisodes called with an airDate filter (%q): TVDB's airDate parameter is unreliable and must not be used to decide an episode",
			*airDate)
	}
	all := orders[seasonType]

	var filtered []tvdb.EpisodeBaseRecord
	for _, ep := range all {
		if season != nil && ep.SeasonNumber != *season {
			continue
		}
		if episodeNumber != nil && ep.Number != *episodeNumber {
			continue
		}
		if airDate != nil && ep.Aired != *airDate {
			continue
		}
		filtered = append(filtered, ep)
	}

	const pageSize = 500
	start := page * pageSize
	if start >= len(filtered) {
		return nil, nil
	}
	end := start + pageSize
	if end > len(filtered) {
		end = len(filtered)
	}
	return filtered[start:end], nil
}

type fakeTMDB struct {
	details map[string]*tmdb.EpisodeDetails // key "series/season/episode"
	names   map[int]string                  // series id -> series name
	tvdbID  map[int]int                     // external_ids route
	// seasons backs GetSeasonEpisodes, keyed "series/season".
	seasons map[string][]tmdb.SeasonEpisode
	// episodesByID backs GetEpisodeByID.
	episodesByID map[int]*tmdb.EpisodeByID
	// episodeExternalIDs backs GetEpisodeExternalIDs, keyed "series/season/episode".
	// Values are the real ones TMDB reports (measured 2026-09-25) and each has
	// been confirmed against TVDB's own /episodes/{id}/extended.
	episodeExternalIDs map[string]*tmdb.EpisodeExternalIDs
}

func (f *fakeTMDB) GetEpisodeDetails(_ context.Context, tvID, season, episode int) (*tmdb.EpisodeDetails, error) {
	if d, ok := f.details[fmt.Sprintf("%d/%d/%d", tvID, season, episode)]; ok {
		return d, nil
	}
	return nil, fmt.Errorf("no tmdb episode %d s%de%d", tvID, season, episode)
}

// GetEpisodeByID models TMDB 404ing on an episode id that no longer exists,
// which is what TVDB's stale remote ids for the Futurama specials hit.
// GetEpisodeExternalIDs returns TMDB's cross-reference for an episode. An
// episode with no entry returns an EMPTY result and a nil error, not an error:
// TMDB genuinely does not carry a TVDB link for every episode, and the mapper is
// required to fall back to air date and name rather than to fail. Modelling this
// as "no link" rather than "not found" is what exercises that fallback.
func (f *fakeTMDB) GetEpisodeExternalIDs(_ context.Context, tvID, season, episode int) (*tmdb.EpisodeExternalIDs, error) {
	if ext, ok := f.episodeExternalIDs[fmt.Sprintf("%d/%d/%d", tvID, season, episode)]; ok {
		return ext, nil
	}
	return &tmdb.EpisodeExternalIDs{}, nil
}

func (f *fakeTMDB) GetEpisodeByID(_ context.Context, episodeID int) (*tmdb.EpisodeByID, error) {
	if ep, ok := f.episodesByID[episodeID]; ok {
		return ep, nil
	}
	return nil, fmt.Errorf("tmdb episode %d not found", episodeID)
}

func (f *fakeTMDB) GetTVDetails(_ context.Context, tvID int) (*tmdb.TVDetails, error) {
	name, ok := f.names[tvID]
	if !ok {
		return nil, fmt.Errorf("no tmdb series %d", tvID)
	}
	return &tmdb.TVDetails{Name: name, NumberOfSeasons: 1}, nil
}

// GetTvdbIDFromTmdbID models TMDB's external_ids route.
func (f *fakeTMDB) GetTvdbIDFromTmdbID(_ context.Context, tvID int) (int, error) {
	if id, ok := f.tvdbID[tvID]; ok {
		return id, nil
	}
	return 0, nil
}

func (f *fakeTMDB) GetEpisodeGroups(context.Context, int) ([]tmdb.EpisodeGroup, error) {
	return nil, nil
}
func (f *fakeTMDB) GetEpisodeGroup(context.Context, string) (*tmdb.EpisodeGroupDetail, error) {
	return nil, nil
}
func (f *fakeTMDB) GetSeasonEpisodes(_ context.Context, tvID int, season int) ([]tmdb.SeasonEpisode, error) {
	if eps, ok := f.seasons[fmt.Sprintf("%d/%d", tvID, season)]; ok {
		return eps, nil
	}
	// A season with no entry is absent, not empty: the real client 404s, and the
	// scan treats that as "skip this season".
	return nil, fmt.Errorf("no tmdb season %d for series %d", season, tvID)
}

// --- the Futurama fixture (real values) -------------------------------------

func futuramaTVDB() *fakeTVDB {
	return &fakeTVDB{
		seriesByTMDB: map[int]*tvdb.SeriesBaseRecord{
			615: {ID: 73871, Name: "Futurama"},
		},
		seriesByRemote: map[string]*tvdb.SeriesBaseRecord{
			"imdb:tt0149460": {ID: 73871, Name: "Futurama"},
			"tmdb-615":       {ID: 73871, Name: "Futurama"},
		},
		seriesByID: map[int]*tvdb.SeriesBaseRecord{
			73871: {ID: 73871, Name: "Futurama"},
		},
		episodes: map[int]map[string][]tvdb.EpisodeBaseRecord{
			73871: {
				"default": {
					{ID: 131174, Name: "Space Pilot 3000", Aired: "1999-03-28", SeriesID: 73871, SeasonNumber: 1, Number: 1},
					{ID: 1051911, Name: "Rebirth", Aired: "2010-06-24", SeriesID: 73871, SeasonNumber: 6, Number: 1},
					{ID: 4319164, Name: "The Bots and the Bees", Aired: "2012-06-20", SeriesID: 73871, SeasonNumber: 7, Number: 1},
					// Season 0 (specials), as TVDB 73871 "default" really lists them,
					// names carrying the series prefix.
					{ID: 389457, Name: "Everybody Loves Hypnotoad", Aired: "2007-11-27", SeriesID: 73871, SeasonNumber: 0, Number: 1},
					{ID: 342888, Name: "Futurama: Bender's Big Score", Aired: "2007-11-27", SeriesID: 73871, SeasonNumber: 0, Number: 2},
					{ID: 359477, Name: "Futurama: The Beast with a Billion Backs", Aired: "2008-06-30", SeriesID: 73871, SeasonNumber: 0, Number: 3},
					{ID: 372786, Name: "Futurama: The Lost Adventure", Aired: "2008-06-24", SeriesID: 73871, SeasonNumber: 0, Number: 4},
					{ID: 395236, Name: "Futurama: Bender's Game", Aired: "2008-11-03", SeriesID: 73871, SeasonNumber: 0, Number: 5},
					{ID: 427447, Name: "Futurama: Into the Wild Green Yonder", Aired: "2009-02-24", SeriesID: 73871, SeasonNumber: 0, Number: 6},
				},
				"alternate": {
					{ID: 8234611, Name: "Bender's Big Score (1)", Aired: "2008-03-23", SeriesID: 73871, SeasonNumber: 6, Number: 1},
					{ID: 1051911, Name: "Rebirth", Aired: "2010-06-24", SeriesID: 73871, SeasonNumber: 7, Number: 1},
				},
			},
		},
		episodesByID: map[int64]tvdb.EpisodeBaseRecord{
			// SeriesID matters: the TMDB->TVDB exact path verifies that the episode
			// TMDB points at belongs to the series it just resolved, so an entry
			// without SeriesID silently disables that path and hides regressions.
			131174:  {ID: 131174, Name: "Space Pilot 3000", Aired: "1999-03-28", SeriesID: 73871, SeasonNumber: 1, Number: 1},
			389457:  {ID: 389457, Name: "Everybody Loves Hypnotoad", Aired: "2007-11-27", SeriesID: 73871, SeasonNumber: 0, Number: 1},
			342888:  {ID: 342888, Name: "Futurama: Bender's Big Score", Aired: "2007-11-27", SeriesID: 73871, SeasonNumber: 0, Number: 2},
			359477:  {ID: 359477, Name: "Futurama: The Beast with a Billion Backs", Aired: "2008-06-30", SeriesID: 73871, SeasonNumber: 0, Number: 3},
			372786:  {ID: 372786, Name: "Futurama: The Lost Adventure", Aired: "2008-06-24", SeriesID: 73871, SeasonNumber: 0, Number: 4},
			395236:  {ID: 395236, Name: "Futurama: Bender's Game", Aired: "2008-11-03", SeriesID: 73871, SeasonNumber: 0, Number: 5},
			427447:  {ID: 427447, Name: "Futurama: Into the Wild Green Yonder", Aired: "2009-02-24", SeriesID: 73871, SeasonNumber: 0, Number: 6},
			1051911: {ID: 1051911, Name: "Rebirth", Aired: "2010-06-24", SeriesID: 73871, SeasonNumber: 6, Number: 1},
			4319164: {ID: 4319164, Name: "The Bots and the Bees", Aired: "2012-06-20", SeriesID: 73871, SeasonNumber: 7, Number: 1},
			8234611: {ID: 8234611, Name: "Bender's Big Score (1)", Aired: "2008-03-23", SeriesID: 73871, SeasonNumber: 6, Number: 1},
		},
		// Series-level remote id, as TVDB really carries it.
		seriesRemoteIDs: map[int][]tvdb.RemoteID{
			73871: {{ID: "615", SourceName: "moviedb"}},
		},
		// Episode-level remote ids. 395236 (S0E5 "Bender's Game") points at TMDB
		// episode 13253, which no longer exists -- read from live TVDB and
		// confirmed as a 404 on 2026-09-25. This is the stale pointer the mapper
		// must survive.
		episodeRemoteIDs: map[int64][]tvdb.RemoteID{
			395236: {{ID: "13253", SourceName: "moviedb"}},
		},
		lookupUsed: "tmdb",
	}
}

func futuramaTMDB() *fakeTMDB {
	return &fakeTMDB{
		details: map[string]*tmdb.EpisodeDetails{
			"615/1/1": {ID: 35076, Name: "Space Pilot 3000", AirDate: "1999-03-28", SeasonNumber: 1, EpisodeNumber: 1},
			"615/6/1": {ID: 35077, Name: "Rebirth", AirDate: "2010-06-24", SeasonNumber: 6, EpisodeNumber: 1},
			"615/7/1": {ID: 35107, Name: "The Bots and the Bees", AirDate: "2012-06-20", SeasonNumber: 7, EpisodeNumber: 1},

			// Season 0, exactly as TMDB reports it (measured 2026-09-25). TMDB and
			// TVDB `default` SWAP s0e1/s0e2, and both air 2007-11-27 -- this is the
			// collision that produced the wrong-episode bug.
			"615/0/1": {ID: 35101, Name: "Bender's Big Score", AirDate: "2007-11-27", SeasonNumber: 0, EpisodeNumber: 1},
			"615/0/2": {ID: 35103, Name: "Everybody Loves Hypnotoad", AirDate: "2007-11-27", SeasonNumber: 0, EpisodeNumber: 2},
			"615/0/5": {ID: 35105, Name: "Bender's Game", AirDate: "2008-11-04", SeasonNumber: 0, EpisodeNumber: 5},
		},
		// The real TMDB cross-references, each confirmed against TVDB's own
		// /episodes/{id}/extended rather than trusted from TMDB alone.
		episodeExternalIDs: map[string]*tmdb.EpisodeExternalIDs{
			"615/0/1": {IMDbID: "tt0471711", TVDBID: 342888},  // TVDB default s0e2 Bender's Big Score
			"615/0/2": {IMDbID: "tt1151330", TVDBID: 389457},  // TVDB default s0e1 Everybody Loves Hypnotoad
			"615/0/5": {IMDbID: "tt1054486", TVDBID: 395236},  // TVDB default s0e5 Bender's Game
			"615/1/1": {IMDbID: "tt0584449", TVDBID: 131174},  // TVDB default s1e1 Space Pilot 3000
			"615/6/1": {IMDbID: "tt1620650", TVDBID: 1051911}, // TVDB default s6e1 Rebirth
			"615/7/1": {IMDbID: "tt1952190", TVDBID: 4319164}, // TVDB default s7e1 The Bots and the Bees
		},
		names:  map[int]string{615: "Futurama"},
		tvdbID: map[int]int{615: 73871},
	}
}

// futuramaMapperWithoutEpisodeExternalIDs removes TMDB's episode cross-references,
// leaving only air date and name as evidence -- the state every episode without a
// TMDB->TVDB link is in.
func futuramaMapperWithoutEpisodeExternalIDs(t *testing.T) *Mapper {
	t.Helper()
	m := futuramaMapper(t)
	m.tmdb.(*fakeTMDB).episodeExternalIDs = nil
	return m
}

func futuramaMapper(t *testing.T) *Mapper {
	t.Helper()
	return &Mapper{
		tvdb:                futuramaTVDB(),
		tmdb:                futuramaTMDB(),
		tvdbSeasonType:      "default",
		fallbackSeasonTypes: defaultFallbackSeasonTypes,
		maxTVDBPages:        200,
		maxTMDBSeasons:      300,
	}
}

// --- Task 0.0 ---------------------------------------------------------------

// TestTmdbToTvdbRefusesSameCoordinateFallback pins that an episode the evidence
// cannot place is an error, not whatever TVDB episode shares its numbers.
//
// TMDB 615 S7E1 is "The Bots and the Bees" (2012-06-20). Under the "alternate"
// order TVDB has no episode on that date and none by that name, while alternate
// s7e1 is "Rebirth" (2010-06-24) -- a different episode with the same numbers.
// Handing back "Rebirth" is the bug.
func TestTmdbToTvdbRefusesSameCoordinateFallback(t *testing.T) {
	m := futuramaMapper(t)

	got, err := m.TmdbToTvdbInOrder(context.Background(), 615, 7, 1, "alternate")
	if err == nil {
		t.Fatalf("expected an error for an unplaceable episode; got %q (%d) via matched_by=%q",
			got.TVDBEpisodeName, got.TVDBEpisodeID, got.MatchedBy)
	}
	if !strings.Contains(err.Error(), "unable to map") {
		t.Fatalf("want an 'unable to map' error, got %v", err)
	}
}

// TestTmdbToTvdbDoesNotReturnTheWrongEpisodeByNumbers states the same case as an
// assertion about the answer: alternate s7e1 "Rebirth" must never come back for a
// question about "The Bots and the Bees".
func TestTmdbToTvdbDoesNotReturnTheWrongEpisodeByNumbers(t *testing.T) {
	m := futuramaMapper(t)

	got, err := m.TmdbToTvdbInOrder(context.Background(), 615, 7, 1, "alternate")
	if err == nil && got.TVDBEpisodeName == "Rebirth" {
		t.Fatalf("returned alternate s7e1 %q for TMDB S7E1 %q: same numbers, different episode",
			got.TVDBEpisodeName, "The Bots and the Bees")
	}
}

// TestTmdbToTvdbMapsWhenEvidenceAgrees is the positive control for the
// evidence-based path. It removes the TMDB cross-reference on purpose: with an
// exact id available the mapper takes the exact path instead (covered by
// TestTmdbSeasonZeroResolvesTheFilmNotHypnotoad), and this test exists to pin
// that air date and name still work when there is no id to lean on.
func TestTmdbToTvdbMapsWhenEvidenceAgrees(t *testing.T) {
	m := futuramaMapperWithoutEpisodeExternalIDs(t)

	got, err := m.TmdbToTvdb(context.Background(), 615, 1, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.TVDBEpisodeName != "Space Pilot 3000" {
		t.Fatalf("got %q, want %q", got.TVDBEpisodeName, "Space Pilot 3000")
	}
	if got.MatchedBy != "air_date" {
		t.Fatalf("got matched_by %q, want %q", got.MatchedBy, "air_date")
	}
}

// TestTmdbToTvdbCoordinateFallbackIsOptIn pins that the removed behaviour, if
// re-enabled, announces itself rather than masquerading as a match.
func TestTmdbToTvdbCoordinateFallbackIsOptIn(t *testing.T) {
	m := futuramaMapper(t)
	m.allowCoordinateIdentityFallback = true

	got, err := m.TmdbToTvdbInOrder(context.Background(), 615, 7, 1, "alternate")
	if err != nil {
		t.Fatalf("fallback was opted in, expected a result: %v", err)
	}
	if got.MatchedBy != "assumed_same_coordinates" {
		t.Fatalf("got matched_by %q; an assumption must never be reported as a match",
			got.MatchedBy)
	}
	if got.TVDBEpisodeName != "Rebirth" {
		t.Fatalf("expected the same-coordinate episode %q, got %q",
			"Rebirth", got.TVDBEpisodeName)
	}
}

// --- Task 0.0b --------------------------------------------------------------
//
// TMDB 1433 is "American Dad!". TVDB's bare-number remote-id search answers with
// series 84070 "War and Remembrance", a 1988 WWII miniseries, because the search
// indexes a bare number across every id namespace. Measured live 2026-09-25.
// TMDB's own external_ids correctly gives 73141.

func americanDadMapper(t *testing.T, withExternalIDs bool) *Mapper {
	t.Helper()
	external := map[int]int{}
	if withExternalIDs {
		external[1433] = 73141
	}
	return &Mapper{
		tvdb: &fakeTVDB{
			seriesByTMDB: map[int]*tvdb.SeriesBaseRecord{
				1433: {ID: 84070, Name: "War and Remembrance"},
			},
			// The prefixed forms returned nothing when measured; only the bare
			// number resolves, and it resolves wrong.
			seriesByRemote: map[string]*tvdb.SeriesBaseRecord{},
			seriesByID: map[int]*tvdb.SeriesBaseRecord{
				73141: {ID: 73141, Name: "American Dad!"},
				84070: {ID: 84070, Name: "War and Remembrance"},
			},
			episodes: map[int]map[string][]tvdb.EpisodeBaseRecord{
				73141: {"default": {
					{ID: 5001, Name: "Pilot", Aired: "2005-02-06", SeriesID: 73141, SeasonNumber: 1, Number: 1},
				}},
				84070: {"default": {
					{ID: 5002, Name: "Part I: December 15-27, 1941", Aired: "1988-11-13", SeriesID: 84070, SeasonNumber: 1, Number: 1},
				}},
			},
			lookupUsed: "tvdb",
		},
		tmdb: &fakeTMDB{
			details: map[string]*tmdb.EpisodeDetails{
				"1433/1/1": {ID: 700001, Name: "Pilot", AirDate: "2005-02-06", SeasonNumber: 1, EpisodeNumber: 1},
			},
			names:  map[int]string{1433: "American Dad!"},
			tvdbID: external,
		},
		tvdbSeasonType:      "default",
		fallbackSeasonTypes: defaultFallbackSeasonTypes,
		maxTVDBPages:        200,
		maxTMDBSeasons:      300,
	}
}

// TestTmdbToTvdbPrefersVerifiedExternalIDs pins that TMDB's external_ids wins and
// is accepted only because its name corroborates.
func TestTmdbToTvdbPrefersVerifiedExternalIDs(t *testing.T) {
	m := americanDadMapper(t, true)

	got, err := m.TmdbToTvdb(context.Background(), 1433, 1, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.TVDBSeriesID != 73141 {
		t.Fatalf("resolved tvdb series %d, want 73141 (American Dad!); "+
			"84070 is War and Remembrance and must never be chosen", got.TVDBSeriesID)
	}
	if got.TVDBEpisodeName != "Pilot" {
		t.Fatalf("got episode %q, want %q", got.TVDBEpisodeName, "Pilot")
	}
}

// TestTmdbToTvdbRejectsUnverifiedSeries pins that an id match alone is not a
// mapping: with no external_ids available the search returns a series with a
// different name, and the mapper must refuse rather than relabel American Dad
// submissions as War and Remembrance.
func TestTmdbToTvdbRejectsUnverifiedSeries(t *testing.T) {
	m := americanDadMapper(t, false)

	got, err := m.TmdbToTvdb(context.Background(), 1433, 1, 1)
	if err == nil {
		t.Fatalf("accepted an unverified series link: tvdb %d %q",
			got.TVDBSeriesID, got.TVDBEpisodeName)
	}
	if !strings.Contains(err.Error(), "verified") {
		t.Fatalf("want a verification failure, got %v", err)
	}
}

func TestNamesCorroborate(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"American Dad!", "American Dad!", true},
		{"american dad", "American Dad!", true},
		{"Futurama", "Futurama", true},
		{"American Dad!", "War and Remembrance", false},
		{"Futurama", "Futurama (1999)", false}, // a year suffix is not stripped
		{"", "Futurama", false},
		{"Futurama", "", false},
	}
	for _, c := range cases {
		if got := namesCorroborate(c.a, c.b); got != c.want {
			t.Fatalf("namesCorroborate(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// --- Task 0.0c --------------------------------------------------------------

// TestTmdbToTvdbInOrderResolvesAlternate pins the point of naming an order: the
// same TMDB coordinate resolves to different TVDB episodes in different orders.
//
// TMDB S6E1 is "Rebirth" (2010-06-24). Under "default" that is default s6e1.
// Under "alternate", s6e1 is "Bender's Big Score (1)" (2008-03-23), so the same
// TMDB episode answers at alternate s7e1 instead.
func TestTmdbToTvdbInOrderResolvesAlternate(t *testing.T) {
	m := futuramaMapper(t)

	def, err := m.TmdbToTvdbInOrder(context.Background(), 615, 6, 1, "default")
	if err != nil {
		t.Fatalf("default: unexpected error: %v", err)
	}
	if def.TVDBEpisodeID != 1051911 || def.TVDBSeason != 6 || def.TVDBEpisode != 1 {
		t.Fatalf("default: got episode %d at s%de%d, want 1051911 at s6e1",
			def.TVDBEpisodeID, def.TVDBSeason, def.TVDBEpisode)
	}

	alt, err := m.TmdbToTvdbInOrder(context.Background(), 615, 6, 1, "alternate")
	if err != nil {
		t.Fatalf("alternate: unexpected error: %v", err)
	}
	if alt.TVDBEpisodeID != 1051911 {
		t.Fatalf("alternate: got episode %d, want 1051911 (Rebirth)", alt.TVDBEpisodeID)
	}
	if alt.TVDBSeason != 7 || alt.TVDBEpisode != 1 {
		t.Fatalf("alternate: got s%de%d, want s7e1 -- the same episode has different "+
			"numbers in a different order", alt.TVDBSeason, alt.TVDBEpisode)
	}
}

// TestTmdbToTvdbInOrderRejectsUnknownOrder pins that a typo'd order is an error
// rather than a silent fallback to default -- answering in the wrong order
// without saying so is the failure mode this change exists to remove.
func TestTmdbToTvdbInOrderRejectsUnknownOrder(t *testing.T) {
	m := futuramaMapper(t)

	if _, err := m.TmdbToTvdbInOrder(context.Background(), 615, 1, 1, "bogus"); err == nil {
		t.Fatal("expected an error for an unknown order")
	}
}

// TestTmdbToTvdbInOrderEmptyUsesConfiguredOrder pins backward compatibility: an
// empty order means "the mapper's configured order", not "no order".
func TestTmdbToTvdbInOrderEmptyUsesConfiguredOrder(t *testing.T) {
	m := futuramaMapper(t)

	got, err := m.TmdbToTvdbInOrder(context.Background(), 615, 1, 1, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.TVDBEpisodeName != "Space Pilot 3000" {
		t.Fatalf("got %q, want %q", got.TVDBEpisodeName, "Space Pilot 3000")
	}
}

// --- Task 0.0e --------------------------------------------------------------

// TestTmdbToTvdbWithHintsAlternateConfirmedByTitle covers the intended use:
// resolve in a named order, then confirm with the episode title before accepting.
func TestTmdbToTvdbWithHintsAlternateConfirmedByTitle(t *testing.T) {
	m := futuramaMapper(t)

	got, err := m.TmdbToTvdbWithHints(context.Background(), 615, 6, 1, EpisodeHints{
		TVDBOrder:   "alternate",
		EpisodeName: "Rebirth",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.TVDBEpisodeID != 1051911 {
		t.Fatalf("got episode id %d, want 1051911", got.TVDBEpisodeID)
	}
	if got.MatchedBy != "air_date+name" {
		t.Fatalf("got matched_by %q, want %q (the title confirmed the date match)",
			got.MatchedBy, "air_date+name")
	}
}

// TestTmdbToTvdbWithHintsRejectsContradictingTitle is the guard the direction
// needs: the numbers resolve, but the title says otherwise, so the answer is
// refused rather than accepted on numbering alone. Here the numbers land on
// alternate s7e1 "Rebirth" while the caller insists on "Bender's Big Score (1)".
func TestTmdbToTvdbWithHintsRejectsContradictingTitle(t *testing.T) {
	m := futuramaMapper(t)

	_, err := m.TmdbToTvdbWithHints(context.Background(), 615, 6, 1, EpisodeHints{
		TVDBOrder:   "alternate",
		EpisodeName: "Bender's Big Score (1)",
	})
	if err == nil {
		t.Fatal("accepted a match whose title contradicts the caller")
	}
	var hintErr *EpisodeHintError
	if !errors.As(err, &hintErr) {
		t.Fatalf("want an *EpisodeHintError, got %T: %v", err, err)
	}
	if hintErr.Field != "episode_name" {
		t.Fatalf("got field %q, want %q", hintErr.Field, "episode_name")
	}
}

// TestTmdbToTvdbWithHintsEpisodeIDIsAuthoritative pins that an episode id needs no
// numbering: it resolves even where the caller's numbers would place a different
// episode.
func TestTmdbToTvdbWithHintsEpisodeIDIsAuthoritative(t *testing.T) {
	m := futuramaMapper(t)

	got, err := m.TmdbToTvdbWithHints(context.Background(), 615, 6, 1, EpisodeHints{
		TVDBOrder:     "alternate",
		TVDBEpisodeID: 8234611,
		EpisodeName:   "Bender's Big Score (1)",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.TVDBEpisodeID != 8234611 || got.MatchedBy != "episode_id" {
		t.Fatalf("got id %d via %q, want 8234611 via episode_id", got.TVDBEpisodeID, got.MatchedBy)
	}
	if got.TVDBSeason != 6 || got.TVDBEpisode != 1 {
		t.Fatalf("got coordinates s%de%d, want s6e1 in the alternate order",
			got.TVDBSeason, got.TVDBEpisode)
	}
}

// TestTmdbToTvdbWithHintsRejectsUnknownOrder pins that a typo cannot silently
// resolve in the wrong order.
func TestTmdbToTvdbWithHintsRejectsUnknownOrder(t *testing.T) {
	m := futuramaMapper(t)

	_, err := m.TmdbToTvdbWithHints(context.Background(), 615, 1, 1, EpisodeHints{TVDBOrder: "season2"})
	var hintErr *EpisodeHintError
	if !errors.As(err, &hintErr) || hintErr.Field != "tvdb_order" {
		t.Fatalf("want an EpisodeHintError on tvdb_order, got %v", err)
	}
}

// TestTmdbToTvdbWithHintsEpisodeIDOutsideOrder pins that an id the named order
// does not contain is a contradiction, not a silent success.
func TestTmdbToTvdbWithHintsEpisodeIDOutsideOrder(t *testing.T) {
	m := futuramaMapper(t)

	_, err := m.TmdbToTvdbWithHints(context.Background(), 615, 6, 1, EpisodeHints{
		TVDBOrder:     "alternate",
		TVDBEpisodeID: 1111111, // in no order
	})
	var hintErr *EpisodeHintError
	if !errors.As(err, &hintErr) || hintErr.Field != "tvdb_episode_id" {
		t.Fatalf("want an EpisodeHintError on tvdb_episode_id, got %v", err)
	}
}

// --- pagination (plan task 0.0d) --------------------------------------------

// TestFetchOrderEpisodesPagesPastTheFirstPage pins the 500-episode page limit.
// Reading only page 0 makes every long-running series look truncated, which turns
// real episodes into "does not exist" rejections. One Piece really returns
// 500/500/242.
func TestFetchOrderEpisodesPagesPastTheFirstPage(t *testing.T) {
	const total = 1242
	episodes := make([]tvdb.EpisodeBaseRecord, 0, total)
	for i := 0; i < total; i++ {
		episodes = append(episodes, tvdb.EpisodeBaseRecord{
			ID:           int64(900000 + i),
			Name:         fmt.Sprintf("Episode %d", i+1),
			SeriesID:     81797,
			SeasonNumber: 1,
			Number:       i + 1,
		})
	}
	m := &Mapper{
		tvdb: &fakeTVDB{
			seriesByID: map[int]*tvdb.SeriesBaseRecord{81797: {ID: 81797, Name: "One Piece"}},
			episodes:   map[int]map[string][]tvdb.EpisodeBaseRecord{81797: {"default": episodes}},
		},
		tvdbSeasonType: "default",
		maxTVDBPages:   200,
	}

	got, err := m.fetchOrderEpisodes(context.Background(), 81797, "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != total {
		t.Fatalf("got %d episodes, want %d -- the list was truncated at the page boundary", len(got), total)
	}
	if findEpisodeByID(got, int64(900000+total-1)) == nil {
		t.Fatal("could not find the final episode; pages beyond the first were not read")
	}
}

// --- the real Bleach case ---------------------------------------------------
//
// Bleach is the measured case where the same coordinates name completely
// different episodes: TMDB 30984 S2E1 is "The Blood Warfare" (2022-10-11), while
// TVDB 74796 default s2e1 is "突入！死神の世界" (2005-03-01). TMDB's S2 is the 2022
// revival; TVDB files those episodes at s17.
//
// This is why "no evidence" must not become "same numbers, close enough": the
// removed fallback would have answered a 2022 question with a 2005 episode.

func bleachMapper() *Mapper {
	return &Mapper{
		tvdb: &fakeTVDB{
			seriesByID: map[int]*tvdb.SeriesBaseRecord{74796: {ID: 74796, Name: "Bleach"}},
			seriesByTMDB: map[int]*tvdb.SeriesBaseRecord{
				30984: {ID: 74796, Name: "Bleach"},
			},
			episodes: map[int]map[string][]tvdb.EpisodeBaseRecord{
				74796: {"default": {
					{ID: 600001, Name: "突入！死神の世界", Aired: "2005-03-01", SeriesID: 74796, SeasonNumber: 2, Number: 1},
					{ID: 600002, Name: "THE BLOOD WARFARE", Aired: "2022-10-11", SeriesID: 74796, SeasonNumber: 17, Number: 1},
				}},
			},
		},
		tmdb: &fakeTMDB{
			details: map[string]*tmdb.EpisodeDetails{
				"30984/2/1": {ID: 800001, Name: "The Blood Warfare", AirDate: "2022-10-11", SeasonNumber: 2, EpisodeNumber: 1},
				// An episode whose air date is absent from the order and whose
				// name is localized on the TVDB side: no evidence places it.
				"30984/2/9": {ID: 800009, Name: "The Blade and Me", AirDate: "2022-12-06", SeasonNumber: 2, EpisodeNumber: 9},
			},
			names:  map[int]string{30984: "Bleach"},
			tvdbID: map[int]int{30984: 74796},
		},
		tvdbSeasonType:      "default",
		fallbackSeasonTypes: defaultFallbackSeasonTypes,
		maxTVDBPages:        200,
		maxTMDBSeasons:      300,
	}
}

// TestBleachAirDateFindsTheRightEpisode is the positive control for the case
// above: the air date places the 2022 episode at TVDB s17e1, not s2e1.
func TestBleachAirDateFindsTheRightEpisode(t *testing.T) {
	m := bleachMapper()

	got, err := m.TmdbToTvdb(context.Background(), 30984, 2, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.TVDBEpisodeID != 600002 || got.TVDBSeason != 17 {
		t.Fatalf("got episode %d at s%de%d, want 600002 at s17e1",
			got.TVDBEpisodeID, got.TVDBSeason, got.TVDBEpisode)
	}
}

// TestBleachDoesNotLeakTheOldEpisodeOntoNewCoordinates pins the failure the
// fallback caused: when the evidence places nothing, the mapper must not answer
// with the 2005 episode that happens to share the caller's numbers.
//
// (The opt-in behaviour of the removed fallback is covered by
// TestTmdbToTvdbCoordinateFallbackIsOptIn, where a same-coordinate episode
// genuinely exists to be returned.)
func TestBleachDoesNotLeakTheOldEpisodeOntoNewCoordinates(t *testing.T) {
	m := bleachMapper()

	got, err := m.TmdbToTvdb(context.Background(), 30984, 2, 9)
	if err == nil {
		t.Fatalf("expected an honest failure; got episode %q at s%de%d via %q",
			got.TVDBEpisodeName, got.TVDBSeason, got.TVDBEpisode, got.MatchedBy)
	}
	if !strings.Contains(err.Error(), "unable to map") {
		t.Fatalf("want an 'unable to map' error, got %v", err)
	}
}
