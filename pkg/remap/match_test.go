// SPDX-License-Identifier: MIT
// Copyright (C) 2026 TheIntroDB

package remap

import (
	"testing"

	"github.com/Pasithea0/go-tmdb-episodes-fix/pkg/tmdb"
	"github.com/Pasithea0/go-tmdb-episodes-fix/pkg/tvdb"
)

// bingeSeason returns a season where every episode shares one air date (the
// classic binge-drop / same-day release) with English "Episode N" names.
func bingeSeason() []tmdb.SeasonEpisode {
	return []tmdb.SeasonEpisode{
		{ID: 101, EpisodeNumber: 1, Name: "Episode 1", AirDate: "2026-06-05"},
		{ID: 102, EpisodeNumber: 2, Name: "Episode 2", AirDate: "2026-06-05"},
		{ID: 103, EpisodeNumber: 3, Name: "Episode 3", AirDate: "2026-06-05"},
		{ID: 104, EpisodeNumber: 4, Name: "Episode 4", AirDate: "2026-06-05"},
	}
}

// sharedAirDateOnly returns a season with all episodes on one air date and
// names that do NOT align with the TVDB names (foreign-title edge case).
func sharedAirDateOnly() []tmdb.SeasonEpisode {
	return []tmdb.SeasonEpisode{
		{EpisodeNumber: 1, Name: "Different title A", AirDate: "2026-06-05"},
		{EpisodeNumber: 2, Name: "Different title B", AirDate: "2026-06-05"},
		{EpisodeNumber: 3, Name: "Different title C", AirDate: "2026-06-05"},
		{EpisodeNumber: 4, Name: "Different title D", AirDate: "2026-06-05"},
	}
}

func TestMatchSeasonEpisode_BingeDropUsesEpisodeNumber(t *testing.T) {
	// Regression: TVDB s1e3 with aired=2026-06-05, name "3회". TMDB season has
	// ALL episodes on 2026-06-05 with English "Episode N" names (don't match the
	// Korean TVDB name). A bare air-date scan would pick episode 1; the
	// air-date+number scan must pick episode 3.
	tvdbEp := tvdb.EpisodeBaseRecord{Name: "3회", Aired: "2026-06-05", SeasonNumber: 1, Number: 3}
	epNum, epID, matchedBy := matchSeasonEpisode(tvdbEp, bingeSeason())
	if epNum != 3 {
		t.Fatalf("expected episode 3, got %d (matchedBy=%s)", epNum, matchedBy)
	}
	if matchedBy != "air_date+number_scan" {
		t.Fatalf("expected air_date+number_scan, got %s", matchedBy)
	}
	if epID != 103 {
		t.Fatalf("expected episode id 103, got %d", epID)
	}
}

func TestMatchSeasonEpisode_NameMatchTakesPriority(t *testing.T) {
	// If a TMDB episode name exactly matches, that wins even on a shared air date.
	tvdbEp := tvdb.EpisodeBaseRecord{Name: "Episode 2", Aired: "2026-06-05", SeasonNumber: 1, Number: 9}
	epNum, _, matchedBy := matchSeasonEpisode(tvdbEp, bingeSeason())
	if epNum != 2 {
		t.Fatalf("expected episode 2 (name match), got %d (matchedBy=%s)", epNum, matchedBy)
	}
	if matchedBy != "name_scan" {
		t.Fatalf("expected name_scan, got %s", matchedBy)
	}
}

func TestMatchSeasonEpisode_NoNameMatchFallsToDatePlusNumber(t *testing.T) {
	// TVDB names don't appear in TMDB at all; numbers align. Must still resolve.
	tvdbEp := tvdb.EpisodeBaseRecord{Name: "3회", Aired: "2026-06-05", SeasonNumber: 1, Number: 4}
	epNum, _, matchedBy := matchSeasonEpisode(tvdbEp, sharedAirDateOnly())
	if epNum != 4 {
		t.Fatalf("expected episode 4 (date+number), got %d (matchedBy=%s)", epNum, matchedBy)
	}
	if matchedBy != "air_date+number_scan" {
		t.Fatalf("expected air_date+number_scan, got %s", matchedBy)
	}
}

func TestMatchSeasonEpisode_NoMatch(t *testing.T) {
	tvdbEp := tvdb.EpisodeBaseRecord{Name: "Nope", Aired: "1999-01-01", SeasonNumber: 1, Number: 5}
	if epNum, epID, matchedBy := matchSeasonEpisode(tvdbEp, bingeSeason()); matchedBy != "" {
		t.Fatalf("expected no match, got epNum=%d epID=%d matchedBy=%s", epNum, epID, matchedBy)
	}
}

func TestMatchSeasonEpisode_OneDayAirDateSkew(t *testing.T) {
	// TVDB records the Japanese broadcast date (2023-09-28) while TMDB uses the
	// local one (2023-09-29) — the same episode. The scan must still match.
	tvdbEp := tvdb.EpisodeBaseRecord{Name: "昏乱", Aired: "2023-09-28", SeasonNumber: 2, Number: 10}
	season := []tmdb.SeasonEpisode{
		{ID: 201, EpisodeNumber: 34, Name: "Pandemonium", AirDate: "2023-09-29"},
		{ID: 202, EpisodeNumber: 35, Name: "A Girl Named Miwa", AirDate: "2023-10-06"},
	}
	epNum, epID, matchedBy := matchSeasonEpisode(tvdbEp, season)
	if epNum != 34 {
		t.Fatalf("expected episode 34, got %d (matchedBy=%s)", epNum, matchedBy)
	}
	if matchedBy != "air_date_scan" {
		t.Fatalf("expected air_date_scan, got %s", matchedBy)
	}
	if epID != 201 {
		t.Fatalf("expected episode id 201, got %d", epID)
	}
}

func TestDatesWithinOneDay(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"2023-09-28", "2023-09-29", true},
		{"2023-09-29", "2023-09-28", true},
		{"2023-09-28", "2023-09-28", true},
		{"2023-09-28", "2023-09-30", false},
		{"2023-01-01", "2022-12-31", true},
		{"not-a-date", "2023-09-29", false},
	}
	for _, c := range cases {
		if got := datesWithinOneDay(c.a, c.b); got != c.want {
			t.Fatalf("datesWithinOneDay(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestLookupInGroupBucket(t *testing.T) {
	// Bucket order 5 = TVDB season 5; entry.Order is 0-based, so position 10
	// (TVDB s5e10) has Order 9 and maps to TMDB s1e101 (id 1517234) — the
	// exact Bleach case that the air-date scan could not resolve.
	bucket := tmdb.EpisodeGroupOrder{
		Order: 5,
		Episodes: []tmdb.EpisodeGroupEntry{
			{ID: 1517225, SeasonNumber: 1, EpisodeNumber: 92, Order: 0},
			{ID: 1517226, SeasonNumber: 1, EpisodeNumber: 93, Order: 1},
			{ID: 1517234, SeasonNumber: 1, EpisodeNumber: 101, Order: 9},
		},
	}

	tvdbEp := tvdb.EpisodeBaseRecord{SeasonNumber: 5, Number: 10, Name: "マユリ卍解!! 沢渡・悪魔の激突", Aired: "2006-10-25"}
	epID, season, episode := lookupInGroupBucket(bucket, tvdbEp)
	if epID != 1517234 || season != 1 || episode != 101 {
		t.Fatalf("expected (1517234, 1, 101), got (%d, %d, %d)", epID, season, episode)
	}

	// Wrong bucket (season mismatch) must not match.
	tvdbEp.SeasonNumber = 6
	if epID, _, _ := lookupInGroupBucket(bucket, tvdbEp); epID != 0 {
		t.Fatalf("expected no match for wrong season, got %d", epID)
	}

	// Episode beyond the bucket's range must not match.
	tvdbEp.SeasonNumber = 5
	tvdbEp.Number = 20
	if epID, _, _ := lookupInGroupBucket(bucket, tvdbEp); epID != 0 {
		t.Fatalf("expected no match for out-of-range episode, got %d", epID)
	}
}
