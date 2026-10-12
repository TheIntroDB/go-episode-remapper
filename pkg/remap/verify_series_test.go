package remap

import (
	"context"
	"strings"
	"testing"

	"github.com/Pasithea0/go-tmdb-episodes-fix/pkg/tvdb"
)

// seriesLinkFixture is the evidence for one TMDB external_ids link: TMDB's name for
// the series, and the TVDB record the id points at (canonical name, aliases, the
// record's own remote ids, and its per-language translations). tmdbImdbID, when set,
// is what the mapper's SeriesImdbID lookup answers -- the third corroboration signal.
type seriesLinkFixture struct {
	tmdbName     string
	tmdbID       int
	tvdbID       int
	canonical    string
	aliases      []tvdb.Alias
	remoteIDs    []tvdb.RemoteID
	translations map[string]*tvdb.SeriesTranslationRecord
	tmdbImdbID   string
}

func linkMapper(f seriesLinkFixture) *Mapper {
	if f.tmdbID == 0 {
		f.tmdbID = 900
	}
	if f.tvdbID == 0 {
		f.tvdbID = 73871
	}
	m := &Mapper{
		tvdb: &fakeTVDB{
			seriesByID:         map[int]*tvdb.SeriesBaseRecord{f.tvdbID: {ID: f.tvdbID, Name: f.canonical}},
			aliasesByID:        map[int][]tvdb.Alias{f.tvdbID: f.aliases},
			seriesRemoteIDs:    map[int][]tvdb.RemoteID{f.tvdbID: f.remoteIDs},
			seriesTranslations: map[int]map[string]*tvdb.SeriesTranslationRecord{f.tvdbID: f.translations},
		},
		tmdb: &fakeTMDB{
			names:  map[int]string{f.tmdbID: f.tmdbName},
			tvdbID: map[int]int{f.tmdbID: f.tvdbID},
		},
		tvdbSeasonType:      "default",
		fallbackSeasonTypes: defaultFallbackSeasonTypes,
		maxTVDBPages:        200,
		maxTMDBSeasons:      300,
	}
	if f.tmdbImdbID != "" {
		m.seriesImdbID = func(_ context.Context, _ int) (string, error) { return f.tmdbImdbID, nil }
	}
	return m
}

// Route 1 is TMDB's own external_ids field, which is user-contributed and can be
// wrong, and the caller stores TVDB ids -- so the link is trusted only when the TVDB
// record CORROBORATES it: by echoing the TMDB id in its own remoteIds (language-free),
// or by the name matching in some language (canonical, alias, or a per-language
// translation). A record that corroborates by neither is refused.
func TestResolveVerifiedSeriesCorroboratesTMDBExternalIDs(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name      string
		fixture   seriesLinkFixture
		wantProv  string
		wantNoErr bool
	}{
		{
			name: "exact canonical name matches",
			fixture: seriesLinkFixture{
				tmdbName: "Futurama", canonical: "Futurama",
			},
			wantProv: "tmdb_external_ids:name", wantNoErr: true,
		},
		{
			name: "name found in an alias",
			fixture: seriesLinkFixture{
				tmdbName: "Attack on Titan", canonical: "進撃の巨人",
				aliases: []tvdb.Alias{{Language: "eng", Name: "Attack on Titan"}},
			},
			wantProv: "tmdb_external_ids:name", wantNoErr: true,
		},
		{
			// The measured real case: TVDB files 青の祓魔師 with romaji/translation
			// aliases and NOT "Blue Exorcist", so no name comparison reaches it -- the
			// record's own remoteIds echo is what settles it.
			name: "aliases do not match, but the record echoes the TMDB id",
			fixture: seriesLinkFixture{
				tmdbName: "Blue Exorcist", canonical: "青の祓魔師",
				aliases:   []tvdb.Alias{{Language: "eng", Name: "Ao no Exorcist"}},
				remoteIDs: []tvdb.RemoteID{{SourceName: "TheMovieDB.com", ID: "900"}},
			},
			wantProv: "tmdb_external_ids:remote_id_echo", wantNoErr: true,
		},
		{
			// 名探偵コナン: aliases carry no "Detective Conan", the record echoes no
			// TMDB id, but TVDB's English translation IS the TMDB name.
			name: "no name overlap and no echo, but the English translation matches",
			fixture: seriesLinkFixture{
				tmdbName: "Detective Conan", canonical: "名探偵コナン",
				aliases: []tvdb.Alias{
					{Language: "jpn", Name: "Meitantei Conan"},
					{Language: "cat", Name: "Detectiu Conan"},
					{Language: "eng", Name: "Case Closed"},
				},
				translations: map[string]*tvdb.SeriesTranslationRecord{
					"eng": {Name: "Detective Conan", Language: "eng"},
				},
			},
			wantProv: "tmdb_external_ids:translation:eng", wantNoErr: true,
		},
		{
			// The bucket a wrong or stale external_ids falls into: nothing about the
			// TVDB record says this is TMDB 900.
			name: "no echo, no alias, no translation -> refused",
			fixture: seriesLinkFixture{
				tmdbName: "Vanished Name", canonical: "隐身的名字",
			},
			wantNoErr: false,
		},
		{
			// A transient GetTVDetails failure leaves no name, but the echo alone is
			// still evidence, so the link survives.
			name: "no TMDB name, but the record echoes the id",
			fixture: seriesLinkFixture{
				tmdbName: "", canonical: "進撃の巨人",
				remoteIDs: []tvdb.RemoteID{{SourceName: "TheMovieDB.com", ID: "900"}},
			},
			wantProv: "tmdb_external_ids:remote_id_echo", wantNoErr: true,
		},
		{
			name: "no TMDB name and no echo -> refused",
			fixture: seriesLinkFixture{
				tmdbName: "", canonical: "進撃の巨人",
			},
			wantNoErr: false,
		},
		{
			// The measured real case: TVDB has NO TMDB remote id and the title differs,
			// but both records name the same IMDb id. Travelers (tvdb 314544 / tmdb
			// 67683) is "Travelers (2016)" vs "Travelers", both tt5651844.
			name: "imdb id echoes on both records -> accepted",
			fixture: seriesLinkFixture{
				tmdbName: "Travelers", canonical: "Travelers (2016)",
				remoteIDs: []tvdb.RemoteID{
					{SourceName: "IMDb", ID: "tt5651844"},
					{SourceName: "TMS (Zap2It)", ID: "EP02506429"},
				},
				tmdbImdbID: "tt5651844",
			},
			wantProv: "tmdb_external_ids:imdb_echo", wantNoErr: true,
		},
		{
			// Digimon Adventure (tvdb 72241 / tmdb 31654): a Japanese title with no
			// English alias, both records tt0210418.
			name: "imdb echo, original-language title -> accepted",
			fixture: seriesLinkFixture{
				tmdbName: "Digimon: Digital Monsters", canonical: "デジモンアドベンチャー",
				remoteIDs:  []tvdb.RemoteID{{SourceName: "IMDb", ID: "tt0210418"}},
				tmdbImdbID: "tt0210418",
			},
			wantProv: "tmdb_external_ids:imdb_echo", wantNoErr: true,
		},
		{
			name: "imdb ids differ -> still refused",
			fixture: seriesLinkFixture{
				tmdbName: "Travelers", canonical: "Travelers (2016)",
				remoteIDs:  []tvdb.RemoteID{{SourceName: "IMDb", ID: "tt5651844"}},
				tmdbImdbID: "tt9999999",
			},
			wantNoErr: false,
		},
		{
			// No lookup configured: the signal is simply unavailable, which must not
			// change any other answer.
			name: "imdb would match but no lookup is configured -> refused",
			fixture: seriesLinkFixture{
				tmdbName: "Travelers", canonical: "Travelers (2016)",
				remoteIDs: []tvdb.RemoteID{{SourceName: "IMDb", ID: "tt5651844"}},
			},
			wantNoErr: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := linkMapper(c.fixture)
			series, prov, err := m.resolveVerifiedTVDBSeries(ctx, 900)
			if c.wantNoErr && err != nil {
				t.Fatalf("expected a resolution, got error: %v", err)
			}
			if !c.wantNoErr {
				if err == nil {
					t.Fatalf("expected a refusal, got series %+v (provenance %q)", series, prov)
				}
				return
			}
			if series == nil || series.ID != 73871 {
				t.Fatalf("series = %+v, want id %d", series, 73871)
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
