// SPDX-License-Identifier: MIT
// Copyright (C) 2026 TheIntroDB

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Pasithea0/go-tmdb-episodes-fix/pkg/remap"
)

func main() {
	loadDotEnv(".env")

	var (
		direction   = flag.String("direction", "tmdb2tvdb", "tmdb2tvdb, tvdb2tmdb, tvdb2imdb, or imdb2tmdb")
		tmdbID      = flag.Int("tmdb-id", 0, "TMDB series id")
		tvdbID      = flag.Int("tvdb-id", 0, "TVDB series id")
		imdbID      = flag.String("imdb-id", "", "IMDb series id (e.g. tt0434665)")
		season      = flag.Int("season", -1, "season number (0 = specials)")
		episode     = flag.Int("episode", 0, "episode number")
		tvdbKey     = flag.String("tvdb-key", getenv("TVDB_API_KEY"), "TVDB API key (or TVDB_API_KEY env var)")
		tvdbPin     = flag.String("tvdb-pin", getenv("TVDB_PIN"), "TVDB subscriber pin (or TVDB_PIN env var)")
		tmdbToken   = flag.String("tmdb-token", getenv("TMDB_BEARER_TOKEN"), "TMDB bearer token (or TMDB_BEARER_TOKEN env var)")
		seasonType  = flag.String("tvdb-season-type", "default", "TVDB season order (default, official, dvd, absolute, alternate, regional)")
		tvdbOrder   = flag.String("tvdb-order", "", "episode hint: the order the season/episode numbers belong to (official, dvd, absolute, alternate, regional, altdvd, alttwo); used alone, without fallback")
		episodeID   = flag.Int64("tvdb-episode-id", 0, "episode hint: TVDB episode id (unique across every order)")
		episodeName = flag.String("episode-name", "", "episode hint: the episode title, used as corroboration")
		batchSource = flag.String("mismatches", "", "batch mode: URL or file path of a feed-sync mismatches JSON ({items:[...]}); maps every unique imdb_id+season+episode")
	)

	flag.Parse()

	mapper := remap.NewMapper(remap.Options{
		TVDBAPIKey:      *tvdbKey,
		TVDBPIN:         *tvdbPin,
		TMDBBearerToken: *tmdbToken,
		TVDBSeasonType:  *seasonType,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	// Batch mode: map every unique (imdb_id, season, episode) in a feed-sync
	// mismatches JSON. Requires -mismatches to point at the URL or file.
	if *batchSource != "" {
		batchCtx, batchCancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer batchCancel()
		if err := runBatch(batchCtx, mapper, *batchSource); err != nil {
			exitErr(err.Error())
		}
		return
	}

	// Season 0 is legitimate -- TMDB and TVDB both keep specials there, and the
	// season scan reaches season 0 for exactly that case -- so only "unset" is an
	// error. That is why the flag defaults to -1 rather than 0.
	if *season < 0 || *episode <= 0 {
		exitErr("season (0 or greater) and episode (1 or greater) are required")
	}

	hints := remap.EpisodeHints{
		TVDBOrder:     *tvdbOrder,
		TVDBEpisodeID: *episodeID,
		EpisodeName:   *episodeName,
	}

	var out any
	switch *direction {
	case "tmdb2tvdb":
		if *tmdbID == 0 {
			exitErr("tmdb-id is required for tmdb2tvdb")
		}
		res, err := mapper.TmdbToTvdbWithHints(ctx, *tmdbID, *season, *episode, hints)
		if err != nil {
			exitErr(err.Error())
		}
		out = res
	case "tvdb2tmdb":
		if *tvdbID == 0 {
			exitErr("tvdb-id is required for tvdb2tmdb")
		}
		res, err := mapper.TvdbToTmdbWithHints(ctx, *tvdbID, *season, *episode, hints)
		if err != nil {
			exitErr(err.Error())
		}
		out = res
	case "tvdb2imdb":
		if *tvdbID == 0 {
			exitErr("tvdb-id is required for tvdb2imdb")
		}
		res, err := mapper.TvdbToImdbWithHints(ctx, *tvdbID, *season, *episode, hints)
		if err != nil {
			exitErr(err.Error())
		}
		out = res
	case "imdb2tmdb":
		if *imdbID == "" {
			exitErr("imdb-id is required for imdb2tmdb")
		}
		res, err := mapper.ImdbToTmdbWithHints(ctx, *imdbID, *tmdbID, *season, *episode, hints)
		if err != nil {
			exitErr(err.Error())
		}
		out = res
	default:
		exitErr("unknown direction: " + *direction)
	}

	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		exitErr(err.Error())
	}
	fmt.Println(string(b))
}

// batchKey identifies one unique (imdb_id, season, episode) mapping request.
type batchKey struct {
	imdb    string
	season  int
	episode int
}

// reportRow is one output row of batch mode: the input episode and its
// correctly-mapped TMDB numbers (or the error).
type reportRow struct {
	ImdbID          string   `json:"imdb_id"`
	TmdbID          int      `json:"tmdb_id,omitempty"`
	InputSeason     int      `json:"input_season"`
	InputEpisode    int      `json:"input_episode"`
	Title           string   `json:"title"`
	Segments        []string `json:"segments"`
	Matched         bool     `json:"matched"`
	Error           string   `json:"error,omitempty"`
	MatchedBy       string   `json:"matched_by,omitempty"`
	TVDBSeriesID    int      `json:"tvdb_series_id,omitempty"`
	TVDBEpisodeName string   `json:"tvdb_episode_name,omitempty"`
	TMDBSeriesID    int      `json:"tmdb_series_id,omitempty"`
	TMDBEpisodeID   int      `json:"tmdb_episode_id,omitempty"`
	TMDBSeason      int      `json:"tmdb_season,omitempty"`
	TMDBEpisode     int      `json:"tmdb_episode,omitempty"`
}

// mismatchItem is one entry of a feed-sync mismatches JSON.
type mismatchItem struct {
	ImdbID  string `json:"imdb_id"`
	TmdbID  int    `json:"tmdb_id"`
	Season  int    `json:"season"`
	Episode int    `json:"episode"`
	Title   string `json:"title"`
	Segment string `json:"segment"`
}

// dedupeMismatchItems collapses the raw mismatches list to one row per unique
// (imdb_id, season, episode), merging the segment names. Items without a usable
// imdb id / season / episode are dropped.
func dedupeMismatchItems(items []mismatchItem) []reportRow {
	rows := []reportRow{}
	idxByKey := map[batchKey]int{}

	for _, it := range items {
		if it.ImdbID == "" || it.Season <= 0 || it.Episode <= 0 {
			continue
		}
		k := batchKey{it.ImdbID, it.Season, it.Episode}
		if i, ok := idxByKey[k]; ok {
			if it.Segment != "" {
				rows[i].Segments = append(rows[i].Segments, it.Segment)
			}
			continue
		}
		row := reportRow{
			ImdbID:       it.ImdbID,
			TmdbID:       it.TmdbID,
			InputSeason:  it.Season,
			InputEpisode: it.Episode,
			Title:        it.Title,
		}
		if it.Segment != "" {
			row.Segments = []string{it.Segment}
		}
		idxByKey[k] = len(rows)
		rows = append(rows, row)
	}
	return rows
}

// runBatch loads a feed-sync mismatches JSON (URL or local file), dedupes by
// (imdb_id, season, episode), maps each with ImdbToTmdb, and prints a compact
// JSON report: the correctly-mapped tmdb season/episode per input.
func runBatch(ctx context.Context, mapper *remap.Mapper, source string) error {
	raw, err := loadMismatches(source)
	if err != nil {
		return err
	}

	var payload struct {
		Items []mismatchItem `json:"items"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("parse mismatches json: %w", err)
	}
	if len(payload.Items) == 0 {
		return errors.New("mismatches json has no items")
	}

	rows := dedupeMismatchItems(payload.Items)
	results := make([]reportRow, 0, len(rows))
	for _, row := range rows {
		rowCopy := row
		var (
			res *remap.ImdbToTmdbResult
			err error
		)
		if rowCopy.TmdbID > 0 {
			// The payload already knows the TMDB series id — use it directly
			// (TVDB remote ids are sometimes missing that mapping).
			res, err = mapper.ImdbToTmdbWithTMDB(ctx, rowCopy.ImdbID, rowCopy.TmdbID, rowCopy.InputSeason, rowCopy.InputEpisode)
		} else {
			res, err = mapper.ImdbToTmdb(ctx, rowCopy.ImdbID, rowCopy.InputSeason, rowCopy.InputEpisode)
		}
		if err != nil {
			rowCopy.Error = err.Error()
		} else {
			rowCopy.Matched = true
			rowCopy.MatchedBy = res.MatchedBy
			rowCopy.TVDBSeriesID = res.TVDBSeriesID
			rowCopy.TVDBEpisodeName = res.TVDBEpisodeName
			rowCopy.TMDBSeriesID = res.TMDBSeriesID
			rowCopy.TMDBEpisodeID = res.TMDBEpisodeID
			rowCopy.TMDBSeason = res.TMDBSeason
			rowCopy.TMDBEpisode = res.TMDBEpisode
		}
		results = append(results, rowCopy)
	}

	b, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

func loadMismatches(source string) ([]byte, error) {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		client := &http.Client{Timeout: 30 * time.Second}
		res, err := client.Get(source)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			return nil, fmt.Errorf("fetch %s: status=%d", source, res.StatusCode)
		}
		return io.ReadAll(res.Body)
	}
	return os.ReadFile(source)
}

func getenv(key string) string {
	v, _ := os.LookupEnv(key)
	return v
}

func loadDotEnv(relativePath string) {
	cwd, err := os.Getwd()
	if err != nil {
		return
	}
	path := filepath.Join(cwd, relativePath)
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if key == "" {
			continue
		}

		if (strings.HasPrefix(val, "\"") && strings.HasSuffix(val, "\"")) || (strings.HasPrefix(val, "'") && strings.HasSuffix(val, "'")) {
			val = strings.Trim(val, "\"'")
		}

		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		_ = os.Setenv(key, val)
	}
}

func exitErr(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}
