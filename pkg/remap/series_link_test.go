package remap

import (
	"context"
	"fmt"
	"testing"

	"github.com/Pasithea0/go-tmdb-episodes-fix/pkg/tvdb"
)

type fakeTranslations struct {
	byLang map[string]*tvdb.SeriesTranslationRecord
}

func (f fakeTranslations) GetSeriesTranslation(_ context.Context, _ int, language string) (*tvdb.SeriesTranslationRecord, error) {
	if tr, ok := f.byLang[language]; ok {
		return tr, nil
	}
	return nil, fmt.Errorf("no %s translation", language)
}

// The remote-id echo is what settles links no title comparison can, so its source
// spellings and its number matching must be exact.
func TestSeriesRemoteIDMatches(t *testing.T) {
	ids := []tvdb.RemoteID{
		{SourceName: "IMDB", ID: "tt0131179"},
		{SourceName: "TheMovieDB.com", ID: "30983"},
		{SourceName: "TV Maze", ID: "5429"},
	}
	if !seriesRemoteIDMatches(ids, 30983) {
		t.Error("a TheMovieDB.com echo must corroborate")
	}
	if seriesRemoteIDMatches(ids, 5429) {
		t.Error("a TV Maze id must not corroborate as a TMDB id")
	}
	if seriesRemoteIDMatches(ids, 30984) {
		t.Error("a different number must not corroborate")
	}
	for _, spelling := range []string{"TMDB", "themoviedb", "TheMovieDB.com"} {
		if !seriesRemoteIDMatches([]tvdb.RemoteID{{SourceName: spelling, ID: "7"}}, 7) {
			t.Errorf("source spelling %q must match", spelling)
		}
	}
}

// The language list is bounded and English-first, and never repeats.
func TestCorroborationLanguages(t *testing.T) {
	got := corroborationLanguages([]tvdb.Alias{
		{Language: "jpn"}, {Language: "cat"}, {Language: "eng"},
		{Language: "jpn"}, {Language: "deu"}, {Language: "fra"}, {Language: "spa"},
	})
	if len(got) == 0 || got[0] != "eng" {
		t.Fatalf("eng must be first, got %v", got)
	}
	if len(got) > maxSeriesTranslationLookups {
		t.Fatalf("language list not capped: %v", got)
	}
	seen := map[string]bool{}
	for _, l := range got {
		if seen[l] {
			t.Fatalf("duplicate language %q in %v", l, got)
		}
		seen[l] = true
	}
}

func TestSeriesCorroboratesTmdbSignals(t *testing.T) {
	ctx := context.Background()
	trans := fakeTranslations{byLang: map[string]*tvdb.SeriesTranslationRecord{
		"eng": {Name: "Detective Conan", Language: "eng"},
	}}
	aliases := []tvdb.Alias{{Language: "eng", Name: "Case Closed"}}
	withEcho := &tvdb.SeriesExtendedRecord{
		ID: 72454, Name: "名探偵コナン", Aliases: aliases,
		RemoteIDs: []tvdb.RemoteID{{SourceName: "TheMovieDB.com", ID: "30983"}},
	}
	withoutEcho := &tvdb.SeriesExtendedRecord{ID: 72454, Name: "名探偵コナン", Aliases: aliases}

	if sig, ok := SeriesCorroboratesTmdb(ctx, trans, withEcho, 30983, "Detective Conan"); !ok || sig != "remote_id_echo" {
		t.Fatalf("the echo should corroborate first: %q %v", sig, ok)
	}
	if sig, ok := SeriesCorroboratesTmdb(ctx, trans, withoutEcho, 30983, "Detective Conan"); !ok || sig != "translation:eng" {
		t.Fatalf("the English translation should corroborate: %q %v", sig, ok)
	}
	if sig, ok := SeriesCorroboratesTmdb(ctx, trans, withoutEcho, 30983, "Vanished Name"); ok {
		t.Fatalf("no signal must not corroborate: %q", sig)
	}
	if _, ok := SeriesCorroboratesTmdb(ctx, trans, nil, 30983, "Detective Conan"); ok {
		t.Fatal("a nil series must not corroborate")
	}
}