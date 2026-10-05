package remap

import (
	"context"
	"strings"
	"testing"

	"github.com/Pasithea0/go-tmdb-episodes-fix/pkg/tvdb"
)

// seriesMapper builds a Mapper whose only identity evidence is TMDB's own
// external_ids field, so resolveVerifiedTVDBSeries exercises route 1.
func seriesMapper(tmdbName string, tmdbID, tvdbID int, canonical string, aliases []tvdb.Alias) *Mapper {
	return &Mapper{
		tvdb: &fakeTVDB{
			seriesByID:  map[int]*tvdb.SeriesBaseRecord{tvdbID: {ID: tvdbID, Name: canonical}},
			aliasesByID: map[int][]tvdb.Alias{tvdbID: aliases},
		},
		tmdb: &fakeTMDB{
			names:  map[int]string{tmdbID: tmdbName},
			tvdbID: map[int]int{tmdbID: tvdbID},
		},
		tvdbSeasonType:      "default",
		fallbackSeasonTypes: defaultFallbackSeasonTypes,
		maxTVDBPages:        200,
		maxTMDBSeasons:      300,
	}
}

// Route 1 is TMDB's own external_ids field -- a claim about TMDB's own record -- so a
// name disagreement must not reject it. Measured live: the series-level tvdb_id was
// correct in all five sampled failures while the name gate rejected every one.
func TestResolveVerifiedSeriesTrustsTMDBExternalIDs(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name         string
		tmdbName     string
		canonical    string
		aliases      []tvdb.Alias
		wantProv     string
		wantNoErr    bool
		wantSeriesID int
	}{
		{
			name:      "exact name match is unchanged",
			tmdbName:  "Futurama",
			canonical: "Futurama",
			wantProv:  "tmdb_external_ids", wantNoErr: true, wantSeriesID: 73871,
		},
		{
			name:      "name found in aliases resolves, provenance unchanged",
			tmdbName:  "Attack on Titan",
			canonical: "進撃の巨人",
			aliases:   []tvdb.Alias{{Language: "eng", Name: "Attack on Titan"}},
			wantProv:  "tmdb_external_ids", wantNoErr: true, wantSeriesID: 73871,
		},
		{
			// The measured real case, and the reason the alias check was not enough on
			// its own: TVDB lists 17 aliases -- romaji, translations, Cyrillic -- and
			// NOT the English name TMDB uses. It still resolves, via external_ids.
			name:      "Blue Exorcist: aliases exist but none matches, still resolves",
			tmdbName:  "Blue Exorcist",
			canonical: "青の祓魔師",
			aliases:   []tvdb.Alias{{Language: "eng", Name: "Ao no Exorcist"}},
			wantProv:  "tmdb_external_ids_name_mismatch", wantNoErr: true, wantSeriesID: 73871,
		},
		{
			// "Vanished Name" (TMDB) vs "隐身的名字" (TVDB), no aliases at all.
			// No string comparison can reach this, yet the id is correct.
			name:      "no name overlap still resolves, and says so",
			tmdbName:  "Vanished Name",
			canonical: "隐身的名字",
			wantProv:  "tmdb_external_ids_name_mismatch", wantNoErr: true, wantSeriesID: 73871,
		},
		{
			// The guard that used to sit before route 1 rejected this outright. Route 1
			// does not consult the name, so requiring one there was a rejection with
			// nothing to justify it -- and a transient GetTVDetails failure had the
			// same effect, silently disabling the most reliable route.
			name:      "no TMDB name at all still resolves via external_ids",
			tmdbName:  "",
			canonical: "進撃の巨人",
			wantProv:  "tmdb_external_ids_name_mismatch", wantNoErr: true, wantSeriesID: 73871,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := seriesMapper(c.tmdbName, 900, 73871, c.canonical, c.aliases)
			series, prov, err := m.resolveVerifiedTVDBSeries(ctx, 900)
			if c.wantNoErr && err != nil {
				t.Fatalf("expected a resolution, got error: %v", err)
			}
			if err != nil {
				return
			}
			if series == nil || series.ID != c.wantSeriesID {
				t.Fatalf("series = %+v, want id %d", series, c.wantSeriesID)
			}
			if prov != c.wantProv {
				t.Fatalf("provenance = %q, want %q", prov, c.wantProv)
			}
		})
	}
}

// The gate must still be a gate on the routes that need it: with no external_ids link
// and a name that matches nothing, the mapper refuses rather than inventing a series.
func TestResolveVerifiedSeriesStillRefusesWithoutEvidence(t *testing.T) {
	m := &Mapper{
		tvdb: &fakeTVDB{
			seriesByID: map[int]*tvdb.SeriesBaseRecord{73871: {ID: 73871, Name: "進撃の巨人"}},
		},
		tmdb: &fakeTMDB{
			names:  map[int]string{900: "Attack on Titan"},
			tvdbID: map[int]int{}, // no external_ids route available
		},
		tvdbSeasonType:      "default",
		fallbackSeasonTypes: defaultFallbackSeasonTypes,
		maxTVDBPages:        200,
		maxTMDBSeasons:      300,
	}

	_, _, err := m.resolveVerifiedTVDBSeries(context.Background(), 900)
	if err == nil {
		t.Fatal("expected a refusal when there is no evidence for a link")
	}
	if !strings.Contains(err.Error(), "no candidate whose title matches") {
		t.Fatalf("unexpected error text: %v", err)
	}
}