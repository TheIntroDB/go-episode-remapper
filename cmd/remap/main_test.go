// SPDX-License-Identifier: MIT
// Copyright (C) 2026 TheIntroDB

package main

import (
	"reflect"
	"testing"
)

func TestDedupeMismatchItems_MergesSegments(t *testing.T) {
	items := []mismatchItem{
		{ImdbID: "tt0434665", Season: 14, Episode: 19, Title: "Bleach", Segment: "intro"},
		{ImdbID: "tt0434665", Season: 14, Episode: 19, Title: "Bleach", Segment: "credits"},
		{ImdbID: "tt5607616", Season: 3, Episode: 4, Title: "Re:ZERO", Segment: "credits"},
		{ImdbID: "", Season: 3, Episode: 4, Title: "no imdb id"},          // dropped
		{ImdbID: "tt5607616", Season: 0, Episode: 4, Title: "no season"},  // dropped
		{ImdbID: "tt5607616", Season: 3, Episode: 0, Title: "no episode"}, // dropped
	}

	rows := dedupeMismatchItems(items)
	if len(rows) != 2 {
		t.Fatalf("expected 2 unique rows, got %d: %+v", len(rows), rows)
	}

	first := rows[0]
	if first.ImdbID != "tt0434665" || first.InputSeason != 14 || first.InputEpisode != 19 {
		t.Fatalf("unexpected first row: %+v", first)
	}
	if !reflect.DeepEqual(first.Segments, []string{"intro", "credits"}) {
		t.Fatalf("expected merged segments [intro credits], got %v", first.Segments)
	}

	second := rows[1]
	if second.ImdbID != "tt5607616" || second.InputSeason != 3 || second.InputEpisode != 4 {
		t.Fatalf("unexpected second row: %+v", second)
	}
	if !reflect.DeepEqual(second.Segments, []string{"credits"}) {
		t.Fatalf("expected segments [credits], got %v", second.Segments)
	}
}

func TestDedupeMismatchItems_Empty(t *testing.T) {
	if rows := dedupeMismatchItems(nil); len(rows) != 0 {
		t.Fatalf("expected no rows for nil input, got %d", len(rows))
	}
}
