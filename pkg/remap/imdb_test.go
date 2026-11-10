// SPDX-License-Identifier: MIT
// Copyright (C) 2026 TheIntroDB

package remap

import (
	"context"
	"testing"

	"github.com/Pasithea0/go-tmdb-episodes-fix/pkg/tvdb"
)

// ep builds a TVDB episode fixture. id 0 means "no episode id known", which is
// what arrives when a caller has only numbers to go on.
func ep(id int64, name, aired string, season, number int) tvdb.EpisodeBaseRecord {
	return tvdb.EpisodeBaseRecord{ID: id, Name: name, Aired: aired, SeasonNumber: season, Number: number}
}

func TestSelectIMDbOrder_PrefersAlternateWhenBothOrdersContainTheEpisode(t *testing.T) {
	// A TVDB episode id is unique across orders, so this episode is found by id in
	// BOTH lists and the two score equally. That tie is the normal case, and the
	// preference list is what must resolve it to alternate -- "IMDb episodes
	// usually follow TVDB's alternate order". Candidates are supplied default-first
	// to prove preference decides, not input order.
	episode := ep(342888, "Bender's Big Score (1)", "2007-11-27", 0, 2)
	defaultList := []tvdb.EpisodeBaseRecord{
		ep(342888, "Bender's Big Score (1)", "2007-11-27", 0, 2),
		ep(389457, "Everybody Loves Hypnotoad", "2007-11-27", 0, 1),
	}
	alternateList := []tvdb.EpisodeBaseRecord{
		ep(342888, "Bender's Big Score (1)", "2007-11-27", 6, 1),
		ep(389457, "Everybody Loves Hypnotoad", "2007-11-27", 6, 2),
	}

	match, ok := selectIMDbOrder(episode, []imdbOrderCandidate{
		{Order: "default", Episodes: defaultList},
		{Order: "alternate", Episodes: alternateList},
	})
	if !ok {
		t.Fatal("expected a match")
	}
	if match.Order != "alternate" {
		t.Fatalf("order = %q, want alternate", match.Order)
	}
	if match.Season != 6 || match.Episode != 1 {
		t.Fatalf("coords = s%de%d, want s6e1", match.Season, match.Episode)
	}
	if match.MatchedBy != "tvdb_episode_id" {
		t.Fatalf("matchedBy = %q, want tvdb_episode_id", match.MatchedBy)
	}
}

func TestSelectIMDbOrder_FallsBackToDefaultWhenAlternateIsAbsent(t *testing.T) {
	// TVDB 404s an order a series has none of, so the candidate list simply has no
	// alternate entry. This is the "alternate isn't an option" case and it must
	// still answer.
	episode := ep(100, "Pilot", "2020-01-01", 1, 1)
	match, ok := selectIMDbOrder(episode, []imdbOrderCandidate{
		{Order: "default", Episodes: []tvdb.EpisodeBaseRecord{ep(100, "Pilot", "2020-01-01", 1, 1)}},
	})
	if !ok {
		t.Fatal("expected a match from the default order alone")
	}
	if match.Order != "default" || match.Season != 1 || match.Episode != 1 {
		t.Fatalf("got %q s%de%d, want default s1e1", match.Order, match.Season, match.Episode)
	}
}

func TestSelectIMDbOrder_IgnoresAlternateThatLacksTheEpisode(t *testing.T) {
	// The alternate order exists but does not list this episode. Preference must
	// not hand back alternate's other episode.
	episode := ep(100, "Pilot", "2020-01-01", 1, 1)
	match, ok := selectIMDbOrder(episode, []imdbOrderCandidate{
		{Order: "alternate", Episodes: []tvdb.EpisodeBaseRecord{ep(999, "Some Other Special", "2019-05-05", 0, 1)}},
		{Order: "default", Episodes: []tvdb.EpisodeBaseRecord{ep(100, "Pilot", "2020-01-01", 1, 1)}},
	})
	if !ok {
		t.Fatal("expected a match")
	}
	if match.Order != "default" {
		t.Fatalf("order = %q, want default (alternate does not contain the episode)", match.Order)
	}
}

func TestSelectIMDbOrder_KnownOrderOutranksUnknownOnATie(t *testing.T) {
	// An order nobody recognises must never outrank a known one when the evidence
	// scores the same, so a typo'd or new TVDB order cannot silently win.
	episode := ep(100, "Pilot", "2020-01-01", 1, 1)
	match, ok := selectIMDbOrder(episode, []imdbOrderCandidate{
		{Order: "madeup", Episodes: []tvdb.EpisodeBaseRecord{ep(100, "Pilot", "2020-01-01", 7, 7)}},
		{Order: "default", Episodes: []tvdb.EpisodeBaseRecord{ep(100, "Pilot", "2020-01-01", 1, 1)}},
	})
	if !ok {
		t.Fatal("expected a match")
	}
	if match.Order != "default" {
		t.Fatalf("order = %q, want default over an unknown order", match.Order)
	}
}

func TestLocateEpisodeInOrder_UsesNameWhenNoEpisodeID(t *testing.T) {
	episode := ep(0, "Rebirth", "2010-06-24", 6, 1)
	got, ok := locateEpisodeInOrder(episode, "alternate", []tvdb.EpisodeBaseRecord{
		ep(0, "Rebirth", "2010-06-24", 6, 1),
		ep(0, "In-a-Gadda-Da-Leela", "2010-07-01", 6, 2),
	})
	if !ok {
		t.Fatal("expected a name match")
	}
	if got.MatchedBy != "name" || got.Season != 6 || got.Episode != 1 {
		t.Fatalf("got %s s%de%d, want name s6e1", got.MatchedBy, got.Season, got.Episode)
	}
}

func TestLocateEpisodeInOrder_PrefixTolerantNameMatch(t *testing.T) {
	// TVDB prefixes specials with the series name, so plain equality rejects the
	// very episodes this exists to place. The looser matcher must catch it, and be
	// labelled as loose so a caller can weigh it differently.
	episode := ep(0, "Bender's Game", "2008-11-04", 6, 4)
	got, ok := locateEpisodeInOrder(episode, "alternate", []tvdb.EpisodeBaseRecord{
		ep(0, "Futurama: Bender's Game", "2008-11-04", 6, 4),
	})
	if !ok {
		t.Fatal("expected a prefix-tolerant name match")
	}
	if got.MatchedBy != "name_loose" {
		t.Fatalf("matchedBy = %q, want name_loose", got.MatchedBy)
	}
	if got.Season != 6 || got.Episode != 4 {
		t.Fatalf("coords = s%de%d, want s6e4", got.Season, got.Episode)
	}
}

func TestLocateEpisodeInOrder_BingeDropResolvesByEpisodeNumber(t *testing.T) {
	// Four episodes share one air date and carry "Episode N" names that do not
	// match the caller's title, so the episode number is the only usable evidence.
	episode := ep(0, "3회", "2026-06-05", 1, 3)
	got, ok := locateEpisodeInOrder(episode, "default", []tvdb.EpisodeBaseRecord{
		ep(0, "Episode 1", "2026-06-05", 1, 1),
		ep(0, "Episode 2", "2026-06-05", 1, 2),
		ep(0, "Episode 3", "2026-06-05", 1, 3),
		ep(0, "Episode 4", "2026-06-05", 1, 4),
	})
	if !ok {
		t.Fatal("expected the shared air date to resolve by episode number")
	}
	if got.MatchedBy != "air_date+number" || got.Episode != 3 {
		t.Fatalf("got %s e%d, want air_date+number e3", got.MatchedBy, got.Episode)
	}
}

func TestLocateEpisodeInOrder_AirDateAloneWhenNothingElseAvailable(t *testing.T) {
	episode := ep(0, "", "2019-03-01", 2, 5)
	got, ok := locateEpisodeInOrder(episode, "default", []tvdb.EpisodeBaseRecord{
		ep(0, "Something", "2019-02-01", 2, 4),
		ep(0, "Whatever", "2019-03-01", 2, 5),
	})
	if !ok {
		t.Fatal("expected an air-date match")
	}
	if got.MatchedBy != "air_date" || got.Episode != 5 {
		t.Fatalf("got %s e%d, want air_date e5", got.MatchedBy, got.Episode)
	}
}

func TestLocateEpisodeInOrder_RefusesAmbiguousSharedName(t *testing.T) {
	// Two episodes share the title and there is no id or air date to separate
	// them. Picking either is how the wrong episode reaches a library, so this must
	// refuse rather than fall through to the first hit.
	episode := ep(0, "Pilot", "", 1, 1)
	if _, ok := locateEpisodeInOrder(episode, "default", []tvdb.EpisodeBaseRecord{
		ep(0, "Pilot", "", 1, 1),
		ep(0, "Pilot", "", 2, 1),
	}); ok {
		t.Fatal("expected no match for an ambiguous shared name")
	}
}

func TestLocateEpisodeInOrder_RefusesSharedAirDateWithNoUsableNumber(t *testing.T) {
	// Same date, and the caller's episode number does not exist in this order, and
	// the names disagree: nothing identifies the episode, so nothing is returned.
	episode := ep(0, "Unknown", "2026-06-05", 1, 9)
	if _, ok := locateEpisodeInOrder(episode, "default", []tvdb.EpisodeBaseRecord{
		ep(0, "A", "2026-06-05", 1, 1),
		ep(0, "B", "2026-06-05", 1, 2),
	}); ok {
		t.Fatal("expected no match when the shared air date cannot be broken")
	}
}

func TestLocateEpisodeInOrder_EmptyOrderList(t *testing.T) {
	if _, ok := locateEpisodeInOrder(ep(1, "Pilot", "2020-01-01", 1, 1), "default", nil); ok {
		t.Fatal("expected no match against an empty order")
	}
}

func TestTvdbToImdb_RequiresSeriesID(t *testing.T) {
	// The guard runs before any client call, so this needs no network.
	m := NewMapper(Options{})
	if _, err := m.TvdbToImdb(context.Background(), 0, 1, 1); err == nil {
		t.Fatal("expected an error for a missing series id")
	}
}
