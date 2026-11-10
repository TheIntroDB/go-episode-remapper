// SPDX-License-Identifier: MIT
// Copyright (C) 2026 TheIntroDB

package remap

import (
	"context"
	"strings"
	"testing"

	"github.com/Pasithea0/go-tmdb-episodes-fix/pkg/tmdb"
	"github.com/Pasithea0/go-tmdb-episodes-fix/pkg/tvdb"
)

// ---------------------------------------------------------------------------
// TMDB -> TVDB season 0: the wrong-episode bug, measured 2026-09-25.
//
// TMDB and TVDB `default` SWAP s0e1 and s0e2, and all four share the air date
// 2007-11-27, so an air-date-only resolver cannot tell them apart:
//
//	TMDB s0e1 "Bender's Big Score"       = TVDB default s0e2 (id 342888)
//	TMDB s0e2 "Everybody Loves Hypnotoad" = TVDB default s0e1 (id 389457)
//
// Before this fix, TMDB s0e1 resolved to TVDB default s0e1 -- "Everybody Loves
// Hypnotoad", a different episode -- and s0e2 resolved to the same episode, so
// default s0e2 was unreachable from TMDB coordinates entirely.
// ---------------------------------------------------------------------------

// TestTmdbSeasonZeroResolvesTheFilmNotHypnotoad pins the exact resolution. TMDB
// carries the TVDB episode id for this episode, so it must be used: no air date
// or title heuristic can separate these two, and both would be guesses.
func TestTmdbSeasonZeroResolvesTheFilmNotHypnotoad(t *testing.T) {
	m := futuramaMapper(t)

	got, err := m.TmdbToTvdbInOrder(context.Background(), 615, 0, 1, "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.TVDBEpisodeID != 342888 {
		t.Fatalf("TMDB s0e1 resolved to tvdb episode %d (%q); want 342888 (TVDB default s0e2 Bender's Big Score)",
			got.TVDBEpisodeID, got.TVDBEpisodeName)
	}
	if got.TVDBSeason != 0 || got.TVDBEpisode != 2 {
		t.Fatalf("got tvdb s%de%d, want s0e2", got.TVDBSeason, got.TVDBEpisode)
	}
	if got.MatchedBy != "tmdb_episode_tvdb_id" {
		t.Fatalf("got matched_by %q, want %q -- an exact cross-reference is not a heuristic",
			got.MatchedBy, "tmdb_episode_tvdb_id")
	}
}

// TestTmdbSeasonZeroHypnotoadResolvesToItsOwnEpisode is the other half: the fix
// must not merely shift both episodes up by one. TMDB s0e2 really is TVDB
// default s0e1, and both directions must land on distinct episodes.
func TestTmdbSeasonZeroHypnotoadResolvesToItsOwnEpisode(t *testing.T) {
	m := futuramaMapper(t)

	got, err := m.TmdbToTvdbInOrder(context.Background(), 615, 0, 2, "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.TVDBEpisodeID != 389457 || got.TVDBSeason != 0 || got.TVDBEpisode != 1 {
		t.Fatalf("TMDB s0e2 resolved to tvdb episode %d (s%de%d %q); want 389457 (s0e1 Everybody Loves Hypnotoad)",
			got.TVDBEpisodeID, got.TVDBSeason, got.TVDBEpisode, got.TVDBEpisodeName)
	}
}

// TestAirDateTieIsBrokenByName pins the fallback ordering: air date narrows the
// candidates, and the name chooses among them. This is the path for every episode
// TMDB has no TVDB link for, which is most of them.
func TestAirDateTieIsBrokenByName(t *testing.T) {
	m := futuramaMapperWithoutEpisodeExternalIDs(t)

	got, err := m.TmdbToTvdbInOrder(context.Background(), 615, 0, 1, "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.TVDBEpisodeID != 342888 {
		t.Fatalf("resolved to tvdb episode %d (%q); want 342888 -- the name must break the air-date tie",
			got.TVDBEpisodeID, got.TVDBEpisodeName)
	}
	if got.MatchedBy != "air_date+name" {
		t.Fatalf("got matched_by %q, want %q", got.MatchedBy, "air_date+name")
	}
}

// TestAirDateTieWithoutANameIsAmbiguous guards the other end: two candidates on
// the same date and no name to separate them is genuinely ambiguous, and picking
// the first is exactly the bug. The error must name the collisions so the caller
// can act.
func TestAirDateTieWithoutANameIsAmbiguous(t *testing.T) {
	m := futuramaMapperWithoutEpisodeExternalIDs(t)
	f := m.tmdb.(*fakeTMDB)
	prev := f.details["615/0/1"]
	f.details["615/0/1"] = &tmdb.EpisodeDetails{
		ID: prev.ID, Name: "", AirDate: prev.AirDate, SeasonNumber: 0, EpisodeNumber: 1,
	}

	_, err := m.TmdbToTvdbInOrder(context.Background(), 615, 0, 1, "default")
	if err == nil {
		t.Fatal("expected an ambiguity error: two tvdb episodes aired 2007-11-27 and nothing could choose between them")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("want an 'ambiguous' error, got %v", err)
	}
	for _, want := range []string{"Everybody Loves Hypnotoad", "Bender's Big Score"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the error must name the colliding episodes (missing %q): %v", want, err)
		}
	}
}

// TestResolutionDoesNotUseTheServerSideAirDateFilter pins that candidates are
// computed locally. TVDB's own airDate query parameter is unreliable -- it
// returned one of two episodes for 2007-11-27, none for 2008-06-30 (which s0e3
// aired), and a non-matching episode for 2008-06-24 -- so depending on it is a
// silent-wrong-answer risk. The fake fails loudly if any path passes it.
func TestResolutionDoesNotUseTheServerSideAirDateFilter(t *testing.T) {
	m := futuramaMapper(t)
	m.tvdb.(*fakeTVDB).forbidAirDateFilter = true

	cases := []struct {
		season, episode int
		wantID          int64
	}{
		{0, 1, 342888},
		{0, 2, 389457},
		{1, 1, 131174},
	}
	for _, tc := range cases {
		got, err := m.TmdbToTvdbInOrder(context.Background(), 615, tc.season, tc.episode, "default")
		if err != nil {
			t.Fatalf("s%de%d: %v -- resolution must not depend on TVDB's airDate filter", tc.season, tc.episode, err)
		}
		if got.TVDBEpisodeID != tc.wantID {
			t.Fatalf("s%de%d: got episode %d, want %d", tc.season, tc.episode, got.TVDBEpisodeID, tc.wantID)
		}
	}
}

// ---------------------------------------------------------------------------
// Name comparison must see through TVDB's series-name prefix.
// ---------------------------------------------------------------------------

// TestEpisodeNameMatchSeesThroughTheSeriesPrefix pins that the reported verdict
// uses the same rule as the matching logic. Before this, submitting the correct
// title for any prefixed special was reported as a contradiction, which makes any
// "reject on mismatch" policy reject correct submissions.
func TestEpisodeNameMatchSeesThroughTheSeriesPrefix(t *testing.T) {
	if got := episodeNameMatch("Bender's Game", "Futurama: Bender's Game"); got != NameMatchMatch {
		t.Fatalf("got %q, want %q: TVDB's series-name prefix is not a contradiction",
			got, NameMatchMatch)
	}
	if got := episodeNameMatch("Bender's Game", "Everybody Loves Hypnotoad"); got != NameMatchMismatch {
		t.Fatalf("got %q, want %q: a genuinely different title is still a contradiction",
			got, NameMatchMismatch)
	}
	if got := episodeNameMatch("", "Futurama: Bender's Game"); got != NameMatchUnknown {
		t.Fatalf("got %q, want %q", got, NameMatchUnknown)
	}
}

// TestPickBestByNameMatchesPrefixedSpecials pins the tiebreaker itself, which is
// where the prefix rule has to work: plain equality compared "Bender's Big Score"
// against "Futurama: Bender's Big Score" and returned nil, so the tiebreaker
// could never resolve the specials it exists for.
func TestPickBestByNameMatchesPrefixedSpecials(t *testing.T) {
	candidates := []tvdb.EpisodeBaseRecord{
		{ID: 389457, Name: "Everybody Loves Hypnotoad", Aired: "2007-11-27", SeasonNumber: 0, Number: 1},
		{ID: 342888, Name: "Futurama: Bender's Big Score", Aired: "2007-11-27", SeasonNumber: 0, Number: 2},
	}

	best := pickBestByName(candidates, "Bender's Big Score")
	if best == nil {
		t.Fatal("pickBestByName returned nil: a prefixed special must still be matchable by its plain title")
	}
	if best.ID != 342888 {
		t.Fatalf("picked %d (%q), want 342888", best.ID, best.Name)
	}

	if got := pickBestByName(candidates, "Nothing Like This"); got != nil {
		t.Fatalf("picked %d for an unrelated title; no candidate means nil, not a guess", got.ID)
	}
}
