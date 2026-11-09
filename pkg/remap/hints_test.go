// SPDX-License-Identifier: MIT
// Copyright (C) 2026 TheIntroDB

package remap

import (
	"errors"
	"testing"

	"github.com/Pasithea0/go-tmdb-episodes-fix/pkg/tvdb"
)

func TestNormalizeTVDBOrder(t *testing.T) {
	cases := []struct {
		in    string
		want  string
		valid bool
	}{
		{"official", "official", true},
		{"Official", "official", true},
		{"dvd", "dvd", true},
		{"absolute", "absolute", true},
		{"alternate", "alternate", true},
		{"regional", "regional", true},
		{"default", "default", true},
		// Jellyfin displayorder vocabulary (Series.DisplayOrder).
		{"altdvd", "dvd", true},
		{"alttwo", "alternate", true},
		{"absolute order", "absolute", true},
		{"aired-order", "official", true},
		{"alttwo", "alternate", true},
		{"streaming", "alternate", true},
		{"production", "regional", true},
		{"  ALTERNATE ", "alternate", true},
		{"", "", false},
		{"bogus", "", false},
		{"season2", "", false},
	}
	for _, c := range cases {
		got, ok := NormalizeTVDBOrder(c.in)
		if ok != c.valid || got != c.want {
			t.Fatalf("NormalizeTVDBOrder(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.valid)
		}
	}
}

func TestEpisodeNameMatch(t *testing.T) {
	cases := []struct {
		want, got, expect string
	}{
		{"Rebirth", "Rebirth", NameMatchMatch},
		{"rebirth", "REBIRTH", NameMatchMatch},
		{"Bender's Big Score (1)", "Benders Big Score 1", NameMatchMatch},
		{"Rebirth", "The Bots and the Bees", NameMatchMismatch},
		// A localized caller name is reported as a mismatch. The mapper reports
		// the fact; the caller decides what to do with it (see the plan's
		// localized-name risk — the API only rejects when it knows the stored
		// episode's name differs).
		{"Wiedergeburt", "Rebirth", NameMatchMismatch},
		{"", "Rebirth", NameMatchUnknown},
		{"Rebirth", "", NameMatchUnknown},
		{"", "", NameMatchUnknown},
	}
	for _, c := range cases {
		if got := episodeNameMatch(c.want, c.got); got != c.expect {
			t.Fatalf("episodeNameMatch(%q, %q) = %q, want %q", c.want, c.got, got, c.expect)
		}
	}
}

func TestOrderGroupAllowed(t *testing.T) {
	// The TMDB "TVDB Order" group is keyed by TVDB default/official positions,
	// so it may only back those orders.
	for _, order := range []string{"", "default", "official"} {
		if !orderGroupAllowed(order) {
			t.Fatalf("orderGroupAllowed(%q) = false, want true", order)
		}
	}
	for _, order := range []string{"alternate", "dvd", "absolute", "regional"} {
		if orderGroupAllowed(order) {
			t.Fatalf("orderGroupAllowed(%q) = true, want false", order)
		}
	}
}

func TestSelectEpisodeByHints_EpisodeIDPinsTheOrder(t *testing.T) {
	// The Futurama shape: the same season/episode pair names a different
	// episode per order, and the episode id says which one the caller means.
	candidates := []orderCandidate{
		{Order: "default", Episode: tvdb.EpisodeBaseRecord{ID: 1051911, Name: "Rebirth", SeasonNumber: 6, Number: 1}},
		{Order: "official", Episode: tvdb.EpisodeBaseRecord{ID: 1051911, Name: "Rebirth", SeasonNumber: 6, Number: 1}},
		{Order: "alternate", Episode: tvdb.EpisodeBaseRecord{ID: 8234611, Name: "Bender's Big Score (1)", SeasonNumber: 6, Number: 1}},
	}

	sel, err := selectEpisodeByHints(candidates, EpisodeHints{TVDBEpisodeID: 8234611}, 6, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sel.Order != "alternate" || sel.Episode.ID != 8234611 {
		t.Fatalf("expected the alternate record, got order=%q id=%d", sel.Order, sel.Episode.ID)
	}
}

func TestSelectEpisodeByHints_EpisodeIDContradictionIsAnError(t *testing.T) {
	candidates := []orderCandidate{
		{Order: "default", Episode: tvdb.EpisodeBaseRecord{ID: 1051911, Name: "Rebirth", SeasonNumber: 7, Number: 1}},
		{Order: "alternate", Episode: tvdb.EpisodeBaseRecord{ID: 1051912, Name: "The Bots and the Bees", SeasonNumber: 7, Number: 1}},
	}

	_, err := selectEpisodeByHints(candidates, EpisodeHints{TVDBEpisodeID: 999999}, 7, 1)
	if err == nil {
		t.Fatal("expected an error for an episode id that matches no candidate")
	}
	var hintErr *EpisodeHintError
	if !errors.As(err, &hintErr) || hintErr.Field != "tvdb_episode_id" {
		t.Fatalf("expected an EpisodeHintError on tvdb_episode_id, got %v", err)
	}
}

func TestSelectEpisodeByHints_NoIDTakesThePreferredOrder(t *testing.T) {
	// Without an id the candidate list is already in preference order, which is
	// what reproduces the historical primary-then-fallback behaviour.
	candidates := []orderCandidate{
		{Order: "default", Episode: tvdb.EpisodeBaseRecord{ID: 1, Name: "Rebirth"}},
		{Order: "alternate", Episode: tvdb.EpisodeBaseRecord{ID: 2, Name: "Bender's Big Score (1)"}},
	}
	sel, err := selectEpisodeByHints(candidates, EpisodeHints{}, 7, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sel.Order != "default" || sel.Episode.ID != 1 {
		t.Fatalf("expected the first candidate, got order=%q id=%d", sel.Order, sel.Episode.ID)
	}
}

func TestOrdersForHints(t *testing.T) {
	m := NewMapper(Options{TVDBAPIKey: "x", TVDBSeasonType: "default", FallbackSeasonTypes: []string{"official", "dvd"}})

	// An explicit order is used alone: falling back would answer a different
	// question than the one the caller asked.
	if got := m.ordersForHints(EpisodeHints{TVDBOrder: "alternate"}); len(got) != 1 || got[0] != "alternate" {
		t.Fatalf("order hint must be exclusive, got %v", got)
	}

	// No hints: unchanged historical behaviour.
	if got := m.ordersForHints(EpisodeHints{}); len(got) != 3 || got[0] != "default" || got[1] != "official" || got[2] != "dvd" {
		t.Fatalf("unexpected default order list: %v", got)
	}

	// An episode id must be locatable, so every order is tried — primary first,
	// and no duplicates.
	got := m.ordersForHints(EpisodeHints{TVDBEpisodeID: 8234611})
	if got[0] != "default" {
		t.Fatalf("expected the primary season type first, got %v", got)
	}
	seen := map[string]bool{}
	for _, o := range got {
		if seen[o] {
			t.Fatalf("duplicate order %q in %v", o, got)
		}
		seen[o] = true
	}
	for _, want := range TVDBOrders {
		if !seen[want] {
			t.Fatalf("order %q missing from the episode-id search list %v", want, got)
		}
	}
}

func TestValidateHints(t *testing.T) {
	m := NewMapper(Options{TVDBAPIKey: "x"})

	ok, err := m.validateHints(EpisodeHints{TVDBOrder: " Altdvd ", EpisodeName: "  Rebirth "})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok.TVDBOrder != "dvd" || ok.EpisodeName != "Rebirth" {
		t.Fatalf("expected normalized (dvd, Rebirth), got (%q, %q)", ok.TVDBOrder, ok.EpisodeName)
	}

	_, err = m.validateHints(EpisodeHints{TVDBOrder: "season2"})
	var hintErr *EpisodeHintError
	if !errors.As(err, &hintErr) || hintErr.Field != "tvdb_order" {
		t.Fatalf("expected an EpisodeHintError on tvdb_order, got %v", err)
	}
}
