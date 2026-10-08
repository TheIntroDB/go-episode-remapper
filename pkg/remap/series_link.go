package remap

import (
	"context"
	"strconv"
	"strings"

	"github.com/Pasithea0/go-tmdb-episodes-fix/pkg/tvdb"
)

// SeriesTranslationFetcher is the one TVDB capability link corroboration needs
// beyond the series record itself. *tvdb.Client satisfies it.
type SeriesTranslationFetcher interface {
	GetSeriesTranslation(ctx context.Context, seriesID int, language string) (*tvdb.SeriesTranslationRecord, error)
}

// maxSeriesTranslationLookups bounds the extra requests a corroboration may cost.
const maxSeriesTranslationLookups = 4

// SeriesCorroboratesTmdb reports whether a TVDB series record corroborates a TMDB
// external_ids link to it, naming the signal that did.
//
// WHY THIS EXISTS. TMDB's external_ids is a field about TMDB's own record, but it
// is user-contributed and can be stale or simply wrong, and the caller stores TVDB
// ids -- so trusting it unverified relabels a whole series' submissions. Two
// independent signals are accepted, strongest first:
//
//  1. TVDB's own record ECHOES this TMDB id in its remoteIds. Both providers then
//     assert the same link, which is language-free and settles shows no title
//     comparison can (measured: TVDB 72454 is 名探偵コナン, its aliases contain no
//     "Detective Conan", and its record still lists TheMovieDB.com=30983).
//  2. The name matches in SOME language: the canonical title, any alias, or a
//     per-language translation. Translations are fetched only when the first two
//     fail, and only for a bounded set of languages (English first, then the
//     languages the aliases are tagged with), because each one is a request.
//
// A record that corroborates by neither is NOT this link: the caller must not
// accept it. That is exactly the bucket a wrong or stale external_ids falls into.
func SeriesCorroboratesTmdb(ctx context.Context, c SeriesTranslationFetcher, series *tvdb.SeriesExtendedRecord, tmdbID int, wantName string) (string, bool) {
	if series == nil {
		return "", false
	}
	if seriesRemoteIDMatches(series.RemoteIDs, tmdbID) {
		return "remote_id_echo", true
	}
	if corroboratesSeriesName(wantName, series.Name, series.Aliases) {
		return "name", true
	}
	for _, lang := range corroborationLanguages(series.Aliases) {
		tr, err := c.GetSeriesTranslation(ctx, series.ID, lang)
		if err != nil || tr == nil {
			continue
		}
		if namesCorroborate(wantName, tr.Name) || corroboratesSeriesName(wantName, tr.Name, tr.Aliases) {
			return "translation:" + lang, true
		}
	}
	return "", false
}

// seriesRemoteIDMatches reports whether a TVDB series' own remote ids claim a TMDB
// id -- a bidirectional assertion rather than one provider's claim.
func seriesRemoteIDMatches(remoteIDs []tvdb.RemoteID, tmdbID int) bool {
	want := strconv.Itoa(tmdbID)
	for _, r := range remoteIDs {
		if isTmdbRemoteSource(r.SourceName) && strings.TrimSpace(r.ID) == want {
			return true
		}
	}
	return false
}

// isTmdbRemoteSource matches the spellings TVDB uses for TMDB in a remote id's
// source name ("TheMovieDB.com", "TMDB", ...).
func isTmdbRemoteSource(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return strings.Contains(n, "tmdb") || strings.Contains(n, "themoviedb")
}

// corroborationLanguages is the bounded language list to try for a name check:
// English first (the usual gap between TVDB's canonical title and TMDB's), then the
// languages the aliases already carry, deduplicated and capped.
func corroborationLanguages(aliases []tvdb.Alias) []string {
	seen := map[string]bool{"eng": true}
	out := []string{"eng"}
	for _, a := range aliases {
		lang := strings.ToLower(strings.TrimSpace(a.Language))
		if lang == "" || seen[lang] {
			continue
		}
		seen[lang] = true
		out = append(out, lang)
		if len(out) >= maxSeriesTranslationLookups {
			break
		}
	}
	return out
}
