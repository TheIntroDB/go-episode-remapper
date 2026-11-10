// SPDX-License-Identifier: MIT
// Copyright (C) 2026 TheIntroDB

package remap

import (
	"context"
	"testing"

	"github.com/Pasithea0/go-tmdb-episodes-fix/pkg/tmdb"
	"github.com/Pasithea0/go-tmdb-episodes-fix/pkg/tvdb"
)

// scanTMDBSeasons started at season 1, so TMDB season 0 was never read. TMDB
// keeps specials in season 0, which means a TVDB special had no reachable TMDB
// counterpart and could never map -- it failed with "unable to map" no matter
// how the evidence lined up.
//
// Real values, read from live TMDB 2026-09-25:
//
//	TMDB 615 season 0: e1 id 35101 "Bender's Big Score"        2007-11-27
//	                   e3 id 35102 "The Beast with a Billion Backs" 2008-06-24
//	                   e5 id 35105 "Bender's Game"             2008-11-04
//	                   e6 id 35106 "Into the Wild Green Yonder" 2009-02-12
//	TVDB 73871 default: the same four films as S0E2/E3/E5/E6,
//	                   ids 342888/359477/395236/427447.
//
// Note the numbering differs between the two (TVDB S0E2 is the first film, TMDB
// S0E1 is), which is why the match has to run on name and air date rather than
// on numbers.

func seasonZeroMapper() *Mapper {
	return &Mapper{
		tvdb: futuramaTVDB(),
		tmdb: &fakeTMDB{
			names:  map[int]string{615: "Futurama"},
			tvdbID: map[int]int{615: 73871},
			seasons: map[string][]tmdb.SeasonEpisode{
				// Season 0 -- the specials. Unreachable before this fix.
				"615/0": {
					{ID: 35101, EpisodeNumber: 1, Name: "Bender's Big Score", AirDate: "2007-11-27"},
					{ID: 35102, EpisodeNumber: 3, Name: "The Beast with a Billion Backs", AirDate: "2008-06-24"},
					{ID: 35105, EpisodeNumber: 5, Name: "Bender's Game", AirDate: "2008-11-04"},
					{ID: 35106, EpisodeNumber: 6, Name: "Into the Wild Green Yonder", AirDate: "2009-02-12"},
				},
				"615/1": {
					{ID: 35076, EpisodeNumber: 1, Name: "Space Pilot 3000", AirDate: "1999-03-28"},
				},
			},
		},
		tvdbSeasonType:      "default",
		fallbackSeasonTypes: defaultFallbackSeasonTypes,
		maxTVDBPages:        200,
		maxTMDBSeasons:      300,
	}
}

// TestScanTMDBSeasonsFindsSeasonZero pins the gate: a TVDB special maps to its
// TMDB season-0 episode by name.
func TestScanTMDBSeasonsFindsSeasonZero(t *testing.T) {
	m := seasonZeroMapper()

	tvdbEp := tvdb.EpisodeBaseRecord{
		ID: 342888, Name: "Bender's Big Score",
		Aired: "2007-11-27", SeriesID: 73871, SeasonNumber: 0, Number: 2,
	}

	seasonNum, epNum, epID, matchedBy, err := m.scanTMDBSeasons(context.Background(), 615, tvdbEp, false)
	if err != nil {
		t.Fatalf("a TVDB special must be mappable to TMDB season 0: %v", err)
	}
	if seasonNum != 0 {
		t.Fatalf("got TMDB season %d, want 0 (specials live there)", seasonNum)
	}
	if epNum != 1 || epID != 35101 {
		t.Fatalf("got e%d (id %d), want TMDB s0e1 id 35101 -- note TVDB calls it S0E2",
			epNum, epID)
	}
	if matchedBy != "name_scan" {
		t.Fatalf("got matched_by %q, want %q", matchedBy, "name_scan")
	}
}

// TestScanTMDBSeasonsStillFindsRegularSeasons is the regression guard: adding
// season 0 to the scan must not disturb ordinary episodes.
func TestScanTMDBSeasonsStillFindsRegularSeasons(t *testing.T) {
	m := seasonZeroMapper()

	tvdbEp := tvdb.EpisodeBaseRecord{
		ID: 131174, Name: "Space Pilot 3000",
		Aired: "1999-03-28", SeriesID: 73871, SeasonNumber: 1, Number: 1,
	}

	seasonNum, epNum, epID, _, err := m.scanTMDBSeasons(context.Background(), 615, tvdbEp, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seasonNum != 1 || epNum != 1 || epID != 35076 {
		t.Fatalf("got s%de%d (id %d), want s1e1 id 35076", seasonNum, epNum, epID)
	}
}

// TestScanTMDBSeasonsPrefersSeasonZeroForSpecials pins the ordering: a TVDB
// special looks in TMDB season 0 first, so a same-named episode elsewhere cannot
// capture it.
func TestScanTMDBSeasonsPrefersSeasonZeroForSpecials(t *testing.T) {
	m := seasonZeroMapper()
	// Plant a decoy with the same name in a regular season.
	m.tmdb.(*fakeTMDB).seasons["615/5"] = []tmdb.SeasonEpisode{
		{ID: 999999, EpisodeNumber: 7, Name: "Bender's Big Score", AirDate: "2007-11-27"},
	}

	tvdbEp := tvdb.EpisodeBaseRecord{
		ID: 342888, Name: "Bender's Big Score",
		Aired: "2007-11-27", SeriesID: 73871, SeasonNumber: 0, Number: 2,
	}

	seasonNum, epNum, epID, _, err := m.scanTMDBSeasons(context.Background(), 615, tvdbEp, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seasonNum != 0 || epNum != 1 || epID != 35101 {
		t.Fatalf("got TMDB s%de%d (id %d), want s0e1 id 35101: a TVDB special must "+
			"resolve against TMDB season 0 before any other season", seasonNum, epNum, epID)
	}
}

// TestScanTMDBSeasonsBendersBigScoreNotHypnotoad pins the wrong answer the first
// live run produced. TVDB names the special "Futurama: Bender's Big Score" while
// TMDB names it "Bender's Big Score", so an exact comparison missed and the
// weaker air_date+number rule decided. Both TMDB s0e1 and s0e2 carry the
// 2007-11-27 air date, and TVDB numbers the film 2 while TMDB numbers it 1, so
// the number rule answered s0e2 -- "Everybody Loves Hypnotoad", a different
// special. The name now matches with the series prefix removed.
func TestScanTMDBSeasonsBendersBigScoreNotHypnotoad(t *testing.T) {
	m := seasonZeroMapper()
	m.tmdb.(*fakeTMDB).seasons["615/0"] = []tmdb.SeasonEpisode{
		{ID: 35101, EpisodeNumber: 1, Name: "Bender's Big Score", AirDate: "2007-11-27"},
		{ID: 35103, EpisodeNumber: 2, Name: "Everybody Loves Hypnotoad", AirDate: "2007-11-27"},
		{ID: 35102, EpisodeNumber: 3, Name: "The Beast with a Billion Backs", AirDate: "2008-06-24"},
	}

	// The name as TVDB actually stores it, series prefix included.
	tvdbEp := tvdb.EpisodeBaseRecord{
		ID: 342888, Name: "Futurama: Bender's Big Score",
		Aired: "2007-11-27", SeriesID: 73871, SeasonNumber: 0, Number: 2,
	}

	seasonNum, epNum, epID, matchedBy, err := m.scanTMDBSeasons(context.Background(), 615, tvdbEp, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if epID == 35103 {
		t.Fatal("mapped to TMDB s0e2 \"Everybody Loves Hypnotoad\": a different special " +
			"that shares the air date. The series prefix must not defeat the name match.")
	}
	if seasonNum != 0 || epNum != 1 || epID != 35101 {
		t.Fatalf("got TMDB s%de%d (id %d), want s0e1 id 35101 \"Bender's Big Score\"",
			seasonNum, epNum, epID)
	}
	if matchedBy != "name_scan" {
		t.Fatalf("got matched_by %q, want %q", matchedBy, "name_scan")
	}
}

// TestTitlesMatch pins the prefix rule and, more importantly, that it stays
// narrow: ordinary titles must still require an exact match.
func TestTitlesMatch(t *testing.T) {
	cases := []struct {
		want, got string
		expect    bool
	}{
		{"Bender's Big Score", "Bender's Big Score", true},
		{"Futurama: Bender's Big Score", "Bender's Big Score", true},
		{"Bender's Big Score", "Futurama: Bender's Big Score", true},
		{"Futurama: Bender's Big Score", "Everybody Loves Hypnotoad", false},
		{"Bender's Big Score", "The Beast with a Billion Backs", false},
		// Narrowness: no colon on either side means exact only.
		{"The End", "The Beginning of the End", false},
		{"A", "B", false},
	}
	for _, c := range cases {
		if got := TitlesMatch(c.want, c.got); got != c.expect {
			t.Fatalf("TitlesMatch(%q, %q) = %v, want %v", c.want, c.got, got, c.expect)
		}
	}
}

// TestTvdbToTmdbFallsThroughAStaleRemoteID pins the third failure found while
// verifying season 0 live. TVDB's remote ids for the Futurama specials point at
// TMDB episode ids that no longer exist -- S0E5 "Bender's Game" carries 13253,
// which 404s. The mapper returned that error, refusing a question the name and
// air date settle exactly.
//
// A dead pointer is not evidence that an episode cannot be mapped.
func TestTvdbToTmdbFallsThroughAStaleRemoteID(t *testing.T) {
	m := seasonZeroMapper()

	got, err := m.TvdbToTmdbWithHints(context.Background(), 73871, 0, 5, EpisodeHints{})
	if err != nil {
		t.Fatalf("a stale remote id must not abort the mapping: %v", err)
	}
	if got.TMDBEpisodeID != 35105 || got.TMDBSeason != 0 || got.TMDBEpisode != 5 {
		t.Fatalf("got TMDB s%de%d (id %d), want s0e5 id 35105",
			got.TMDBSeason, got.TMDBEpisode, got.TMDBEpisodeID)
	}
	if got.MatchedBy != "name_scan" {
		t.Fatalf("got matched_by %q, want %q (resolved by evidence, not by the dead id)",
			got.MatchedBy, "name_scan")
	}
}
