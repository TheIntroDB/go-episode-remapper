package remap

import (
	"context"
	"strings"

	"github.com/Pasithea0/go-tmdb-episodes-fix/pkg/tvdb"
)

// pickBestByTranslation breaks an air-date tie using TVDB's per-language episode names.
//
// WHY IT EXISTS. When several episodes share an air date the name breaks the tie, and a
// comparison against the canonical name alone fails whenever TVDB's primary language
// differs from TMDB's. Measured: TVDB series 388150 s2e6 is "La mamma è sempre la
// mamma" (Italian) while TMDB calls it "Episode 6 - Mum is always mum", so the tie
// between s2e5 and s2e6 could not be broken and the request 404'd -- even though TVDB's
// own English translation is exactly "Mum is always mum".
//
// WHY ONLY HERE. The episode LIST endpoint does not return translations, so each
// candidate costs one extra request. On this path the candidate set is the handful of
// episodes sharing one air date (measured: 2 to 5), so the cost is bounded and small.
// Running it over a whole season instead would be one request per episode, which is why
// this is a tie-breaker and not a general name lookup.
//
// It REFUSES AMBIGUITY rather than guessing: two candidates whose translations both
// match is not a match, and the caller falls through to its existing error. Picking the
// first is how a wrong episode reaches the library -- that is precisely the bug this
// tie-break exists to avoid.
func (m *Mapper) pickBestByTranslation(ctx context.Context, candidates []tvdb.EpisodeBaseRecord, wantName string) *tvdb.EpisodeBaseRecord {
	if strings.TrimSpace(wantName) == "" || len(candidates) == 0 {
		return nil
	}

	var best *tvdb.EpisodeBaseRecord
	matches := 0

	for i := range candidates {
		names := []string{candidates[i].Name}

		// A translation failure must not reject the episode: fall back to the
		// canonical name, which is what the caller already tried. Only a positive
		// match decides anything.
		if ext, err := m.tvdb.GetEpisodeExtended(ctx, candidates[i].ID); err == nil && ext != nil {
			if strings.TrimSpace(ext.Name) != "" {
				names = append(names, ext.Name)
			}
			for _, t := range ext.Translations.NameTranslations {
				if strings.TrimSpace(t.Name) != "" {
					names = append(names, t.Name)
				}
			}
		}

		if TitlesMatchAny(names, wantName) {
			matches++
			best = &candidates[i]
		}
	}

	if matches == 1 {
		return best
	}
	return nil
}