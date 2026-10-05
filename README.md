# go-episode-remapper
A go module to help remap between TVDB, TMDB, and IMDb episode order

## CLI
- Automatically loads `./.env` if present (without overriding already-exported env vars).
- Env vars:
  - TVDB_API_KEY
  - TVDB_PIN (optional)
  - TMDB_BEARER_TOKEN (required for tmdb2tvdb and for tvdb2tmdb/imdb2tmdb when TVDB remote ids are missing)

TMDB → TVDB:

```bash
go run ./cmd/remap -direction tmdb2tvdb -tmdb-id 123 -season 1 -episode 18
```

TVDB → TMDB:

```bash
go run ./cmd/remap -direction tvdb2tmdb -tvdb-id 456 -season 1 -episode 18
```

TVDB → IMDb (derives the numbering IMDb uses for the episode — see
[`pkg/remap/tvdb2imdb.md`](pkg/remap/tvdb2imdb.md) for how the order is chosen):

```bash
go run ./cmd/remap -direction tvdb2imdb -tvdb-id 73871 -season 0 -episode 2
```

IMDb → TMDB (resolves the TVDB series from the IMDb id; IMDb and TVDB usually
share the same season/episode numbering, so the TVDB record's name + air date
find the episode on TMDB even when TMDB's season structure differs):

```bash
go run ./cmd/remap -direction imdb2tmdb -imdb-id tt0434665 -season 14 -episode 19
```

Batch mode — map every unique (imdb_id, season, episode) in a feed-sync
mismatches JSON (URL or local file), deduped, with one result row each:

```bash
go run ./cmd/remap -mismatches https://feed-sync.pumpkin.st/mismatches
go run ./cmd/remap -mismatches ./mismatches.json
```

Episode identity hints — these qualify the season/episode numbers, which are only
meaningful relative to a TVDB numbering order (`alternate`, `dvd`, …). See
[`pkg/remap/hints.md`](pkg/remap/hints.md) for the full contract and the live
verification table.

```bash
# a library that follows TVDB's alternate order, sending alternate numbers
go run ./cmd/remap -direction tvdb2tmdb -tvdb-id 73871 -season 7 -episode 1 \
  -tvdb-order alternate -episode-name Rebirth

# pin the episode by its TVDB id (unique across every order)
go run ./cmd/remap -direction tvdb2tmdb -tvdb-id 73871 -season 6 -episode 1 \
  -tvdb-episode-id 8234611

# IMDb-numbered caller that already knows the TMDB series id
go run ./cmd/remap -direction imdb2tmdb -imdb-id tt0149460 -tmdb-id 615 \
  -season 7 -episode 1 -tvdb-order alternate
```

Flags: `-tvdb-order`, `-tvdb-episode-id`, `-episode-name`. Accepted order
spellings include the Jellyfin `Series.DisplayOrder` aliases `altdvd` and
`alttwo`.

## Library
- Entry point: pkg/remap
  - NewMapper(remap.Options)
  - (*Mapper).TmdbToTvdb(ctx, tmdbSeriesID, season, episode)
  - (*Mapper).TvdbToTmdb(ctx, tvdbSeriesID, season, episode)
  - (*Mapper).TvdbToTmdbWithHints(ctx, tvdbSeriesID, season, episode, remap.EpisodeHints{…})
  - (*Mapper).TvdbToImdb(ctx, tvdbSeriesID, season, episode)
  - (*Mapper).TvdbToImdbWithHints(ctx, tvdbSeriesID, season, episode, remap.EpisodeHints{…})
  - (*Mapper).ImdbToTmdb(ctx, imdbID, season, episode)
  - (*Mapper).ImdbToTmdbWithTMDB(ctx, imdbID, tmdbSeriesID, season, episode)
  - (*Mapper).ImdbToTmdbWithHints(ctx, imdbID, tmdbSeriesID /* 0 = resolve from TVDB */, season, episode, remap.EpisodeHints{…})
- The no-hints entry points are thin wrappers over the hint-aware ones, so their
  behaviour is unchanged. An unset `FallbackSeasonTypes` now takes the module's
  documented default (`official, dvd, alternate, regional`) instead of silently
  searching the primary season type only.
