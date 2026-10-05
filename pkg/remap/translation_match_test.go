package remap

import (
	"context"
	"testing"

	"github.com/Pasithea0/go-tmdb-episodes-fix/pkg/tvdb"
)

// translationMapper builds a Mapper whose episodes carry per-language names, the way
// TVDB returns them when the request asks for translations.
func translationMapper(translations map[int64][]tvdb.Translation) *Mapper {
	eps := map[int64]tvdb.EpisodeBaseRecord{}
	for id := range translations {
		eps[id] = tvdb.EpisodeBaseRecord{ID: id}
	}
	// Names are set by the caller through candidates, but the extended record must
	// resolve, so index every id we might be asked about.
	for _, id := range []int64{4600001, 4600002, 4600003} {
		if _, ok := eps[id]; !ok {
			eps[id] = tvdb.EpisodeBaseRecord{ID: id}
		}
	}
	return &Mapper{
		tvdb: &fakeTVDB{
			episodesByID:        eps,
			episodeTranslations: translations,
		},
		tmdb:                &fakeTMDB{},
		tvdbSeasonType:      "default",
		fallbackSeasonTypes: defaultFallbackSeasonTypes,
		maxTVDBPages:        200,
		maxTMDBSeasons:      300,
	}
}

// The measured case: TVDB series 388150 s2e5 and s2e6 both aired 2021-12-01, so the tie
// had to be broken by name -- and the canonical names are Italian while TMDB's is
// English, so the tie could not be broken and the request 404'd. TVDB's own English
// translation is exactly "Mum is always mum".
func TestPickBestByTranslationBreaksTheMeasuredTie(t *testing.T) {
	m := translationMapper(map[int64][]tvdb.Translation{
		4600002: {{Language: "eng", Name: "Mum is always mum"}},
	})

	candidates := []tvdb.EpisodeBaseRecord{
		{ID: 4600001, Name: "Il passato che ritorna", Aired: "2021-12-01", SeasonNumber: 2, Number: 5},
		{ID: 4600002, Name: "La mamma è sempre la mamma", Aired: "2021-12-01", SeasonNumber: 2, Number: 6},
	}

	got := m.pickBestByTranslation(context.Background(), candidates, "Episode 6 - Mum is always mum")
	if got == nil {
		t.Fatal("expected the English translation to break the tie, got nil")
	}
	if got.ID != 4600002 {
		t.Fatalf("picked episode %d, want 4600002 (s2e6)", got.ID)
	}
	if got.Number != 6 {
		t.Fatalf("picked number %d, want 6", got.Number)
	}
}

// Ambiguity must be REFUSED, not resolved by taking the first. Two candidates whose
// translations both match is not a match.
func TestPickBestByTranslationRefusesAmbiguity(t *testing.T) {
	m := translationMapper(map[int64][]tvdb.Translation{
		4600001: {{Language: "eng", Name: "Same Title"}},
		4600002: {{Language: "eng", Name: "Same Title"}},
	})

	candidates := []tvdb.EpisodeBaseRecord{
		{ID: 4600001, Name: "Uno", Aired: "2021-12-01"},
		{ID: 4600002, Name: "Due", Aired: "2021-12-01"},
	}

	if got := m.pickBestByTranslation(context.Background(), candidates, "Same Title"); got != nil {
		t.Fatalf("expected a refusal on a two-way match, got episode %d", got.ID)
	}
}

// No translation matches: refuse, so the caller keeps its existing error rather than
// being handed a guess.
func TestPickBestByTranslationRefusesWhenNothingMatches(t *testing.T) {
	m := translationMapper(map[int64][]tvdb.Translation{
		4600001: {{Language: "eng", Name: "Something Else"}},
	})

	candidates := []tvdb.EpisodeBaseRecord{{ID: 4600001, Name: "Uno", Aired: "2021-12-01"}}

	if got := m.pickBestByTranslation(context.Background(), candidates, "Nothing Like This"); got != nil {
		t.Fatalf("expected a refusal, got episode %d", got.ID)
	}
}

// The canonical name still counts: a candidate whose primary name matches is found even
// with no translations at all.
func TestPickBestByTranslationUsesCanonicalNameToo(t *testing.T) {
	m := translationMapper(nil)

	candidates := []tvdb.EpisodeBaseRecord{
		{ID: 4600001, Name: "Il passato che ritorna"},
		{ID: 4600002, Name: "Mum is always mum"},
	}

	got := m.pickBestByTranslation(context.Background(), candidates, "Episode 6 - Mum is always mum")
	if got == nil || got.ID != 4600002 {
		t.Fatalf("got %+v, want episode 4600002", got)
	}
}