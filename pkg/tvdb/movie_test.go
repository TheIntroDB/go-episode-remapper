// SPDX-License-Identifier: MIT
// Copyright (C) 2026 TheIntroDB

package tvdb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestClient points a Client at a stub TVDB. It answers /login so the token step is
// exercised the same way it is against the real API, and fails any request that does
// not carry the resulting bearer token.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			writeJSON(t, w, map[string]any{"status": "success", "data": map[string]string{"token": "test-token"}})
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("request %s sent Authorization %q, want the login token", r.URL.Path, got)
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	c := NewClient("test-key", "")
	c.baseURL = srv.URL
	return c
}

func writeJSON(t *testing.T, w http.ResponseWriter, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Errorf("encoding stub response: %v", err)
	}
}

// recordPath returns a handler that records the paths it was asked for.
func recordPath(t *testing.T, seen *[]string, body any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		*seen = append(*seen, r.URL.Path)
		writeJSON(t, w, body)
	}
}

var ctx = context.Background()

// remoteIDLookup builds the shape TVDB returns for /search/remoteid/{id}: a list of
// results, each tagged with the kind it is.
func remoteIDLookup(items ...map[string]any) map[string]any {
	return map[string]any{"status": "success", "data": items}
}

func movieItem(id int, name string) map[string]any {
	return map[string]any{"movie": map[string]any{"id": id, "name": name, "year": "1999"}}
}

func seriesItem(id int, name string) map[string]any {
	return map[string]any{"series": map[string]any{"id": id, "name": name}}
}

// The critical case: one lookup can carry a series and a movie at once, because TMDB
// ids are unique per namespace rather than globally. Taking the first entry would hand
// back a TV show for a film.
func TestFindMovieByTMDBIDSelectsTheMovieNotTheSeries(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, remoteIDLookup(
			seriesItem(2001, "A Show Sharing The Number"),
			movieItem(169, "The Matrix"),
		))
	})

	m, candidate, err := c.FindMovieByTMDBID(ctx, 603)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil {
		t.Fatal("expected the movie, got nil")
	}
	if m.ID != 169 || m.Name != "The Matrix" {
		t.Fatalf("got id=%d name=%q, want the movie 169 The Matrix", m.ID, m.Name)
	}
	if candidate != "603" {
		t.Fatalf("candidate = %q, want the bare \"603\" tried first", candidate)
	}
}

// A miss is not an error: TVDB answered, it just has no movie for that id. Conflating
// the two would turn "not on TVDB" into a retryable-looking outage.
func TestFindMovieByTMDBIDReportsAMissAsNil(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, remoteIDLookup(seriesItem(1, "Only A Series")))
	})

	m, candidate, err := c.FindMovieByTMDBID(ctx, 999999999)
	if err != nil {
		t.Fatalf("a miss must not be an error, got: %v", err)
	}
	if m != nil {
		t.Fatalf("expected no movie, got id=%d", m.ID)
	}
	if candidate != "" {
		t.Fatalf("candidate = %q, want empty on a miss", candidate)
	}
}

// The candidates are tried in order, so a hit on the bare number costs one request
// rather than five.
func TestFindMovieByTMDBIDTriesTheBareNumberFirst(t *testing.T) {
	var seen []string
	c := newTestClient(t, recordPath(t, &seen, remoteIDLookup(movieItem(169, "The Matrix"))))

	if _, _, err := c.FindMovieByTMDBID(ctx, 603); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(seen) != 1 || seen[0] != "/search/remoteid/603" {
		t.Fatalf("requested %v, want a single lookup of /search/remoteid/603", seen)
	}
}

func TestFindMovieByTMDBIDRejectsANonPositiveID(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no request should be made for a non-positive id, got %s", r.URL.Path)
	})

	if _, _, err := c.FindMovieByTMDBID(ctx, 0); err == nil {
		t.Fatal("tmdb id 0 must be rejected")
	}
	if _, _, err := c.FindMovieByTMDBID(ctx, -5); err == nil {
		t.Fatal("a negative tmdb id must be rejected")
	}
}

func TestFindMovieByIMDbIDUsesTheIMDbForm(t *testing.T) {
	var seen []string
	c := newTestClient(t, recordPath(t, &seen, remoteIDLookup(movieItem(169, "The Matrix"))))

	m, candidate, err := c.FindMovieByIMDbID(ctx, "tt0133093")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil || m.ID != 169 {
		t.Fatalf("expected movie 169, got %+v", m)
	}
	if candidate != "tt0133093" {
		t.Fatalf("candidate = %q, want the bare imdb id first", candidate)
	}
}

func TestSearchMovieByRemoteIDRejectsAnEmptyID(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no request should be made for an empty remote id, got %s", r.URL.Path)
	})
	if _, err := c.SearchMovieByRemoteID(ctx, "   "); err == nil {
		t.Fatal("an empty remote id must be rejected")
	}
}

func TestGetMovieExtendedReadsTheMovie(t *testing.T) {
	var seen []string
	c := newTestClient(t, recordPath(t, &seen, map[string]any{
		"status": "success",
		"data": map[string]any{
			"id":      169,
			"name":    "The Matrix",
			"year":    "1999",
			"slug":    "the-matrix",
			"runtime": 136,
			"remoteIds": []map[string]any{
				{"id": "tt0133093", "sourceName": "IMDB"},
				{"id": "603", "sourceName": "TheMovieDB.com"},
			},
		},
	}))

	m, err := c.GetMovieExtended(ctx, 169)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ID != 169 || m.Name != "The Matrix" || m.Year != "1999" || m.Runtime != 136 {
		t.Fatalf("parsed %+v, want the movie 169 The Matrix (1999) 136min", m)
	}
	if len(seen) != 1 || seen[0] != "/movies/169/extended" {
		t.Fatalf("requested %v, want /movies/169/extended", seen)
	}
}

// A TVDB movie id is required: the id space overlaps TMDB's, so a caller that passes a
// TMDB id gets a different film rather than an error. The guard catches the ids that
// are clearly wrong, and the doc comment carries the rest.
func TestGetMovieExtendedRejectsANonPositiveID(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no request should be made for a non-positive id, got %s", r.URL.Path)
	})
	if _, err := c.GetMovieExtended(ctx, 0); err == nil {
		t.Fatal("movie id 0 must be rejected")
	}
	if _, err := c.GetMovieExtended(ctx, -1); err == nil {
		t.Fatal("a negative movie id must be rejected")
	}
}

// A 404 must stay distinguishable from an outage, because callers fall through on
// "not present" but must abort on a transport failure.
func TestGetMovieExtendedSurfacesANotFound(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"status":"failure","message":"not found"}`))
	})

	_, err := c.GetMovieExtended(ctx, 123456)
	if err == nil {
		t.Fatal("expected an error")
	}
	var notFound *NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("want a *NotFoundError so callers can fall through, got %T: %v", err, err)
	}
}

// TVDB spells the TMDB source inconsistently across records, so the reader normalises
// rather than matching one literal.
func TestTmdbIDReadsTheVariedSourceNames(t *testing.T) {
	for _, source := range []string{"TheMovieDB.com", "TheMovieDB", "themoviedb", "TMDB", "tmdb"} {
		m := &MovieExtendedRecord{RemoteIDs: []RemoteID{{ID: "603", SourceName: source}}}
		id, ok := m.TmdbID()
		if !ok || id != 603 {
			t.Fatalf("sourceName %q: got (%d,%v), want (603,true)", source, id, ok)
		}
	}
}

func TestTmdbIDIgnoresOtherSources(t *testing.T) {
	m := &MovieExtendedRecord{RemoteIDs: []RemoteID{
		{ID: "tt0133093", SourceName: "IMDB"},
		{ID: "http://example.com", SourceName: "Official Website"},
	}}
	if id, ok := m.TmdbID(); ok {
		t.Fatalf("got (%d,true), want no TMDB id among IMDB/website sources", id)
	}
}

// A malformed entry must not shadow a valid one further down the list.
func TestTmdbIDSkipsUnusableEntries(t *testing.T) {
	m := &MovieExtendedRecord{RemoteIDs: []RemoteID{
		{ID: "not-a-number", SourceName: "TheMovieDB.com"},
		{ID: "0", SourceName: "TheMovieDB.com"},
		{ID: "603", SourceName: "TheMovieDB.com"},
	}}
	id, ok := m.TmdbID()
	if !ok || id != 603 {
		t.Fatalf("got (%d,%v), want (603,true) from the third entry", id, ok)
	}
}

func TestTmdbIDOnAMissingRecord(t *testing.T) {
	var m *MovieExtendedRecord
	if id, ok := m.TmdbID(); ok {
		t.Fatalf("got (%d,true) on a nil record", id)
	}
}
