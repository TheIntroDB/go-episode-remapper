package tvdb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultBaseURL = "https://api4.thetvdb.com/v4"

type Client struct {
	baseURL string
	apiKey  string
	pin     string
	http    *http.Client

	mu    sync.Mutex
	token string
}

func NewClient(apiKey string, pin string) *Client {
	return &Client{
		baseURL: defaultBaseURL,
		apiKey:  strings.TrimSpace(apiKey),
		pin:     strings.TrimSpace(pin),
		http: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
}

func (c *Client) ensureToken(ctx context.Context) error {
	c.mu.Lock()
	hasToken := c.token != ""
	c.mu.Unlock()

	if hasToken {
		return nil
	}

	if c.apiKey == "" {
		return errors.New("tvdb api key is required")
	}

	body := map[string]string{"apikey": c.apiKey}
	if c.pin != "" {
		body["pin"] = c.pin
	}

	var resp struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
		Status string `json:"status"`
	}
	if err := c.do(ctx, http.MethodPost, "/login", nil, body, &resp); err != nil {
		return err
	}

	if strings.TrimSpace(resp.Data.Token) == "" {
		return errors.New("tvdb login succeeded but returned empty token")
	}

	c.mu.Lock()
	c.token = resp.Data.Token
	c.mu.Unlock()
	return nil
}

func (c *Client) do(ctx context.Context, method string, path string, query url.Values, body any, out any) error {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return err
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + path
	if query != nil {
		u.RawQuery = query.Encode()
	}

	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, u.String(), bodyReader)
	if err != nil {
		return err
	}

	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if path != "/login" {
		if err := c.ensureToken(ctx); err != nil {
			return err
		}
		c.mu.Lock()
		token := c.token
		c.mu.Unlock()
		req.Header.Set("Authorization", "Bearer "+token)
	}

	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 1500 {
			msg = msg[:1500]
		}
		if res.StatusCode == http.StatusNotFound {
			return &NotFoundError{Method: method, Path: path, Body: msg}
		}
		return fmt.Errorf("tvdb %s %s: status=%d body=%s", method, path, res.StatusCode, msg)
	}

	if out == nil {
		return nil
	}

	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("tvdb %s %s: decode error: %w (body=%s)", method, path, err, string(raw))
	}
	return nil
}

// NotFoundError indicates the requested TVDB resource returned HTTP 404.
// The mapper treats it as "this season type / episode isn't present under this
// ordering" so it can fall through to other season types rather than aborting,
// which is how the season-type fallback is actually exercised (TVDB 404s a
// series' episodes under "default" when the show only exposes "official"/"dvd").
type NotFoundError struct {
	Method string
	Path   string
	Body   string
}

func (e *NotFoundError) Error() string {
	body := strings.TrimSpace(e.Body)
	if len(body) > 1500 {
		body = body[:1500]
	}
	return fmt.Sprintf("tvdb %s %s: status=404 body=%s", e.Method, e.Path, body)
}

type RemoteID struct {
	ID         string `json:"id"`
	SourceName string `json:"sourceName"`
}

type SeriesBaseRecord struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// Alias is one alternative title TVDB files a series under.
//
// TVDB catalogues many shows under their ORIGINAL-LANGUAGE title and lists the name
// the rest of the world uses here, so a corroboration check that reads only `Name`
// rejects correct links -- which is exactly what happened (see
// corroboratesSeriesName in pkg/remap).
type Alias struct {
	Language string `json:"language"`
	Name     string `json:"name"`
}

type SeriesExtendedRecord struct {
	ID        int        `json:"id"`
	Name      string     `json:"name"`
	Aliases   []Alias    `json:"aliases"`
	RemoteIDs []RemoteID `json:"remoteIds"`
}

type EpisodeBaseRecord struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Aired        string `json:"aired"`
	SeriesID     int    `json:"seriesId"`
	SeasonNumber int    `json:"seasonNumber"`
	Number       int    `json:"number"`
}

type EpisodeExtendedRecord struct {
	ID           int64      `json:"id"`
	Name         string     `json:"name"`
	Aired        string     `json:"aired"`
	SeriesID     int        `json:"seriesId"`
	SeasonNumber int        `json:"seasonNumber"`
	Number       int        `json:"number"`
	RemoteIDs    []RemoteID `json:"remoteIds"`
}

type SearchByRemoteIdResult struct {
	Series *SeriesBaseRecord `json:"series"`
	// Movie is populated for movie remote ids. TVDB tags each result with the kind
	// it is, so a caller must select the key it wants rather than take the first
	// entry: one lookup can return a movie and a series at once, because TMDB ids
	// are unique per namespace but a film and a TV show can share the same number.
	Movie *MovieBaseRecord `json:"movie"`
}

type MovieBaseRecord struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Year    string `json:"year"`
	Slug    string `json:"slug"`
	Runtime int    `json:"runtime"`
}

type MovieExtendedRecord struct {
	ID        int        `json:"id"`
	Name      string     `json:"name"`
	Year      string     `json:"year"`
	Slug      string     `json:"slug"`
	Runtime   int        `json:"runtime"`
	RemoteIDs []RemoteID `json:"remoteIds"`
}

// TmdbID returns the movie's TMDB id from its remote ids, reporting whether one is
// present. TVDB lists it as "TheMovieDB.com"; the check is case-insensitive and
// ignores the separators TVDB varies between records.
//
// The reverse direction is not symmetric: this reads the id TVDB stores, whereas
// finding a video by TMDB id needs the remote-id search (see FindMovieByTMDBID).
func (m *MovieExtendedRecord) TmdbID() (int, bool) {
	if m == nil {
		return 0, false
	}
	for _, rid := range m.RemoteIDs {
		source := strings.ToLower(strings.TrimSpace(rid.SourceName))
		source = strings.NewReplacer(" ", "", ".", "", "-", "", "_", "").Replace(source)
		if source != "themoviedbcom" && source != "themoviedb" && source != "tmdb" {
			continue
		}
		id, err := strconv.Atoi(strings.TrimSpace(rid.ID))
		if err != nil || id <= 0 {
			continue
		}
		return id, true
	}
	return 0, false
}

func (c *Client) SearchSeriesByRemoteID(ctx context.Context, remoteID string) (*SeriesBaseRecord, error) {
	var resp struct {
		Data   []SearchByRemoteIdResult `json:"data"`
		Status string                   `json:"status"`
	}

	if err := c.do(ctx, http.MethodGet, "/search/remoteid/"+url.PathEscape(remoteID), nil, nil, &resp); err != nil {
		return nil, err
	}

	for _, item := range resp.Data {
		if item.Series != nil && item.Series.ID != 0 {
			return item.Series, nil
		}
	}
	return nil, nil
}

// FindSeriesByIMDbID resolves a TVDB series from an IMDb id (e.g. "tt0434665").
// IMDb and TVDB usually share the same season/episode numbering, so the returned
// series id can drive the tvdb->tmdb episode mapping for feeds that number by IMDb.
func (c *Client) FindSeriesByIMDbID(ctx context.Context, imdbID string) (*SeriesBaseRecord, string, error) {
	imdbID = strings.TrimSpace(imdbID)
	if imdbID == "" {
		return nil, "", errors.New("imdb id is required")
	}

	candidates := []string{
		imdbID,
		"imdb-" + imdbID,
		"imdb:" + imdbID,
	}

	for _, candidate := range candidates {
		s, err := c.SearchSeriesByRemoteID(ctx, candidate)
		if err != nil {
			return nil, "", err
		}
		if s != nil {
			return s, candidate, nil
		}
	}
	return nil, "", nil
}

func (c *Client) FindSeriesByTMDBID(ctx context.Context, tmdbID int) (*SeriesBaseRecord, string, error) {
	candidates := []string{
		strconv.Itoa(tmdbID),
		"tmdb-" + strconv.Itoa(tmdbID),
		"tmdb:" + strconv.Itoa(tmdbID),
		"themoviedb-" + strconv.Itoa(tmdbID),
		"themoviedb:" + strconv.Itoa(tmdbID),
	}

	for _, candidate := range candidates {
		s, err := c.SearchSeriesByRemoteID(ctx, candidate)
		if err != nil {
			return nil, "", err
		}
		if s != nil {
			return s, candidate, nil
		}
	}
	return nil, "", nil
}

func (c *Client) GetSeriesExtended(ctx context.Context, seriesID int) (*SeriesExtendedRecord, error) {
	var resp struct {
		Data   SeriesExtendedRecord `json:"data"`
		Status string               `json:"status"`
	}
	if err := c.do(ctx, http.MethodGet, "/series/"+strconv.Itoa(seriesID)+"/extended", nil, nil, &resp); err != nil {
		return nil, err
	}
	if resp.Data.ID == 0 {
		return nil, errors.New("tvdb series extended returned empty data")
	}
	return &resp.Data, nil
}

func (c *Client) GetSeriesEpisodes(ctx context.Context, seriesID int, seasonType string, page int, season *int, episodeNumber *int, airDate *string) ([]EpisodeBaseRecord, error) {
	query := url.Values{}
	query.Set("page", strconv.Itoa(page))
	if season != nil {
		query.Set("season", strconv.Itoa(*season))
	}
	if episodeNumber != nil {
		query.Set("episodeNumber", strconv.Itoa(*episodeNumber))
	}
	if airDate != nil && strings.TrimSpace(*airDate) != "" {
		query.Set("airDate", strings.TrimSpace(*airDate))
	}

	path := fmt.Sprintf("/series/%d/episodes/%s", seriesID, url.PathEscape(seasonType))
	var resp struct {
		Data struct {
			Episodes []EpisodeBaseRecord `json:"episodes"`
		} `json:"data"`
		Status string `json:"status"`
	}
	if err := c.do(ctx, http.MethodGet, path, query, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Data.Episodes, nil
}

func (c *Client) GetEpisodeExtended(ctx context.Context, episodeID int64) (*EpisodeExtendedRecord, error) {
	var resp struct {
		Data   EpisodeExtendedRecord `json:"data"`
		Status string                `json:"status"`
	}
	if err := c.do(ctx, http.MethodGet, "/episodes/"+strconv.FormatInt(episodeID, 10)+"/extended", nil, nil, &resp); err != nil {
		return nil, err
	}
	if resp.Data.ID == 0 {
		return nil, errors.New("tvdb episode extended returned empty data")
	}
	return &resp.Data, nil
}

// SearchMovieByRemoteID resolves a TVDB movie from a remote id (an IMDb id, a TMDB
// id, ...). It returns nil, nil when the lookup contains no movie, which is a normal
// outcome rather than an error: TVDB returns whatever kinds match, so a miss here
// means "not on TVDB", and a caller must not treat that as a transport failure.
//
// Only the movie key is considered. The same remote id can match a series at the
// same time, and taking the first entry would silently return a TV show for a film.
func (c *Client) SearchMovieByRemoteID(ctx context.Context, remoteID string) (*MovieBaseRecord, error) {
	if strings.TrimSpace(remoteID) == "" {
		return nil, errors.New("remote id is required")
	}

	var resp struct {
		Data   []SearchByRemoteIdResult `json:"data"`
		Status string                   `json:"status"`
	}

	if err := c.do(ctx, http.MethodGet, "/search/remoteid/"+url.PathEscape(remoteID), nil, nil, &resp); err != nil {
		return nil, err
	}

	for _, item := range resp.Data {
		if item.Movie != nil && item.Movie.ID != 0 {
			return item.Movie, nil
		}
	}
	return nil, nil
}

// FindMovieByIMDbID resolves a TVDB movie from an IMDb id (e.g. "tt0133093").
func (c *Client) FindMovieByIMDbID(ctx context.Context, imdbID string) (*MovieBaseRecord, string, error) {
	imdbID = strings.TrimSpace(imdbID)
	if imdbID == "" {
		return nil, "", errors.New("imdb id is required")
	}

	for _, candidate := range []string{imdbID, "imdb-" + imdbID, "imdb:" + imdbID} {
		m, err := c.SearchMovieByRemoteID(ctx, candidate)
		if err != nil {
			return nil, "", err
		}
		if m != nil {
			return m, candidate, nil
		}
	}
	return nil, "", nil
}

// FindMovieByTMDBID resolves a TVDB movie from a TMDB movie id.
//
// This is the only safe way to go from a TMDB id to a film on TVDB, and the reason
// is not obvious: /movies/{id} takes a TVDB id, and TVDB movie ids are a different
// namespace that overlaps TMDB's numerically. Passing a TMDB id to /movies/{id} does
// not fail -- it returns a real, different film (TMDB 603 is The Matrix, while TVDB
// movie 603 is Zombieland), so a mistake there is silent and looks like success.
func (c *Client) FindMovieByTMDBID(ctx context.Context, tmdbID int) (*MovieBaseRecord, string, error) {
	if tmdbID <= 0 {
		return nil, "", errors.New("tmdb id must be positive")
	}

	id := strconv.Itoa(tmdbID)
	for _, candidate := range []string{id, "tmdb-" + id, "tmdb:" + id, "themoviedb-" + id, "themoviedb:" + id} {
		m, err := c.SearchMovieByRemoteID(ctx, candidate)
		if err != nil {
			return nil, "", err
		}
		if m != nil {
			return m, candidate, nil
		}
	}
	return nil, "", nil
}

// GetMovieExtended fetches a movie's extended record.
//
// movieID is a TVDB movie id, never a TMDB one. See FindMovieByTMDBID: the two id
// spaces overlap numerically, so a TMDB id here returns a different film rather than
// an error, and the only symptom is wrong metadata.
func (c *Client) GetMovieExtended(ctx context.Context, movieID int) (*MovieExtendedRecord, error) {
	if movieID <= 0 {
		return nil, errors.New("tvdb movie id must be positive")
	}

	var resp struct {
		Data   MovieExtendedRecord `json:"data"`
		Status string              `json:"status"`
	}
	if err := c.do(ctx, http.MethodGet, "/movies/"+strconv.Itoa(movieID)+"/extended", nil, nil, &resp); err != nil {
		return nil, err
	}
	if resp.Data.ID == 0 {
		return nil, errors.New("tvdb movie extended returned empty data")
	}
	return &resp.Data, nil
}
