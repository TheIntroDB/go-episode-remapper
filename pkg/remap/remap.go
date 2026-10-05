package remap

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Pasithea0/go-tmdb-episodes-fix/pkg/tmdb"
	"github.com/Pasithea0/go-tmdb-episodes-fix/pkg/tvdb"
)

type Options struct {
	TVDBAPIKey          string
	TVDBPIN             string
	TMDBBearerToken     string
	TVDBSeasonType      string
	FallbackSeasonTypes []string
	MaxTVDBPageScan     int
	MaxTMDBSeasonScan   int
	// AllowCoordinateIdentityFallback opts back in to the removed behaviour where
	// an unmatchable episode is assumed to share coordinates across schemes.
	// Leave false unless a caller has independently established that the orders
	// agree; results then carry MatchedBy "assumed_same_coordinates".
	AllowCoordinateIdentityFallback bool
}

var defaultFallbackSeasonTypes = []string{"official", "dvd", "alternate", "regional"}

// tvdbAPI is the slice of the TVDB client the mapper depends on. Declaring it as
// an interface keeps the mapping rules unit-testable: the rules are the part that
// silently produces wrong episodes, and they cannot be tested through a concrete
// client that reaches the network.
type tvdbAPI interface {
	SearchSeriesByRemoteID(ctx context.Context, remoteID string) (*tvdb.SeriesBaseRecord, error)
	FindSeriesByIMDbID(ctx context.Context, imdbID string) (*tvdb.SeriesBaseRecord, string, error)
	FindSeriesByTMDBID(ctx context.Context, tmdbID int) (*tvdb.SeriesBaseRecord, string, error)
	GetSeriesExtended(ctx context.Context, seriesID int) (*tvdb.SeriesExtendedRecord, error)
	GetSeriesEpisodes(ctx context.Context, seriesID int, seasonType string, page int, season *int, episodeNumber *int, airDate *string) ([]tvdb.EpisodeBaseRecord, error)
	GetEpisodeExtended(ctx context.Context, episodeID int64) (*tvdb.EpisodeExtendedRecord, error)
}

// tmdbAPI is the slice of the TMDB client the mapper depends on.
type tmdbAPI interface {
	GetEpisodeDetails(ctx context.Context, tvID int, season int, episode int) (*tmdb.EpisodeDetails, error)
	GetEpisodeExternalIDs(ctx context.Context, tvID int, season int, episode int) (*tmdb.EpisodeExternalIDs, error)
	GetEpisodeByID(ctx context.Context, episodeID int) (*tmdb.EpisodeByID, error)
	GetTVDetails(ctx context.Context, tvID int) (*tmdb.TVDetails, error)
	GetTvdbIDFromTmdbID(ctx context.Context, tvID int) (int, error)
	GetEpisodeGroups(ctx context.Context, tvID int) ([]tmdb.EpisodeGroup, error)
	GetEpisodeGroup(ctx context.Context, groupID string) (*tmdb.EpisodeGroupDetail, error)
	GetSeasonEpisodes(ctx context.Context, tvID int, season int) ([]tmdb.SeasonEpisode, error)
}

type Mapper struct {
	tvdb                tvdbAPI
	tmdb                tmdbAPI
	tvdbSeasonType      string
	fallbackSeasonTypes []string
	maxTVDBPages        int
	maxTMDBSeasons      int
	// allowCoordinateIdentityFallback re-enables the behaviour where an episode
	// that cannot be matched by air date or name is assumed to sit at the same
	// coordinates in both schemes. Off by default: that assumption is false for
	// exactly the shows this mapper exists to handle (TMDB Futurama S6E1 is
	// "Bender's Big Score (1)" in the alternate order while TVDB default S6E1 is
	// "Rebirth"). When on, results carry MatchedBy "assumed_same_coordinates" so
	// a caller can never mistake the guess for a match.
	allowCoordinateIdentityFallback bool
}

func NewMapper(opts Options) *Mapper {
	seasonType := strings.TrimSpace(opts.TVDBSeasonType)
	if seasonType == "" {
		seasonType = "default"
	}

	// An unset fallback list means the caller didn't narrow the search, so the
	// documented default applies. Without this the mapper silently tried the
	// primary season type only -- the variable below existed but nothing used it.
	fallbacks := opts.FallbackSeasonTypes
	if len(fallbacks) == 0 {
		fallbacks = defaultFallbackSeasonTypes
	}

	maxPages := opts.MaxTVDBPageScan
	if maxPages <= 0 {
		maxPages = 200
	}

	maxSeasons := opts.MaxTMDBSeasonScan
	if maxSeasons <= 0 {
		maxSeasons = 300
	}

	// Declared as the interface, not the concrete client: assigning a nil
	// *tmdb.Client into an interface field yields a non-nil interface holding a
	// nil pointer, so the `m.tmdb == nil` guard in TmdbToTvdb would stop working.
	var tmdbClient tmdbAPI
	if strings.TrimSpace(opts.TMDBBearerToken) != "" {
		tmdbClient = tmdb.NewClient(opts.TMDBBearerToken)
	}

	return &Mapper{
		tvdb:                            tvdb.NewClient(opts.TVDBAPIKey, opts.TVDBPIN),
		tmdb:                            tmdbClient,
		tvdbSeasonType:                  seasonType,
		fallbackSeasonTypes:             fallbacks,
		maxTVDBPages:                    maxPages,
		maxTMDBSeasons:                  maxSeasons,
		allowCoordinateIdentityFallback: opts.AllowCoordinateIdentityFallback,
	}
}

type TmdbToTvdbResult struct {
	InputTMDBSeriesID int    `json:"input_tmdb_series_id"`
	InputSeason       int    `json:"input_season"`
	InputEpisode      int    `json:"input_episode"`
	TVDBSeriesID      int    `json:"tvdb_series_id"`
	TVDBEpisodeID     int64  `json:"tvdb_episode_id"`
	TVDBSeason        int    `json:"tvdb_season"`
	TVDBEpisode       int    `json:"tvdb_episode"`
	TVDBEpisodeName   string `json:"tvdb_episode_name"`
	MatchedBy         string `json:"matched_by"`
	TVDBSeriesLookup  string `json:"tvdb_series_lookup"`
}

// TmdbToTvdb maps a TMDB coordinate to TVDB within the mapper's configured order.
func (m *Mapper) TmdbToTvdb(ctx context.Context, tmdbSeriesID int, season int, episode int) (*TmdbToTvdbResult, error) {
	return m.TmdbToTvdbInOrder(ctx, tmdbSeriesID, season, episode, m.tvdbSeasonType)
}

// TmdbToTvdbInOrder maps a TMDB coordinate to TVDB within a named order.
//
// Prefer this over TmdbToTvdb, because the order is part of the answer: TVDB
// coordinates only mean something inside one. Futurama (6,1) is "Rebirth" under
// default and "Bender's Big Score (1)" under alternate -- two different episodes
// with two different ids, and the same numbers for both.
func (m *Mapper) TmdbToTvdbInOrder(ctx context.Context, tmdbSeriesID int, season int, episode int, order string) (*TmdbToTvdbResult, error) {
	if m.tmdb == nil {
		return nil, errors.New("tmdb bearer token is required for tmdb->tvdb mapping")
	}

	// An empty order means "whatever the mapper was configured with", so that
	// callers who genuinely do not care keep the old single-argument behaviour.
	if strings.TrimSpace(order) == "" {
		order = m.tvdbSeasonType
	}
	normalizedOrder, ok := NormalizeTVDBOrder(order)
	if !ok {
		return nil, fmt.Errorf("unknown tvdb order %q", order)
	}

	// The series must be verified, not merely found by id: TVDB's bare-number
	// remote-id search can return a different series entirely (see
	// resolveVerifiedTVDBSeries).
	tvdbSeries, lookupUsed, err := m.resolveVerifiedTVDBSeries(ctx, tmdbSeriesID)
	if err != nil {
		return nil, err
	}
	if tvdbSeries == nil {
		return nil, fmt.Errorf("tvdb series not found for tmdb id %d", tmdbSeriesID)
	}

	tmdbEp, err := m.tmdb.GetEpisodeDetails(ctx, tmdbSeriesID, season, episode)
	if err != nil {
		return nil, err
	}

	// 0. TMDB's own cross-reference is exact and needs no heuristics. TMDB records
	//    the TVDB episode id for many episodes, and that id is a DEFAULT-order
	//    record.
	//
	//    Verified live 2026-09-25 (Futurama, TMDB 615): s0e1 Bender's Big Score ->
	//    342888 = TVDB default s0e2, and s0e2 Everybody Loves Hypnotoad -> 389457 =
	//    default s0e1. Those two are precisely the pair that evidence-based
	//    matching gets WRONG -- TMDB and TVDB default swap s0e1/s0e2 and both air
	//    2007-11-27 -- so this is the only path that resolves them exactly.
	//
	//    Default only: the id is a default-order record and does not appear in
	//    other orders' lists (TVDB alternate s0e2 is 11871359, not 342888), so a
	//    different order falls through to the evidence below rather than being
	//    handed coordinates that belong to another order.
	//
	//    Failures here are deliberately non-fatal: a missing or 404ing
	//    external_ids link is not an error, it just means this shortcut is
	//    unavailable. Falling through to real evidence is not a guess.
	if normalizedOrder == "default" {
		if ext, extErr := m.tmdb.GetEpisodeExternalIDs(ctx, tmdbSeriesID, season, episode); extErr == nil && ext != nil && ext.TVDBID != 0 {
			if byID, idErr := m.tvdb.GetEpisodeExtended(ctx, int64(ext.TVDBID)); idErr == nil && byID != nil && byID.SeriesID == tvdbSeries.ID {
				return &TmdbToTvdbResult{
					InputTMDBSeriesID: tmdbSeriesID,
					InputSeason:       season,
					InputEpisode:      episode,
					TVDBSeriesID:      tvdbSeries.ID,
					TVDBEpisodeID:     byID.ID,
					TVDBSeason:        byID.SeasonNumber,
					TVDBEpisode:       byID.Number,
					TVDBEpisodeName:   byID.Name,
					MatchedBy:         "tmdb_episode_tvdb_id",
					TVDBSeriesLookup:  lookupUsed,
				}, nil
			}
		}
	}

	if strings.TrimSpace(tmdbEp.AirDate) != "" {
		// Candidates are computed LOCALLY from the complete order list. TVDB's own
		// airDate query parameter is not a trustworthy filter and must not decide
		// an episode. Measured live 2026-09-25 on Futurama (73871, order=default):
		//
		//	airDate=2007-11-27 -> 1 episode, but TWO aired that day (s0e1, s0e2)
		//	airDate=2008-06-30 -> 0 episodes, though s0e3 aired exactly then
		//	airDate=2008-06-24 -> returns s0e3 (aired 2008-06-30) as well as s0e4
		//
		// So it both misses matching episodes and returns non-matching ones. It
		// happens to look correct when a date has exactly one episode, which is
		// why the previous len(eps)==1 shortcut survived: it was reading a
		// filtered list whose contents were never verified. Filtering a complete
		// list compares the field itself instead of whatever TVDB means by the
		// parameter.
		all, err := m.fetchOrderEpisodes(ctx, tvdbSeries.ID, normalizedOrder)
		if err != nil {
			return nil, err
		}

		var candidates []tvdb.EpisodeBaseRecord
		for i := range all {
			if strings.TrimSpace(all[i].Aired) == strings.TrimSpace(tmdbEp.AirDate) {
				candidates = append(candidates, all[i])
			}
		}

		switch {
		case len(candidates) == 1:
			ep := candidates[0]
			return &TmdbToTvdbResult{
				InputTMDBSeriesID: tmdbSeriesID,
				InputSeason:       season,
				InputEpisode:      episode,
				TVDBSeriesID:      tvdbSeries.ID,
				TVDBEpisodeID:     ep.ID,
				TVDBSeason:        ep.SeasonNumber,
				TVDBEpisode:       ep.Number,
				TVDBEpisodeName:   ep.Name,
				MatchedBy:         "air_date",
				TVDBSeriesLookup:  lookupUsed,
			}, nil

		case len(candidates) > 1:
			// A tie on air date is legitimate and common: Futurama default s0e1
			// and s0e2 both aired 2007-11-27. The name breaks the tie. If it
			// cannot, this is genuinely ambiguous, and picking the first
			// candidate is exactly how the wrong episode reaches the library --
			// it used to return s0e1 "Everybody Loves Hypnotoad" for TMDB s0e1
			// "Bender's Big Score".
			if strings.TrimSpace(tmdbEp.Name) != "" {
				if best := pickBestByName(candidates, tmdbEp.Name); best != nil {
					return &TmdbToTvdbResult{
						InputTMDBSeriesID: tmdbSeriesID,
						InputSeason:       season,
						InputEpisode:      episode,
						TVDBSeriesID:      tvdbSeries.ID,
						TVDBEpisodeID:     best.ID,
						TVDBSeason:        best.SeasonNumber,
						TVDBEpisode:       best.Number,
						TVDBEpisodeName:   best.Name,
						MatchedBy:         "air_date+name",
						TVDBSeriesLookup:  lookupUsed,
					}, nil
				}
			}
			return nil, fmt.Errorf(
				"ambiguous tmdb %d s%de%d: %d episodes of tvdb series %d in the %q order aired %s (%s) and none matched the name %q",
				tmdbSeriesID, season, episode, len(candidates), tvdbSeries.ID, normalizedOrder,
				tmdbEp.AirDate, episodeNames(candidates), tmdbEp.Name)
		}
	}

	if strings.TrimSpace(tmdbEp.Name) != "" {
		wantName := tmdbEp.Name
		for page := 0; page < m.maxTVDBPages; page++ {
			eps, err := m.tvdb.GetSeriesEpisodes(ctx, tvdbSeries.ID, normalizedOrder, page, nil, nil, nil)
			if err != nil {
				return nil, err
			}
			if len(eps) == 0 {
				break
			}
			best := pickBestByName(eps, wantName)
			if best != nil {
				return &TmdbToTvdbResult{
					InputTMDBSeriesID: tmdbSeriesID,
					InputSeason:       season,
					InputEpisode:      episode,
					TVDBSeriesID:      tvdbSeries.ID,
					TVDBEpisodeID:     best.ID,
					TVDBSeason:        best.SeasonNumber,
					TVDBEpisode:       best.Number,
					TVDBEpisodeName:   best.Name,
					MatchedBy:         "name_scan",
					TVDBSeriesLookup:  lookupUsed,
				}, nil
			}
		}
	}

	// Coordinate identity is an assumption, not a match: it asserts that both
	// schemes number this episode the same way, which is false exactly where the
	// mapper is needed (TMDB Futurama S6E1 is "Bender's Big Score (1)" while TVDB
	// default S6E1 is "Rebirth"). It is therefore opt-in, and when opted into it
	// is labelled as an assumption so no caller can mistake it for evidence.
	if m.allowCoordinateIdentityFallback {
		s, e := season, episode
		eps, err := m.tvdb.GetSeriesEpisodes(ctx, tvdbSeries.ID, normalizedOrder, 0, &s, &e, nil)
		if err != nil {
			return nil, err
		}
		if len(eps) > 0 {
			ep := eps[0]
			return &TmdbToTvdbResult{
				InputTMDBSeriesID: tmdbSeriesID,
				InputSeason:       season,
				InputEpisode:      episode,
				TVDBSeriesID:      tvdbSeries.ID,
				TVDBEpisodeID:     ep.ID,
				TVDBSeason:        ep.SeasonNumber,
				TVDBEpisode:       ep.Number,
				TVDBEpisodeName:   ep.Name,
				MatchedBy:         "assumed_same_coordinates",
				TVDBSeriesLookup:  lookupUsed,
			}, nil
		}
	}

	return nil, fmt.Errorf("unable to map tmdb %d s%de%d to tvdb: no tvdb episode matched by "+
		"air date or name in the %q order (coordinate identity is not assumed; "+
		"set AllowCoordinateIdentityFallback to opt in)", tmdbSeriesID, season, episode, normalizedOrder)
}

// TmdbToTvdbWithHints maps a TMDB coordinate to TVDB using what the caller
// already knows, strongest evidence first.
//
// Precedence, and why:
//  1. TVDBEpisodeID -- TVDB episode ids are unique across every order, so an id
//     identifies the episode with no numbering involved. It is checked against
//     the named order and against EpisodeName; a conflict is an EpisodeHintError
//     rather than a silent pick.
//  2. Air date within the named order, then the episode name.
//  3. Nothing. Coordinate identity is never assumed (see TmdbToTvdbInOrder).
//
// hints.TVDBOrder names the order the caller's numbers belong to. It matters:
// the same numbers name different episodes in different orders.
func (m *Mapper) TmdbToTvdbWithHints(ctx context.Context, tmdbSeriesID int, season int, episode int, hints EpisodeHints) (*TmdbToTvdbResult, error) {
	if m.tmdb == nil {
		return nil, errors.New("tmdb bearer token is required for tmdb->tvdb mapping")
	}

	order := strings.TrimSpace(hints.TVDBOrder)
	if order == "" {
		order = m.tvdbSeasonType
	}
	normalizedOrder, ok := NormalizeTVDBOrder(order)
	if !ok {
		return nil, &EpisodeHintError{Field: "tvdb_order", Detail: fmt.Sprintf("unknown tvdb order %q", hints.TVDBOrder)}
	}

	// 1. An episode id is authoritative -- it needs no numbering to be correct.
	if hints.TVDBEpisodeID != 0 {
		series, _, err := m.resolveVerifiedTVDBSeries(ctx, tmdbSeriesID)
		if err != nil {
			return nil, err
		}
		episodes, err := m.fetchOrderEpisodes(ctx, series.ID, normalizedOrder)
		if err != nil {
			return nil, err
		}
		found := findEpisodeByID(episodes, hints.TVDBEpisodeID)
		if found == nil {
			return nil, &EpisodeHintError{
				Field: "tvdb_episode_id",
				Detail: fmt.Sprintf("episode %d is not in the %q order for tvdb series %d",
					hints.TVDBEpisodeID, normalizedOrder, series.ID),
			}
		}
		if hints.EpisodeName != "" && episodeNameMatch(hints.EpisodeName, found.Name) == NameMatchMismatch {
			return nil, &EpisodeHintError{
				Field:  "episode_name",
				Detail: fmt.Sprintf("episode %d is %q but the caller said %q", found.ID, found.Name, hints.EpisodeName),
			}
		}
		return tmdbToTvdbResult(tmdbSeriesID, season, episode, series.ID, *found, "episode_id", "verified:"+normalizedOrder), nil
	}

	// 2. Evidence: air date, then name.
	result, err := m.TmdbToTvdbInOrder(ctx, tmdbSeriesID, season, episode, normalizedOrder)
	if err != nil {
		return nil, err
	}

	// 3. Corroborate whichever episode the evidence produced. A supplied title
	//    that contradicts the match means the caller and the mapper are talking
	//    about different episodes; picking one silently is how wrong data lands
	//    in the library.
	if hints.EpisodeName != "" {
		switch episodeNameMatch(hints.EpisodeName, result.TVDBEpisodeName) {
		case NameMatchMatch:
			result.MatchedBy += "+name"
		case NameMatchMismatch:
			return nil, &EpisodeHintError{
				Field: "episode_name",
				Detail: fmt.Sprintf("resolved to %q (%d) but the caller said %q",
					result.TVDBEpisodeName, result.TVDBEpisodeID, hints.EpisodeName),
			}
		}
	}

	return result, nil
}

// tmdbToTvdbResult builds a result record. Kept in one place so every branch
// reports the same provenance fields.
func tmdbToTvdbResult(
	tmdbSeriesID, season, episode, tvdbSeriesID int,
	ep tvdb.EpisodeBaseRecord, matchedBy, lookup string,
) *TmdbToTvdbResult {
	return &TmdbToTvdbResult{
		InputTMDBSeriesID: tmdbSeriesID,
		InputSeason:       season,
		InputEpisode:      episode,
		TVDBSeriesID:      tvdbSeriesID,
		TVDBEpisodeID:     ep.ID,
		TVDBSeason:        ep.SeasonNumber,
		TVDBEpisode:       ep.Number,
		TVDBEpisodeName:   ep.Name,
		MatchedBy:         matchedBy,
		TVDBSeriesLookup:  lookup,
	}
}

// findEpisodeByID returns the episode with the given id, or nil.
func findEpisodeByID(episodes []tvdb.EpisodeBaseRecord, id int64) *tvdb.EpisodeBaseRecord {
	for i := range episodes {
		if episodes[i].ID == id {
			return &episodes[i]
		}
	}
	return nil
}

// fetchOrderEpisodes returns a complete episode list for one order.
//
// It pages to exhaustion deliberately: TVDB returns at most 500 episodes per
// page (measured: One Piece 500/500/242), so reading only page 0 silently
// truncates every long-running series -- and a truncated list makes callers
// reject episodes that do exist.
func (m *Mapper) fetchOrderEpisodes(ctx context.Context, seriesID int, order string) ([]tvdb.EpisodeBaseRecord, error) {
	var all []tvdb.EpisodeBaseRecord
	for page := 0; page < m.maxTVDBPages; page++ {
		batch, err := m.tvdb.GetSeriesEpisodes(ctx, seriesID, order, page, nil, nil, nil)
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			return all, nil
		}
		all = append(all, batch...)
	}
	return all, nil
}

// resolveVerifiedTVDBSeries returns the TVDB series for a TMDB series, refusing
// any link whose name does not corroborate. The second return value names the
// lookup route that produced the answer, for provenance.
//
// TVDB's /search/remoteid indexes a bare number across every id namespace, so a
// number that is a valid TMDB id can collide with some other show's id.
// Measured 2026-09-25: TMDB 1433 ("American Dad!") resolves through that search
// to TVDB series 84070, "War and Remembrance", a 1988 WWII miniseries. Using the
// id without checking the name would relabel every American Dad submission onto
// a different series, silently.
//
// TMDB is asked first: its external_ids mapping was correct in 5 of 5 measured
// cases, including the case TVDB's own search gets wrong. It is still checked,
// because that field is user-contributed and can itself go stale.
func (m *Mapper) resolveVerifiedTVDBSeries(ctx context.Context, tmdbSeriesID int) (*tvdb.SeriesBaseRecord, string, error) {
	tmdbName := ""
	if details, err := m.tmdb.GetTVDetails(ctx, tmdbSeriesID); err == nil && details != nil {
		tmdbName = details.Name
	}
	if strings.TrimSpace(tmdbName) == "" {
		return nil, "", fmt.Errorf("cannot verify a tvdb series for tmdb id %d: no tmdb series name to check against", tmdbSeriesID)
	}

	// 1. TMDB's own external_ids mapping.
	if tvdbID, err := m.tmdb.GetTvdbIDFromTmdbID(ctx, tmdbSeriesID); err == nil && tvdbID != 0 {
		if series, err := m.tvdb.GetSeriesExtended(ctx, tvdbID); err == nil && series != nil &&
			corroboratesSeriesName(tmdbName, series.Name, series.Aliases) {
			return &tvdb.SeriesBaseRecord{ID: series.ID, Name: series.Name}, "tmdb_external_ids", nil
		}
	}

	// 2. TVDB's prefixed searches only. The bare-number form is the one that
	//    collides, so it is never used as a fallback here.
	for _, form := range []string{
		fmt.Sprintf("tmdb-%d", tmdbSeriesID),
		fmt.Sprintf("tmdb:%d", tmdbSeriesID),
		fmt.Sprintf("themoviedb-%d", tmdbSeriesID),
		fmt.Sprintf("themoviedb:%d", tmdbSeriesID),
	} {
		series, err := m.tvdb.SearchSeriesByRemoteID(ctx, form)
		if err != nil || series == nil {
			continue
		}
		if namesCorroborate(tmdbName, series.Name) {
			return series, "tvdb_search:" + form, nil
		}
		// The search record carries no aliases, so a title that differs may still be
		// this show filed under another name. One extra fetch, on a path already
		// reached only after the reliable route failed.
		if ext, err := m.tvdb.GetSeriesExtended(ctx, series.ID); err == nil && ext != nil &&
			corroboratesSeriesName(tmdbName, ext.Name, ext.Aliases) {
			return series, "tvdb_search:" + form + ":alias", nil
		}
	}

	return nil, "unverified", fmt.Errorf("no verified tvdb series for tmdb %d: no candidate whose title matches %q (an id match alone is not a mapping)", tmdbSeriesID, tmdbName)
}

// namesCorroborate reports whether two series names are the same title.
// Deliberately tolerant of case and punctuation, deliberately intolerant of
// everything else: this is a wrong-link check, not a fuzzy matcher, and a
// near-miss here means a different show.
func namesCorroborate(a, b string) bool {
	na, nb := normalizeName(a), normalizeName(b)
	return na != "" && na == nb
}

// corroboratesSeriesName reports whether a TMDB series name is one of the names TVDB
// files this series under -- the canonical name OR any alias.
//
// WHY THIS EXISTS. namesCorroborate compares the canonical name only, and that
// rejects CORRECT links for every show TVDB catalogues under its original-language
// title while TMDB carries the English one. Measured against the live databases:
// Attack on Titan, My Hero Academia, Bluey, Archer, Horimiya, Detective Conan,
// Cardcaptor Sakura and Kuroko's Basketball all resolved through TMDB's own
// external_ids and were then rejected on wording alone, so 11 of 60 sampled episodes
// 404'd on the tmdb_id path while the same episode served correctly by tvdb_id.
//
// The gate remains a gate: a name matching neither the canonical title nor any alias
// is still refused, because the risk it guards is real -- TVDB's bare-number
// remote-id search collides with unrelated shows (measured 2026-09-25: TMDB 1433
// "American Dad!" resolves to TVDB 84070, "War and Remembrance", a 1988 miniseries).
func corroboratesSeriesName(want, canonical string, aliases []tvdb.Alias) bool {
	if namesCorroborate(want, canonical) {
		return true
	}
	for _, a := range aliases {
		if namesCorroborate(want, a.Name) {
			return true
		}
	}
	return false
}

type TvdbToTmdbResult struct {
	InputTVDBSeriesID int    `json:"input_tvdb_series_id"`
	InputSeason       int    `json:"input_season"`
	InputEpisode      int    `json:"input_episode"`
	TVDBSeriesID      int    `json:"tvdb_series_id,omitempty"`
	TVDBEpisodeID     int64  `json:"tvdb_episode_id"`
	TVDBEpisodeName   string `json:"tvdb_episode_name"`
	TVDBAirDate       string `json:"tvdb_air_date"`
	TMDBSeriesID      int    `json:"tmdb_series_id"`
	TMDBEpisodeID     int    `json:"tmdb_episode_id"`
	TMDBSeason        int    `json:"tmdb_season"`
	TMDBEpisode       int    `json:"tmdb_episode"`
	MatchedBy         string `json:"matched_by"`

	// Hints echo + provenance. These are additive: a caller that sends no hints
	// keeps the historical result shape, and a caller that does send them can
	// see which one decided the episode and whether the name agreed.
	InputTVDBOrder     string `json:"input_tvdb_order,omitempty"`
	InputTVDBEpisodeID int64  `json:"input_tvdb_episode_id,omitempty"`
	InputEpisodeName   string `json:"input_episode_name,omitempty"`
	// TVDBOrderUsed is the order the resolved TVDB episode record was found
	// under — empty when the episode was resolved from its id alone and its
	// numbers are not addressable under any known order.
	TVDBOrderUsed string `json:"tvdb_order_used,omitempty"`
	// IdentitySource says which input identified the episode:
	// "season_episode_numbers", "tvdb_order", "tvdb_episode_id" or "episode_name".
	IdentitySource string `json:"identity_source,omitempty"`
	// EpisodeNameMatch is "match", "mismatch" or "unknown" for the caller's
	// episode_name against the resolved episode. The mapper reports the fact;
	// deciding what a mismatch means belongs to the caller.
	EpisodeNameMatch string `json:"episode_name_match,omitempty"`
}

type ImdbToTmdbResult struct {
	InputIMDbID      string `json:"input_imdb_id"`
	InputSeason      int    `json:"input_season"`
	InputEpisode     int    `json:"input_episode"`
	TVDBSeriesID     int    `json:"tvdb_series_id"`
	TVDBSeriesName   string `json:"tvdb_series_name"`
	TVDBSeriesLookup string `json:"tvdb_series_lookup"`
	TVDBEpisodeID    int64  `json:"tvdb_episode_id"`
	TVDBEpisodeName  string `json:"tvdb_episode_name"`
	TVDBAirDate      string `json:"tvdb_air_date"`
	TMDBSeriesID     int    `json:"tmdb_series_id"`
	TMDBEpisodeID    int    `json:"tmdb_episode_id"`
	TMDBSeason       int    `json:"tmdb_season"`
	TMDBEpisode      int    `json:"tmdb_episode"`
	MatchedBy        string `json:"matched_by"`

	// Hints echo + provenance — see TvdbToTmdbResult.
	InputTVDBOrder     string `json:"input_tvdb_order,omitempty"`
	InputTVDBEpisodeID int64  `json:"input_tvdb_episode_id,omitempty"`
	InputEpisodeName   string `json:"input_episode_name,omitempty"`
	TVDBOrderUsed      string `json:"tvdb_order_used,omitempty"`
	IdentitySource     string `json:"identity_source,omitempty"`
	EpisodeNameMatch   string `json:"episode_name_match,omitempty"`
}

// ImdbToTmdb maps an IMDb-numbered (season, episode) to the equivalent TMDB
// (season, episode). The mapper resolves the TVDB series from the IMDb id and
// reuses the tvdb->tmdb flow, because TVDB and IMDb usually share the same
// season/episode numbering — so an IMDb-numbered episode's TVDB record (name +
// air date, via TVDB remote ids) is the key that finds it on TMDB.
func (m *Mapper) ImdbToTmdb(ctx context.Context, imdbID string, season int, episode int) (*ImdbToTmdbResult, error) {
	return m.ImdbToTmdbWithHints(ctx, imdbID, 0, season, episode, EpisodeHints{})
}

// ImdbToTmdbWithTMDB maps an IMDb-numbered (season, episode) to the equivalent
// TMDB (season, episode) when the caller already knows the TMDB series id (e.g.
// from a feed-sync mismatches payload). This is the right entry point when the
// TVDB series record has no TMDB remote id — the known id is used directly
// instead of resolving it from TVDB. The TVDB series is still resolved from the
// IMDb id to fetch the episode's name + air date (IMDb and TVDB usually share
// season/episode numbering), which then find the episode on TMDB.
func (m *Mapper) ImdbToTmdbWithTMDB(ctx context.Context, imdbID string, tmdbSeriesID int, season int, episode int) (*ImdbToTmdbResult, error) {
	return m.ImdbToTmdbWithHints(ctx, imdbID, tmdbSeriesID, season, episode, EpisodeHints{})
}

// ImdbToTmdbWithHints is ImdbToTmdbWithTMDB with the caller's episode-level
// hints: the numbers are qualified by tvdb_order, the episode pinned by
// tvdb_episode_id, and the title corroborated by episode_name, exactly as
// TvdbToTmdbWithHints does. With no TMDB series id the mapping continues through
// TvdbToTmdbWithHints (resolving the TMDB series from TVDB's remote ids).
func (m *Mapper) ImdbToTmdbWithHints(ctx context.Context, imdbID string, tmdbSeriesID int, season int, episode int, hints EpisodeHints) (*ImdbToTmdbResult, error) {
	imdbID = strings.TrimSpace(imdbID)
	if imdbID == "" {
		return nil, errors.New("imdb id is required")
	}
	if m.tmdb == nil {
		return nil, errors.New("tmdb bearer token is required for imdb->tmdb mapping")
	}
	validated, err := m.validateHints(hints)
	if err != nil {
		return nil, err
	}
	hints = validated

	tvdbSeries, lookupUsed, err := m.tvdb.FindSeriesByIMDbID(ctx, imdbID)
	if err != nil {
		return nil, err
	}
	if tvdbSeries == nil {
		return nil, fmt.Errorf("tvdb series not found for imdb id %s", imdbID)
	}

	if tmdbSeriesID <= 0 {
		t2t, err := m.TvdbToTmdbWithHints(ctx, tvdbSeries.ID, season, episode, hints)
		if err != nil {
			return nil, fmt.Errorf("imdb %s s%de%d -> tvdb %d: %w", imdbID, season, episode, tvdbSeries.ID, err)
		}
		return imdbResultFromTvdb(imdbID, tvdbSeries, lookupUsed, t2t), nil
	}

	sel, err := m.resolveEpisode(ctx, tvdbSeries.ID, season, episode, hints)
	if err != nil {
		return nil, fmt.Errorf("imdb %s s%de%d -> tvdb %d: %w", imdbID, season, episode, tvdbSeries.ID, err)
	}
	tvdbEp := sel.Episode

	seasonNum, epNum, epID, matchedBy, err := m.scanTMDBSeasons(ctx, tmdbSeriesID, tvdbEp, orderGroupAllowed(sel.Order))
	if err != nil {
		return nil, fmt.Errorf("imdb %s s%de%d -> tvdb %d: %w", imdbID, season, episode, tvdbSeries.ID, err)
	}

	return &ImdbToTmdbResult{
		InputIMDbID:        imdbID,
		InputSeason:        season,
		InputEpisode:       episode,
		TVDBSeriesID:       tvdbSeries.ID,
		TVDBSeriesName:     tvdbSeries.Name,
		TVDBSeriesLookup:   lookupUsed,
		TVDBEpisodeID:      tvdbEp.ID,
		TVDBEpisodeName:    tvdbEp.Name,
		TVDBAirDate:        tvdbEp.Aired,
		TMDBSeriesID:       tmdbSeriesID,
		TMDBEpisodeID:      epID,
		TMDBSeason:         seasonNum,
		TMDBEpisode:        epNum,
		MatchedBy:          matchedBy,
		InputTVDBOrder:     hints.TVDBOrder,
		InputTVDBEpisodeID: hints.TVDBEpisodeID,
		InputEpisodeName:   hints.EpisodeName,
		TVDBOrderUsed:      sel.Order,
		IdentitySource:     sel.Source,
		EpisodeNameMatch:   episodeNameMatch(hints.EpisodeName, tvdbEp.Name),
	}, nil
}

// imdbResultFromTvdb reshapes a tvdb->tmdb result into the imdb->tmdb result,
// carrying the hints' provenance across.
func imdbResultFromTvdb(imdbID string, tvdbSeries *tvdb.SeriesBaseRecord, lookupUsed string, t2t *TvdbToTmdbResult) *ImdbToTmdbResult {
	return &ImdbToTmdbResult{
		InputIMDbID:        imdbID,
		InputSeason:        t2t.InputSeason,
		InputEpisode:       t2t.InputEpisode,
		TVDBSeriesID:       tvdbSeries.ID,
		TVDBSeriesName:     tvdbSeries.Name,
		TVDBSeriesLookup:   lookupUsed,
		TVDBEpisodeID:      t2t.TVDBEpisodeID,
		TVDBEpisodeName:    t2t.TVDBEpisodeName,
		TVDBAirDate:        t2t.TVDBAirDate,
		TMDBSeriesID:       t2t.TMDBSeriesID,
		TMDBEpisodeID:      t2t.TMDBEpisodeID,
		TMDBSeason:         t2t.TMDBSeason,
		TMDBEpisode:        t2t.TMDBEpisode,
		MatchedBy:          t2t.MatchedBy,
		InputTVDBOrder:     t2t.InputTVDBOrder,
		InputTVDBEpisodeID: t2t.InputTVDBEpisodeID,
		InputEpisodeName:   t2t.InputEpisodeName,
		TVDBOrderUsed:      t2t.TVDBOrderUsed,
		IdentitySource:     t2t.IdentitySource,
		EpisodeNameMatch:   t2t.EpisodeNameMatch,
	}
}

// TvdbToTmdb maps a TVDB-numbered (season, episode) to the equivalent TMDB
// episode with no hints: the caller's numbers are read under the mapper's
// primary season type and then its fallbacks — the historical behaviour.
func (m *Mapper) TvdbToTmdb(ctx context.Context, tvdbSeriesID int, season int, episode int) (*TvdbToTmdbResult, error) {
	return m.TvdbToTmdbWithHints(ctx, tvdbSeriesID, season, episode, EpisodeHints{})
}

// TvdbToTmdbWithHints maps a TVDB episode to TMDB using the caller's
// episode-level hints. Episode identity is resolved most-specific-first:
//
//  1. an episode id — unique across every TVDB order, so it identifies the
//     episode exactly, and the order it is found under is the order that defines
//     the caller's numbers;
//  2. an order name — the caller's season/episode are read under that order
//     only, with no fallback (the caller said what the numbers mean);
//  3. the numbers alone — primary season type, then fallbacks, as before;
//  4. the episode name — corroboration at every step, and a resolver of last
//     resort when nothing else identifies an episode and the name matches
//     exactly one episode of the series.
//
// The resolved TVDB record is then located on TMDB by the existing remote-id /
// name / air-date cascade. The caller's episode name is compared against the
// resolved episode and reported as EpisodeNameMatch ("match" / "mismatch" /
// "unknown"); the mapper reports that fact and leaves the policy to the caller.
func (m *Mapper) TvdbToTmdbWithHints(ctx context.Context, tvdbSeriesID int, season int, episode int, hints EpisodeHints) (*TvdbToTmdbResult, error) {
	validated, err := m.validateHints(hints)
	if err != nil {
		return nil, err
	}
	hints = validated

	sel, err := m.resolveEpisode(ctx, tvdbSeriesID, season, episode, hints)
	if err != nil {
		return nil, err
	}
	tvdbEp := sel.Episode

	ext, err := m.tvdb.GetEpisodeExtended(ctx, tvdbEp.ID)
	if err != nil {
		return nil, err
	}

	tmdbEpisodeID := findRemoteNumericID(ext.RemoteIDs, []string{"moviedb", "tmdb"})
	tmdbSeriesID := 0

	seriesExt, err := m.tvdb.GetSeriesExtended(ctx, tvdbSeriesID)
	if err == nil && seriesExt != nil {
		tmdbSeriesID = findRemoteNumericID(seriesExt.RemoteIDs, []string{"moviedb", "tmdb"})
	}

	resolvedSeriesID := tvdbSeriesID
	if tvdbEp.SeriesID != 0 {
		resolvedSeriesID = tvdbEp.SeriesID
	}

	res := &TvdbToTmdbResult{
		InputTVDBSeriesID:  tvdbSeriesID,
		InputSeason:        season,
		InputEpisode:       episode,
		TVDBSeriesID:       resolvedSeriesID,
		TVDBEpisodeID:      tvdbEp.ID,
		TVDBEpisodeName:    tvdbEp.Name,
		TVDBAirDate:        tvdbEp.Aired,
		TMDBSeriesID:       tmdbSeriesID,
		TMDBEpisodeID:      tmdbEpisodeID,
		InputTVDBOrder:     hints.TVDBOrder,
		InputTVDBEpisodeID: hints.TVDBEpisodeID,
		InputEpisodeName:   hints.EpisodeName,
		TVDBOrderUsed:      sel.Order,
		IdentitySource:     sel.Source,
		EpisodeNameMatch:   episodeNameMatch(hints.EpisodeName, tvdbEp.Name),
	}

	if m.tmdb == nil {
		if tmdbEpisodeID != 0 || tmdbSeriesID != 0 {
			res.MatchedBy = "tvdb_remote_ids"
			return res, nil
		}
		return nil, errors.New("tmdb bearer token is required for tvdb->tmdb mapping when remote ids are missing")
	}

	if tmdbEpisodeID != 0 {
		byID, err := m.tmdb.GetEpisodeByID(ctx, tmdbEpisodeID)
		// A stale TVDB remote id points at a TMDB episode that no longer exists:
		// measured 2026-09-25, Futurama's specials carry ids 13253 and 15060 and
		// both 404. Aborting on that refuses to answer a question the name and
		// air date can still settle, so fall through to the season scan instead.
		// A dead pointer is not evidence that the episode cannot be mapped.
		if err == nil && byID != nil {
			res.TMDBSeason = byID.SeasonNumber
			res.TMDBEpisode = byID.EpisodeNumber
			if byID.ShowID != 0 {
				res.TMDBSeriesID = byID.ShowID
			}
			res.MatchedBy = "tvdb_episode_remote_id"
			return res, nil
		}
	}

	if tmdbSeriesID == 0 {
		return nil, errors.New("tvdb series remote ids did not include a tmdb id")
	}

	if strings.TrimSpace(tvdbEp.Aired) == "" && strings.TrimSpace(tvdbEp.Name) == "" {
		return nil, errors.New("tvdb episode missing both air date and name; cannot scan tmdb seasons")
	}

	seasonNum, epNum, epID, matchedBy, err := m.scanTMDBSeasons(ctx, tmdbSeriesID, tvdbEp, orderGroupAllowed(sel.Order))
	if err != nil {
		return nil, err
	}
	res.TMDBSeason = seasonNum
	res.TMDBEpisode = epNum
	res.TMDBEpisodeID = epID
	res.MatchedBy = matchedBy
	return res, nil
}

// scanTMDBSeasons finds the TMDB (season, episode) matching a TVDB episode by
// scanning every season of the given TMDB series with matchSeasonEpisode.
// allowOrderGroup enables the TMDB "TVDB Order" episode group as a last-resort
// fallback; it only carries meaning for TVDB default/official numbering, so a
// caller resolving under another order must pass false.
func (m *Mapper) scanTMDBSeasons(ctx context.Context, tmdbSeriesID int, tvdbEp tvdb.EpisodeBaseRecord, allowOrderGroup bool) (seasonNum int, epNum int, epID int, matchedBy string, err error) {
	tv, err := m.tmdb.GetTVDetails(ctx, tmdbSeriesID)
	if err != nil {
		return 0, 0, 0, "", err
	}

	limit := tv.NumberOfSeasons
	if limit > m.maxTMDBSeasons {
		limit = m.maxTMDBSeasons
	}

	// TMDB keeps specials in season 0, so a scan that starts at season 1 can
	// never place a TVDB special: its TMDB counterpart is in a season the loop
	// does not visit, and the episode fails with "unable to map" however well
	// the name and air date line up.
	//
	// The order is chosen so this stays additive. A TVDB special looks in TMDB
	// season 0 first, so a same-named episode in a regular season cannot capture
	// it; every other episode looks in season 0 last, so season 0 can only turn
	// a previous failure into a match and never changes an existing one.
	seasons := make([]int, 0, limit+1)
	if tvdbEp.SeasonNumber == 0 {
		seasons = append(seasons, 0)
	}
	for seasonNumber := 1; seasonNumber <= limit; seasonNumber++ {
		seasons = append(seasons, seasonNumber)
	}
	if tvdbEp.SeasonNumber != 0 {
		seasons = append(seasons, 0)
	}

	for _, seasonNum := range seasons {
		seasonEps, err := m.tmdb.GetSeasonEpisodes(ctx, tmdbSeriesID, seasonNum)
		if err != nil {
			continue
		}

		if epNum, epID, matchedBy := matchSeasonEpisode(tvdbEp, seasonEps); matchedBy != "" {
			return seasonNum, epNum, epID, matchedBy, nil
		}
	}

	// Fallback: TMDB's own "TVDB Order" episode group maps TVDB season/episode
	// positions directly to TMDB episodes. Use it when the name/air-date scan
	// fails (e.g. TVDB's air date is off by more than a day, or the names are
	// in different languages). The group's season bucket order is the TVDB
	// season number; the episode's 0-based position within the bucket is the
	// TVDB episode number - 1. The mapped TMDB season/episode numbers and id
	// come from the group entry itself.
	if allowOrderGroup {
		if epID, tmdbSeason, tmdbEpisode := m.mapViaTVDBOrderGroup(ctx, tmdbSeriesID, tvdbEp); epID != 0 {
			return tmdbSeason, tmdbEpisode, epID, "tvdb_order_group", nil
		}
	}

	return 0, 0, 0, "", fmt.Errorf("unable to map tvdb episode %q (aired %s) to tmdb %d", tvdbEp.Name, tvdbEp.Aired, tmdbSeriesID)
}

// mapViaTVDBOrderGroup tries TMDB's "TVDB Order" episode group for the series
// and returns the TMDB episode id/season/episode for the given TVDB episode,
// or 0 when the group doesn't exist or lacks the episode.
func (m *Mapper) mapViaTVDBOrderGroup(ctx context.Context, tmdbSeriesID int, tvdbEp tvdb.EpisodeBaseRecord) (epID int, tmdbSeason int, tmdbEpisode int) {
	groups, err := m.tmdb.GetEpisodeGroups(ctx, tmdbSeriesID)
	if err != nil {
		return 0, 0, 0
	}

	var groupID string
	for _, g := range groups {
		name := strings.ToLower(g.Name)
		if strings.Contains(name, "tvdb") {
			groupID = g.ID
			break
		}
	}
	if groupID == "" {
		return 0, 0, 0
	}

	detail, err := m.tmdb.GetEpisodeGroup(ctx, groupID)
	if err != nil {
		return 0, 0, 0
	}

	for _, bucket := range detail.Groups {
		if epID, season, episode := lookupInGroupBucket(bucket, tvdbEp); epID != 0 {
			return epID, season, episode
		}
	}
	return 0, 0, 0
}

// lookupInGroupBucket finds the TMDB episode in one group bucket that matches a
// TVDB episode. The bucket's Order is the TVDB season number (a mismatched
// bucket never matches); each entry's 0-based Order is the TVDB episode number
// - 1. Returns the mapped TMDB episode id/season/episode, or 0 when not
// present.
func lookupInGroupBucket(bucket tmdb.EpisodeGroupOrder, tvdbEp tvdb.EpisodeBaseRecord) (epID int, season int, episode int) {
	if bucket.Order != tvdbEp.SeasonNumber {
		return 0, 0, 0
	}
	for _, entry := range bucket.Episodes {
		if entry.Order == tvdbEp.Number-1 {
			return entry.ID, entry.SeasonNumber, entry.EpisodeNumber
		}
	}
	return 0, 0, 0
}

// matchSeasonEpisode picks the TMDB episode that best corresponds to a TVDB
// episode within one season's episodes, and returns its episode number, its
// TMDB episode id, and the matching method ("" when nothing matches).
// Priority, most specific first:
//
//  1. exact normalized name — the strongest signal;
//  2. air date AND episode number aligned — preferred over a bare air-date
//     match because when a whole season shares one air date (e.g. a binge-drop
//     release) a date-only scan would always return episode 1. Aligning on the
//     number too selects the correct episode when the TVDB and TMDB orderings
//     agree, which is the common case;
//  3. bare air date — fallback for orderings that genuinely differ from TMDB
//     (reordered episodes where numbers don't align).
func matchSeasonEpisode(tvdbEp tvdb.EpisodeBaseRecord, seasonEps []tmdb.SeasonEpisode) (epNum int, epID int, matchedBy string) {
	// 1) Name match — the strongest signal.
	for _, ep := range seasonEps {
		if strings.TrimSpace(tvdbEp.Name) != "" && TitlesMatch(tvdbEp.Name, ep.Name) {
			return ep.EpisodeNumber, ep.ID, "name_scan"
		}
	}
	// 2) Air date AND episode-number alignment. The date comparison tolerates a
	// one-day difference: TVDB often records the Japanese broadcast date while
	// TMDB uses the local one, so the same episode can be 2023-09-28 on TVDB and
	// 2023-09-29 on TMDB.
	for _, ep := range seasonEps {
		if strings.TrimSpace(tvdbEp.Aired) != "" && strings.TrimSpace(ep.AirDate) != "" &&
			datesWithinOneDay(tvdbEp.Aired, ep.AirDate) && ep.EpisodeNumber == tvdbEp.Number {
			return ep.EpisodeNumber, ep.ID, "air_date+number_scan"
		}
	}
	// 3) Bare air-date match (same one-day tolerance).
	for _, ep := range seasonEps {
		if strings.TrimSpace(tvdbEp.Aired) != "" && strings.TrimSpace(ep.AirDate) != "" && datesWithinOneDay(tvdbEp.Aired, ep.AirDate) {
			return ep.EpisodeNumber, ep.ID, "air_date_scan"
		}
	}
	return 0, 0, ""
}

// titlesMatch compares two episode titles, tolerating the series-name prefix
// TVDB puts on specials.
//
// TVDB calls the Futurama films "Futurama: Bender's Big Score" where TMDB says
// "Bender's Big Score", so an exact comparison misses an episode that is plainly
// the same one. Missing it is not harmless: the next rule down is
// air_date+number, and TMDB numbers the specials differently from TVDB (TVDB
// S0E2 is the first film, TMDB S0E1 is), so the number rule answers
// "Everybody Loves Hypnotoad" -- a different special sharing the same air date.
// A wrong episode with no error is worse than no answer.
//
// Exported because the API repo's backlog migration must compare titles by this
// same rule: TVDB's specials carry the series prefix in storage too, so a
// migration using a plain equality check would fail to label them and would
// leave the very episodes this rule exists to place.
func TitlesMatch(want, got string) bool {
	if normalizeName(want) == normalizeName(got) {
		return true
	}

	// Retry with a "Prefix:" segment removed, but only when the colon is
	// actually there -- an exact comparison stays the rule for ordinary titles,
	// so this cannot turn "The End" into a match for "The Beginning of the End".
	wantStripped, gotStripped := stripTitlePrefix(want), stripTitlePrefix(got)
	if wantStripped == want && gotStripped == got {
		return false
	}
	return normalizeName(wantStripped) == normalizeName(gotStripped)
}

// stripTitlePrefix removes a leading "Something: " segment from a title.
func stripTitlePrefix(title string) string {
	if idx := strings.Index(title, ":"); idx > 0 && idx < len(title)-1 {
		return strings.TrimSpace(title[idx+1:])
	}
	return title
}

// datesWithinOneDay reports whether two YYYY-MM-DD dates are the same day or
// exactly one day apart (TVDB vs TMDB air-date skew).
func datesWithinOneDay(a, b string) bool {
	ta, errA := time.Parse("2006-01-02", strings.TrimSpace(a))
	tb, errB := time.Parse("2006-01-02", strings.TrimSpace(b))
	if errA != nil || errB != nil {
		return false
	}
	d := ta.Sub(tb)
	return d >= -24*time.Hour && d <= 24*time.Hour
}

// episodeNames renders candidate titles for an ambiguity error, so the message
// names the episodes that collided instead of just their count.
func episodeNames(eps []tvdb.EpisodeBaseRecord) string {
	parts := make([]string, 0, len(eps))
	for i := range eps {
		parts = append(parts, fmt.Sprintf("s%de%d %q", eps[i].SeasonNumber, eps[i].Number, eps[i].Name))
	}
	return strings.Join(parts, "; ")
}

func pickBestByName(eps []tvdb.EpisodeBaseRecord, wantName string) *tvdb.EpisodeBaseRecord {
	if len(eps) == 0 {
		return nil
	}

	if strings.TrimSpace(wantName) == "" {
		return &eps[0]
	}

	// TitlesMatch, not normalized equality: TVDB prefixes specials with the series
	// name ("Futurama: Bender's Game"), and plain equality rejects those. Measured:
	// the name tiebreaker could never resolve the very specials it exists for,
	// because "Bender's Game" != "Futurama: Bender's Game" under equality.
	for i := range eps {
		if TitlesMatch(wantName, eps[i].Name) {
			return &eps[i]
		}
	}

	for i := range eps {
		if strings.EqualFold(strings.TrimSpace(eps[i].Name), strings.TrimSpace(wantName)) {
			return &eps[i]
		}
	}

	return nil
}

func normalizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (m *Mapper) seasonTypesTried() string {
	types := []string{m.tvdbSeasonType}
	types = append(types, m.fallbackSeasonTypes...)
	return strings.Join(types, ", ")
}

// ---------------------------------------------------------------------------
// Episode identity hints
//
// A caller's (season, episode) pair only means something relative to a numbering
// order: TVDB `alternate` s6e1 is "Bender's Big Score (1)" while `official`
// s6e1 is "Rebirth", and both are real episodes of the same series. The hints
// below let a caller say which episode it means in a way that survives that.
// ---------------------------------------------------------------------------

// EpisodeHints carries a caller's episode-level identity add-ons. Every field is
// optional; season/episode are always required and are what the hints qualify.
type EpisodeHints struct {
	// TVDBOrder names the TVDB order the caller's season/episode numbers belong
	// to ("official", "dvd", "absolute", "alternate", "regional", "default",
	// plus the Jellyfin displayorder aliases). Empty means "not supplied".
	TVDBOrder string
	// TVDBEpisodeID is a TVDB episode id. TVDB episode ids are unique across all
	// orders, so an id identifies the episode with no numbering needed.
	TVDBEpisodeID int64
	// EpisodeName is the caller's episode title, used as corroboration.
	EpisodeName string
}

// Identity sources reported on the result.
const (
	IdentitySourceNumbers   = "season_episode_numbers"
	IdentitySourceOrder     = "tvdb_order"
	IdentitySourceEpisodeID = "tvdb_episode_id"
	IdentitySourceName      = "episode_name"
)

// Episode-name comparison outcomes.
const (
	NameMatchMatch    = "match"
	NameMatchMismatch = "mismatch"
	NameMatchUnknown  = "unknown"
)

// EpisodeHintError reports hints that contradict each other, or an unknown
// order value. An episode id that disagrees with the episode found at the
// caller's numbers under a resolvable order cannot be honoured two ways at once,
// so it is an error rather than a silent pick.
type EpisodeHintError struct {
	Field  string
	Detail string
}

func (e *EpisodeHintError) Error() string { return e.Field + ": " + e.Detail }

// tvdbOrderAliases maps every accepted spelling to the TVDB season-type name
// used by /series/{id}/episodes/{type}.
var tvdbOrderAliases = map[string]string{
	"default":         "default",
	"defaultorder":    "default",
	"official":        "official",
	"officialorder":   "official",
	"aired":           "official",
	"airedorder":      "official",
	"dvd":             "dvd",
	"altdvd":          "dvd",
	"dvdorder":        "dvd",
	"absolute":        "absolute",
	"absoluteorder":   "absolute",
	"abso":            "absolute",
	"alternate":       "alternate",
	"alternateorder":  "alternate",
	"alttwo":          "alternate",
	"streaming":       "alternate",
	"streamingorder":  "alternate",
	"regional":        "regional",
	"regionalorder":   "regional",
	"production":      "regional",
	"productionorder": "regional",
}

// TVDBOrders lists the order names searched when an episode id has to be located
// without an order hint, in preference order.
var TVDBOrders = []string{"default", "official", "dvd", "alternate", "absolute", "regional"}

// NormalizeTVDBOrder resolves an order spelling to the TVDB season-type name. It
// accepts TVDB's own names and the Jellyfin displayorder vocabulary
// (Series.DisplayOrder: official, regional, alternate, altdvd, dvd, absolute,
// alttwo), ignoring case, spaces, dashes and underscores.
func NormalizeTVDBOrder(name string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(name))
	key = strings.NewReplacer(" ", "", "-", "", "_", "").Replace(key)
	order, ok := tvdbOrderAliases[key]
	return order, ok
}

// identitySelection is the TVDB episode record a hint set resolved, plus how.
type identitySelection struct {
	Episode tvdb.EpisodeBaseRecord
	// Order is the order the record was found under; empty when the episode was
	// resolved from its id alone and its numbers exist under no known order.
	Order  string
	Source string
}

// orderCandidate is one TVDB episode record found at the caller's numbers under
// one order.
type orderCandidate struct {
	Order   string
	Episode tvdb.EpisodeBaseRecord
}

// selectEpisodeByHints picks the candidate the caller's hints identify.
//
// Precedence: an episode id identifies the episode exactly (the order it was
// found under is then the order that defines the caller's numbers); otherwise
// the candidates are already in preference order and the first one wins, which
// reproduces the historical "primary season type, then fallbacks" behaviour.
func selectEpisodeByHints(candidates []orderCandidate, hints EpisodeHints, season, episode int) (orderCandidate, error) {
	if len(candidates) == 0 {
		return orderCandidate{}, &EpisodeHintError{Field: "season/episode", Detail: "no candidate episodes"}
	}
	if hints.TVDBEpisodeID != 0 {
		for _, c := range candidates {
			if c.Episode.ID == hints.TVDBEpisodeID {
				return c, nil
			}
		}
		orders := make([]string, 0, len(candidates))
		for _, c := range candidates {
			orders = append(orders, fmt.Sprintf("%s=%d", c.Order, c.Episode.ID))
		}
		return orderCandidate{}, &EpisodeHintError{
			Field: "tvdb_episode_id",
			Detail: fmt.Sprintf("episode id %d is not the episode at season %d episode %d under any order (%s)",
				hints.TVDBEpisodeID, season, episode, strings.Join(orders, ", ")),
		}
	}
	return candidates[0], nil
}

// candidatesForHints fetches the episode records at the caller's numbers under
// every order worth trying, in preference order. A 404 or empty answer means
// "not under this order" (TVDB 404s an order the show has no episodes under), so
// it falls through; a real error is remembered but does not stop the scan, and
// is returned only when nothing matched at all.
func (m *Mapper) candidatesForHints(ctx context.Context, seriesID, season, episode int, hints EpisodeHints) ([]orderCandidate, error, error) {
	orders := m.ordersForHints(hints)
	candidates := make([]orderCandidate, 0, len(orders))
	var firstErr error
	for _, order := range orders {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		s, e := season, episode
		eps, err := m.tvdb.GetSeriesEpisodes(ctx, seriesID, order, 0, &s, &e, nil)
		if err != nil {
			var nf *tvdb.NotFoundError
			if errors.As(err, &nf) {
				continue
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, ep := range eps {
			candidates = append(candidates, orderCandidate{Order: order, Episode: ep})
		}
	}
	return candidates, firstErr, nil
}

// ordersForHints decides which orders to read the caller's numbers under.
// An explicit order is used alone — the caller said what the numbers mean, so
// falling back to another order would answer a different question. An episode id
// with no order must be locatable, so every order is worth a look (primary
// first, so the common case stays one request).
func (m *Mapper) ordersForHints(hints EpisodeHints) []string {
	if hints.TVDBOrder != "" {
		return []string{hints.TVDBOrder}
	}
	if hints.TVDBEpisodeID != 0 {
		orders := []string{m.tvdbSeasonType}
		orders = append(orders, m.fallbackSeasonTypes...)
		for _, o := range TVDBOrders {
			if !containsString(orders, o) {
				orders = append(orders, o)
			}
		}
		return orders
	}
	return append([]string{m.tvdbSeasonType}, m.fallbackSeasonTypes...)
}

// resolveEpisode turns a hint set plus numbers into one TVDB episode record.
func (m *Mapper) resolveEpisode(ctx context.Context, tvdbSeriesID, season, episode int, hints EpisodeHints) (identitySelection, error) {
	candidates, firstErr, err := m.candidatesForHints(ctx, tvdbSeriesID, season, episode, hints)
	if err != nil {
		return identitySelection{}, err
	}

	if len(candidates) > 0 {
		sel, err := selectEpisodeByHints(candidates, hints, season, episode)
		if err != nil {
			return identitySelection{}, err
		}
		source := IdentitySourceNumbers
		switch {
		case hints.TVDBEpisodeID != 0:
			source = IdentitySourceEpisodeID
		case hints.TVDBOrder != "":
			source = IdentitySourceOrder
		}
		return identitySelection{Episode: sel.Episode, Order: sel.Order, Source: source}, nil
	}

	// Nothing addressable at the caller's numbers under any order tried. An
	// episode id is still authoritative (it is unique across every order), and a
	// name is too when it matches exactly one episode of the series.
	if hints.TVDBEpisodeID != 0 {
		ep, err := m.tvdb.GetEpisodeExtended(ctx, hints.TVDBEpisodeID)
		if err != nil {
			return identitySelection{}, err
		}
		if ep.SeriesID != 0 && tvdbSeriesID != 0 && ep.SeriesID != tvdbSeriesID {
			return identitySelection{}, &EpisodeHintError{
				Field: "tvdb_episode_id",
				Detail: fmt.Sprintf("episode id %d belongs to series %d, not %d",
					hints.TVDBEpisodeID, ep.SeriesID, tvdbSeriesID),
			}
		}
		return identitySelection{
			Episode: tvdb.EpisodeBaseRecord{
				ID: ep.ID, Name: ep.Name, Aired: ep.Aired, SeriesID: ep.SeriesID,
				SeasonNumber: ep.SeasonNumber, Number: ep.Number,
			},
			Source: IdentitySourceEpisodeID,
		}, nil
	}

	if strings.TrimSpace(hints.EpisodeName) != "" {
		ep, err := m.findEpisodeByName(ctx, tvdbSeriesID, hints.EpisodeName)
		if err != nil {
			return identitySelection{}, err
		}
		if ep != nil {
			return identitySelection{Episode: *ep, Source: IdentitySourceName}, nil
		}
	}

	if firstErr != nil {
		return identitySelection{}, firstErr
	}
	return identitySelection{}, fmt.Errorf("tvdb episode not found: series=%d season=%d episode=%d (tried season types: %s)",
		tvdbSeriesID, season, episode, m.seasonTypesTried())
}

// findEpisodeByName locates the series episode whose name matches exactly,
// scanning the known orders within a bounded page budget. No match returns nil;
// more than one match is an error rather than a first-hit pick, because a first
// hit silently returns the wrong episode when names repeat (the shared-air-date
// lesson). Matches are deduped by episode id — the same episode appears in every
// order that contains it.
func (m *Mapper) findEpisodeByName(ctx context.Context, seriesID int, name string) (*tvdb.EpisodeBaseRecord, error) {
	want := normalizeName(name)
	if want == "" {
		return nil, nil
	}

	budget := m.maxTVDBPages
	seen := map[int64]bool{}
	var matches []tvdb.EpisodeBaseRecord

	for _, order := range TVDBOrders {
		for page := 0; page < m.maxTVDBPages && budget > 0; page++ {
			eps, err := m.tvdb.GetSeriesEpisodes(ctx, seriesID, order, page, nil, nil, nil)
			if err != nil || len(eps) == 0 {
				break
			}
			budget--
			for _, ep := range eps {
				if ep.ID != 0 && normalizeName(ep.Name) == want && !seen[ep.ID] {
					seen[ep.ID] = true
					matches = append(matches, ep)
				}
			}
		}
		if budget <= 0 {
			break
		}
	}

	switch len(matches) {
	case 0:
		return nil, nil
	case 1:
		return &matches[0], nil
	default:
		ids := make([]string, 0, len(matches))
		for _, ep := range matches {
			ids = append(ids, strconv.FormatInt(ep.ID, 10))
		}
		return nil, fmt.Errorf("episode name %q matches %d episodes (ids: %s); supply tvdb_order or an episode id",
			name, len(matches), strings.Join(ids, ", "))
	}
}

// validateHints normalizes the order spelling and rejects an unknown one, and
// trims the name.
func (m *Mapper) validateHints(hints EpisodeHints) (EpisodeHints, error) {
	if strings.TrimSpace(hints.TVDBOrder) != "" {
		order, ok := NormalizeTVDBOrder(hints.TVDBOrder)
		if !ok {
			return hints, &EpisodeHintError{
				Field: "tvdb_order",
				Detail: fmt.Sprintf("unknown order %q (accepted: official, dvd, absolute, alternate, regional, default, altdvd, alttwo)",
					strings.TrimSpace(hints.TVDBOrder)),
			}
		}
		hints.TVDBOrder = order
	}
	hints.EpisodeName = strings.TrimSpace(hints.EpisodeName)
	return hints, nil
}

// episodeNameMatch compares the caller's episode title with the resolved one on
// normalized names. A missing side is "unknown", never a mismatch: a caller that
// cannot supply a name, or an episode with no upstream name, must not be treated
// as disagreeing.
// episodeNameMatch reports whether the caller's title agrees with the resolved
// one. It must use TitlesMatch -- the same rule the matching logic uses -- or the
// two disagree and the reported verdict is wrong.
//
// Measured before this change: submitting episode_name "Bender's Game" against
// TVDB's "Futurama: Bender's Game" reported MISMATCH, because equality does not
// see through TVDB's series-name prefix. A caller that got the right episode and
// named it correctly was told it contradicted itself, which turns any policy of
// "reject on mismatch" into a false rejection of correct submissions.
func episodeNameMatch(want, got string) string {
	w := normalizeName(want)
	g := normalizeName(got)
	if w == "" || g == "" {
		return NameMatchUnknown
	}
	if TitlesMatch(want, got) {
		return NameMatchMatch
	}
	return NameMatchMismatch
}

// orderGroupAllowed reports whether TMDB's own "TVDB Order" episode group may be
// used as a last-resort fallback for a record found under this order. The group
// is keyed by TVDB default/official season and episode positions, so it carries
// no meaning for another order — and no meaning at all when the order is unknown.
func orderGroupAllowed(order string) bool {
	return order == "" || order == "default" || order == "official"
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func findRemoteNumericID(ids []tvdb.RemoteID, hints []string) int {
	for _, id := range ids {
		name := strings.ToLower(strings.TrimSpace(id.SourceName))
		for _, hint := range hints {
			if strings.Contains(name, hint) {
				if n, err := strconv.Atoi(strings.TrimSpace(id.ID)); err == nil && n != 0 {
					return n
				}
			}
		}
	}
	return 0
}
