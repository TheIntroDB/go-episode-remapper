// SPDX-License-Identifier: MIT
// Copyright (C) 2026 TheIntroDB

package remap

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Pasithea0/go-tmdb-episodes-fix/pkg/tvdb"
)

// IMDbOrderPreference is the order in which TVDB orders are tried when deciding
// which one carries IMDb's numbering.
//
// "alternate" comes first because IMDb episodes usually follow TVDB's alternate
// order. It is not always available: TVDB 404s an order a series has none of, and
// even when it exists it may not list the episode. "default" is next, because a
// series with no alternate ordering (or one the caller never had remapped) uses
// the default numbering everywhere. The rest follow so that a series whose
// numbering only exists under, say, "absolute" still answers.
var IMDbOrderPreference = []string{"alternate", "default", "absolute", "official", "dvd", "regional"}

// TvdbToImdbResult is a TVDB episode expressed in the numbering IMDb uses for it.
//
// Every coordinate here is DERIVED from TVDB, not read from IMDb: IMDb publishes
// no episode-numbering API, and nothing in this module's dependency chain exposes
// IMDb episode numbers. IMDbOrder names the TVDB order the numbers came from and
// MatchedBy how the episode was identified inside it, so a caller can decide
// whether to trust them. Treat this as evidence with provenance, never as an
// authoritative IMDb lookup.
type TvdbToImdbResult struct {
	InputTVDBSeriesID int `json:"input_tvdb_series_id"`
	InputSeason       int `json:"input_season"`
	InputEpisode      int `json:"input_episode"`

	TVDBSeriesID    int    `json:"tvdb_series_id"`
	TVDBEpisodeID   int64  `json:"tvdb_episode_id"`
	TVDBEpisodeName string `json:"tvdb_episode_name"`
	TVDBAirDate     string `json:"tvdb_air_date"`

	// IMDbSeason/IMDbEpisode are the episode's coordinates in the order named by
	// IMDbOrder.
	IMDbSeason  int `json:"imdb_season"`
	IMDbEpisode int `json:"imdb_episode"`

	// IMDbOrder is the TVDB order those coordinates came from; MatchedBy is how
	// the episode was identified inside it (tvdb_episode_id | name |
	// name_loose | air_date+name | air_date+number | air_date |
	// assumed_same_coordinates).
	IMDbOrder string `json:"imdb_order"`
	MatchedBy string `json:"matched_by"`
	// NumbersDiffer reports whether the answer moved the episode relative to the
	// caller's input numbers. False means the remake was a no-op for this
	// episode, which is the common case and not an error.
	NumbersDiffer bool `json:"numbers_differ"`

	// Provenance, mirroring the other directions.
	InputTVDBOrder     string   `json:"input_tvdb_order,omitempty"`
	InputTVDBEpisodeID int64    `json:"input_tvdb_episode_id,omitempty"`
	InputEpisodeName   string   `json:"input_episode_name,omitempty"`
	TVDBOrderUsed      string   `json:"tvdb_order_used,omitempty"`
	IdentitySource     string   `json:"identity_source,omitempty"`
	EpisodeNameMatch   string   `json:"episode_name_match,omitempty"`
	OrdersTried        []string `json:"orders_tried,omitempty"`
}

// Evidence ranks for locating an episode inside one order's episode list. A TVDB
// episode id is unique across every order, so it dominates. An exact normalised
// name match is stronger than the prefix-tolerant matcher, which is in turn
// stronger than air-date evidence: a binge drop puts many episodes on one date,
// so a date alone is the weakest thing that can still identify an episode.
const (
	imdbScoreEpisodeID     = 6
	imdbScoreNameExact     = 5
	imdbScoreNameLoose     = 4
	imdbScoreAirDateName   = 3
	imdbScoreAirDateNumber = 2
	imdbScoreAirDate       = 1
)

// imdbOrderCandidate is one order's complete episode list.
type imdbOrderCandidate struct {
	Order    string
	Episodes []tvdb.EpisodeBaseRecord
}

// imdbOrderMatch is an episode located inside one order.
type imdbOrderMatch struct {
	Order     string
	Season    int
	Episode   int
	MatchedBy string
	score     int
}

// locateEpisodeInOrder finds ep inside one order's episode list, strongest
// evidence first, and returns the coordinates it occupies there.
//
// ok=false means "this order does not contain that episode", which is a normal
// answer -- a series with no alternate order 404s it, and an alternate order that
// exists may still have a different episode at these numbers. Returning false lets
// the caller move to the next order instead of being handed a wrong episode.
func locateEpisodeInOrder(ep tvdb.EpisodeBaseRecord, order string, episodes []tvdb.EpisodeBaseRecord) (imdbOrderMatch, bool) {
	if len(episodes) == 0 {
		return imdbOrderMatch{}, false
	}

	// 1. Episode id: unique across orders, so an id hit is exact identity with no
	//    numbering involved.
	if ep.ID != 0 {
		if found := findEpisodeByID(episodes, ep.ID); found != nil {
			return imdbOrderMatch{Order: order, Season: found.SeasonNumber, Episode: found.Number,
				MatchedBy: "tvdb_episode_id", score: imdbScoreEpisodeID}, true
		}
	}

	// 2. Name. An exact normalised match first; two episodes sharing it is
	//    ambiguous, so it is refused rather than picked between.
	if want := strings.TrimSpace(ep.Name); want != "" {
		wantNorm := normalizeName(want)
		var exact *tvdb.EpisodeBaseRecord
		ambiguous := false
		for i := range episodes {
			if wantNorm != "" && normalizeName(episodes[i].Name) == wantNorm {
				if exact != nil {
					ambiguous = true
					break
				}
				exact = &episodes[i]
			}
		}
		if exact != nil && !ambiguous {
			return imdbOrderMatch{Order: order, Season: exact.SeasonNumber, Episode: exact.Number,
				MatchedBy: "name", score: imdbScoreNameExact}, true
		}
		// A loose match is only worth trying when the exact one was absent, not
		// when it was ambiguous: pickBestByName returns the first hit, so falling
		// through here would pick between two identically-named episodes, which is
		// the guess this cascade exists to avoid.
		if !ambiguous {
			if best := pickBestByName(episodes, want); best != nil {
				return imdbOrderMatch{Order: order, Season: best.SeasonNumber, Episode: best.Number,
					MatchedBy: "name_loose", score: imdbScoreNameLoose}, true
			}
		}
	}

	// 3. Air date. A single episode on that date is decent evidence; several means
	//    a binge drop, where only the episode number can break the tie, and only
	//    when this order numbers within the season at all.
	if aired := strings.TrimSpace(ep.Aired); aired != "" {
		var same []tvdb.EpisodeBaseRecord
		for i := range episodes {
			if strings.TrimSpace(episodes[i].Aired) == aired {
				same = append(same, episodes[i])
			}
		}
		switch {
		case len(same) == 1:
			return imdbOrderMatch{Order: order, Season: same[0].SeasonNumber, Episode: same[0].Number,
				MatchedBy: "air_date", score: imdbScoreAirDate}, true
		case len(same) > 1:
			var byNumber *tvdb.EpisodeBaseRecord
			for i := range same {
				if same[i].Number == ep.Number {
					if byNumber != nil {
						byNumber = nil // the number is not unique here either
						break
					}
					byNumber = &same[i]
				}
			}
			if byNumber != nil {
				return imdbOrderMatch{Order: order, Season: byNumber.SeasonNumber, Episode: byNumber.Number,
					MatchedBy: "air_date+number", score: imdbScoreAirDateNumber}, true
			}
			// Same date, no unique number: if the names agree after all, that is
			// still usable evidence.
			if want := strings.TrimSpace(ep.Name); want != "" {
				if best := pickBestByName(same, want); best != nil {
					return imdbOrderMatch{Order: order, Season: best.SeasonNumber, Episode: best.Number,
						MatchedBy: "air_date+name", score: imdbScoreAirDateName}, true
				}
			}
		}
	}

	return imdbOrderMatch{}, false
}

// imdbOrderRank is the position of an order in IMDbOrderPreference; unknown
// orders sort last so they can never outrank a preferred one on a tie.
func imdbOrderRank(order string) int {
	for i, o := range IMDbOrderPreference {
		if o == order {
			return i
		}
	}
	return len(IMDbOrderPreference)
}

// selectIMDbOrder picks the order whose coordinates stand in for IMDb's numbering.
//
// Evidence decides first, because the question is which order actually CONTAINS
// this episode, not which order name is preferred. Preference breaks ties only --
// and ties are the common case: an episode id appears in every order that lists
// the episode, so alternate and default both score imdbScoreEpisodeID and the
// preference list is what chooses alternate. A series with no alternate order (or
// an episode missing from it) has a single survivor, so it answers regardless of
// preference.
func selectIMDbOrder(ep tvdb.EpisodeBaseRecord, candidates []imdbOrderCandidate) (imdbOrderMatch, bool) {
	var best imdbOrderMatch
	found := false
	for _, c := range candidates {
		m, ok := locateEpisodeInOrder(ep, c.Order, c.Episodes)
		if !ok {
			continue
		}
		if !found || m.score > best.score ||
			(m.score == best.score && imdbOrderRank(m.Order) < imdbOrderRank(best.Order)) {
			best, found = m, true
		}
	}
	return best, found
}

// ordersForIMDb lists the orders to read when looking for IMDb's numbering:
// the preference list first, then whatever the mapper was configured with, so a
// caller-configured order is never omitted.
func (m *Mapper) ordersForIMDb() []string {
	orders := make([]string, 0, len(IMDbOrderPreference)+len(m.fallbackSeasonTypes)+1)
	orders = append(orders, IMDbOrderPreference...)
	for _, o := range append([]string{m.tvdbSeasonType}, m.fallbackSeasonTypes...) {
		if !containsString(orders, o) {
			orders = append(orders, o)
		}
	}
	return orders
}

// TvdbToImdb maps a TVDB coordinate to the coordinate IMDb uses for the same
// episode, with no hints.
func (m *Mapper) TvdbToImdb(ctx context.Context, tvdbSeriesID int, season int, episode int) (*TvdbToImdbResult, error) {
	return m.TvdbToImdbWithHints(ctx, tvdbSeriesID, season, episode, EpisodeHints{})
}

// TvdbToImdbWithHints maps a TVDB episode to IMDb's numbering using the caller's
// episode-level hints.
//
// IMDb numbers episodes its own way, usually along TVDB's ALTERNATE order -- but
// "usually" is the whole problem: a series may have no alternate order, and an
// alternate order that exists may not list the episode. Assuming either one is how
// the wrong episode reaches a library, so the answer is decided by evidence:
//
//  1. the episode identity is resolved from the caller's numbers and hints, exactly
//     as the other directions do (episode id > order > numbers > name);
//  2. that episode is then located inside EVERY plausible order's episode list,
//     strongest evidence first -- episode id (exact, order-independent), then
//     normalised name, then the prefix-tolerant name matcher, then air date
//     (with the episode number breaking a binge-drop tie);
//  3. the best-scoring order wins; IMDbOrderPreference breaks ties, which is what
//     makes alternate beat default when both contain the episode.
//
// The result carries the order used and the evidence that matched, plus
// NumbersDiffer so a caller can see whether the remap moved the episode at all.
// When no order contains the episode, coordinate identity is NOT assumed: that
// stays opt-in via Options.AllowCoordinateIdentityFallback and is labelled
// "assumed_same_coordinates" when enabled.
func (m *Mapper) TvdbToImdbWithHints(ctx context.Context, tvdbSeriesID int, season int, episode int, hints EpisodeHints) (*TvdbToImdbResult, error) {
	if tvdbSeriesID <= 0 {
		return nil, errors.New("tvdb series id is required for tvdb->imdb mapping")
	}
	validated, err := m.validateHints(hints)
	if err != nil {
		return nil, err
	}
	hints = validated

	sel, err := m.resolveEpisode(ctx, tvdbSeriesID, season, episode, hints)
	if err != nil {
		return nil, err
	}
	ep := sel.Episode

	resolvedSeriesID := tvdbSeriesID
	if ep.SeriesID != 0 {
		resolvedSeriesID = ep.SeriesID
	}

	res := &TvdbToImdbResult{
		InputTVDBSeriesID:  tvdbSeriesID,
		InputSeason:        season,
		InputEpisode:       episode,
		TVDBSeriesID:       resolvedSeriesID,
		TVDBEpisodeID:      ep.ID,
		TVDBEpisodeName:    ep.Name,
		TVDBAirDate:        ep.Aired,
		InputTVDBOrder:     hints.TVDBOrder,
		InputTVDBEpisodeID: hints.TVDBEpisodeID,
		InputEpisodeName:   hints.EpisodeName,
		TVDBOrderUsed:      sel.Order,
		IdentitySource:     sel.Source,
		EpisodeNameMatch:   episodeNameMatch(hints.EpisodeName, ep.Name),
	}

	// An order a series does not have is a 404, which means "not this order", not
	// an error: that is exactly the "alternate isn't an option" case. A real
	// error is remembered and only surfaced when nothing matched anywhere.
	var firstErr error
	candidates := make([]imdbOrderCandidate, 0, len(IMDbOrderPreference))
	for _, order := range m.ordersForIMDb() {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		eps, err := m.fetchOrderEpisodes(ctx, resolvedSeriesID, order)
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
		if len(eps) == 0 {
			continue
		}
		res.OrdersTried = append(res.OrdersTried, order)
		candidates = append(candidates, imdbOrderCandidate{Order: order, Episodes: eps})
	}

	if match, ok := selectIMDbOrder(ep, candidates); ok {
		res.IMDbOrder = match.Order
		res.IMDbSeason = match.Season
		res.IMDbEpisode = match.Episode
		res.MatchedBy = match.MatchedBy
		res.NumbersDiffer = match.Season != season || match.Episode != episode
		return res, nil
	}

	if m.allowCoordinateIdentityFallback {
		res.IMDbOrder = sel.Order
		res.IMDbSeason = season
		res.IMDbEpisode = episode
		res.MatchedBy = "assumed_same_coordinates"
		return res, nil
	}

	if firstErr != nil {
		return nil, firstErr
	}
	return nil, fmt.Errorf("unable to map tvdb %d s%de%d to imdb numbering: the episode (%q, aired %q) was not found under any order (%s) by episode id, name or air date (coordinate identity is not assumed; set AllowCoordinateIdentityFallback to opt in)",
		tvdbSeriesID, season, episode, ep.Name, ep.Aired, strings.Join(res.OrdersTried, ", "))
}
