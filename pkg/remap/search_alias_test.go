package remap

import (
	"context"
	"testing"

	"github.com/Pasithea0/go-tmdb-episodes-fix/pkg/tvdb"
)

// The search record carries aliases, so route 2 can decide an alias match from the
// record already in hand -- no second fetch.
//
// seriesByID is deliberately EMPTY: if the code still called GetSeriesExtended to read
// aliases, that call would error and this test would fail. That is the point of the
// test, not an oversight.
func TestResolveVerifiedSeriesUsesSearchRecordAliases(t *testing.T) {
	m := &Mapper{
		tvdb: &fakeTVDB{
			seriesByRemote: map[string]*tvdb.SeriesBaseRecord{
				"tmdb-900": {ID: 73871, Name: "進撃の巨人", Aliases: []tvdb.Alias{
					{Language: "jpn", Name: "進撃の巨人"},
					{Language: "eng", Name: "Attack on Titan"},
				}},
			},
			seriesByID: map[int]*tvdb.SeriesBaseRecord{}, // no extended fetch available
		},
		tmdb: &fakeTMDB{
			names:  map[int]string{900: "Attack on Titan"},
			tvdbID: map[int]int{}, // no external_ids route: force the search route
		},
		tvdbSeasonType:      "default",
		fallbackSeasonTypes: defaultFallbackSeasonTypes,
		maxTVDBPages:        200,
		maxTMDBSeasons:      300,
	}

	series, prov, err := m.resolveVerifiedTVDBSeries(context.Background(), 900)
	if err != nil {
		t.Fatalf("expected an alias match from the search record, got: %v", err)
	}
	if series == nil || series.ID != 73871 {
		t.Fatalf("series = %+v, want id 73871", series)
	}
	if prov != "tvdb_search:tmdb-900:alias" {
		t.Fatalf("provenance = %q, want %q", prov, "tvdb_search:tmdb-900:alias")
	}
}