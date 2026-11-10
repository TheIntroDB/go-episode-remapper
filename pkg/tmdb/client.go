// SPDX-License-Identifier: MIT
// Copyright (C) 2026 TheIntroDB

package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const defaultBaseURL = "https://api.themoviedb.org/3"

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func NewClient(bearerToken string) *Client {
	return &Client{
		baseURL: defaultBaseURL,
		token:   strings.TrimSpace(bearerToken),
		http: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
}

func (c *Client) doGet(ctx context.Context, path string, query url.Values, out any) error {
	if c.token == "" {
		return errors.New("tmdb bearer token is required")
	}

	u, err := url.Parse(c.baseURL)
	if err != nil {
		return err
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + path
	if query != nil {
		u.RawQuery = query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

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
		return fmt.Errorf("tmdb GET %s: status=%d body=%s", path, res.StatusCode, msg)
	}

	if out == nil {
		return nil
	}

	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("tmdb GET %s: decode error: %w (body=%s)", path, err, string(raw))
	}
	return nil
}

type EpisodeDetails struct {
	ID            int
	Name          string
	AirDate       string
	SeasonNumber  int
	EpisodeNumber int
}

func (c *Client) GetEpisodeDetails(ctx context.Context, tvID int, season int, episode int) (*EpisodeDetails, error) {
	path := fmt.Sprintf("/tv/%d/season/%d/episode/%d", tvID, season, episode)
	var resp struct {
		ID            int    `json:"id"`
		Name          string `json:"name"`
		AirDate       string `json:"air_date"`
		SeasonNumber  int    `json:"season_number"`
		EpisodeNumber int    `json:"episode_number"`
	}
	if err := c.doGet(ctx, path, nil, &resp); err != nil {
		return nil, err
	}
	if resp.ID == 0 {
		return nil, errors.New("tmdb episode returned empty id")
	}
	return &EpisodeDetails{
		ID:            resp.ID,
		Name:          resp.Name,
		AirDate:       resp.AirDate,
		SeasonNumber:  resp.SeasonNumber,
		EpisodeNumber: resp.EpisodeNumber,
	}, nil
}

// EpisodeExternalIDs is TMDB's own cross-reference table for a single episode.
//
// TVDBID is the load-bearing field: it is the TVDB episode id for the DEFAULT
// order, which turns "which TVDB episode is TMDB s0e1?" from an air-date/name
// guess into an exact lookup.
//
// Verified live 2026-09-25 against Futurama (TMDB 615):
//
//	TMDB s0e1 Bender's Big Score      -> 342888 = TVDB default s0e2
//	TMDB s0e2 Everybody Loves Hypnotoad -> 389457 = TVDB default s0e1
//	TMDB s6e1 Rebirth                 -> 1051911 = TVDB default s6e1
//
// The first two are exactly the pair that evidence-based matching gets wrong:
// TMDB and TVDB default SWAP s0e1/s0e2 and both air 2007-11-27, so an air-date
// tie exists and TVDB's own airDate filter does not even return both of them.
type EpisodeExternalIDs struct {
	IMDbID string
	TVDBID int
}

// GetEpisodeExternalIDs reads /tv/{id}/season/{s}/episode/{e}/external_ids.
//
// A zero TVDBID is returned with a nil error, not as an error: TMDB does not
// carry a TVDB link for every episode, and callers must fall back to air date
// and name. Keeping "no link exists" distinct from "the request failed" is what
// lets the caller decide whether to fall through or to fail.
func (c *Client) GetEpisodeExternalIDs(ctx context.Context, tvID int, season int, episode int) (*EpisodeExternalIDs, error) {
	path := fmt.Sprintf("/tv/%d/season/%d/episode/%d/external_ids", tvID, season, episode)
	var resp struct {
		IMDbID string `json:"imdb_id"`
		TVDBID int    `json:"tvdb_id"`
	}
	if err := c.doGet(ctx, path, nil, &resp); err != nil {
		return nil, err
	}
	return &EpisodeExternalIDs{IMDbID: resp.IMDbID, TVDBID: resp.TVDBID}, nil
}

type EpisodeByID struct {
	ID            int
	Name          string
	AirDate       string
	SeasonNumber  int
	EpisodeNumber int
	ShowID        int
}

func (c *Client) GetEpisodeByID(ctx context.Context, episodeID int) (*EpisodeByID, error) {
	path := "/tv/episode/" + strconv.Itoa(episodeID)
	var resp struct {
		ID            int    `json:"id"`
		Name          string `json:"name"`
		AirDate       string `json:"air_date"`
		SeasonNumber  int    `json:"season_number"`
		EpisodeNumber int    `json:"episode_number"`
		ShowID        int    `json:"show_id"`
	}
	if err := c.doGet(ctx, path, nil, &resp); err != nil {
		return nil, err
	}
	if resp.ID == 0 {
		return nil, errors.New("tmdb episode-by-id returned empty id")
	}
	return &EpisodeByID{
		ID:            resp.ID,
		Name:          resp.Name,
		AirDate:       resp.AirDate,
		SeasonNumber:  resp.SeasonNumber,
		EpisodeNumber: resp.EpisodeNumber,
		ShowID:        resp.ShowID,
	}, nil
}

type TVDetails struct {
	// Name is the series title as TMDB has it. Used to corroborate a TVDB series
	// link: TVDB's remote-id search can return a different series entirely when a
	// bare number collides across id namespaces (tmdb 1433 -> tvdb 84070
	// "War and Remembrance"), so an unverified id is not a mapping.
	Name            string
	NumberOfSeasons int
}

func (c *Client) GetTVDetails(ctx context.Context, tvID int) (*TVDetails, error) {
	path := "/tv/" + strconv.Itoa(tvID)
	var resp struct {
		Name            string `json:"name"`
		NumberOfSeasons int    `json:"number_of_seasons"`
	}
	if err := c.doGet(ctx, path, nil, &resp); err != nil {
		return nil, err
	}
	if resp.NumberOfSeasons == 0 {
		return nil, errors.New("tmdb tv details returned empty number_of_seasons")
	}
	return &TVDetails{Name: resp.Name, NumberOfSeasons: resp.NumberOfSeasons}, nil
}

// GetTvdbIDFromTmdbID reads TMDB's own external_ids mapping for a series.
//
// This is the reliable direction measured 2026-09-25: it was correct in 5/5 cases
// tested, including American Dad (tmdb 1433 -> tvdb 73141), where TVDB's own
// bare-number remote-id search returned series 84070 "War and Remembrance".
// Note that TMDB's field is still user-contributed and can be stale or wrong, so
// callers must corroborate the name of whatever series this points at.
func (c *Client) GetTvdbIDFromTmdbID(ctx context.Context, tvID int) (int, error) {
	path := "/tv/" + strconv.Itoa(tvID) + "/external_ids"
	var resp struct {
		TVDBID int `json:"tvdb_id"`
	}
	if err := c.doGet(ctx, path, nil, &resp); err != nil {
		return 0, err
	}
	return resp.TVDBID, nil
}

type SeasonEpisode struct {
	ID            int
	EpisodeNumber int
	Name          string
	AirDate       string
}

type EpisodeGroup struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type int    `json:"type"`
}

// EpisodeGroupEntry is one episode inside an episode group ordering. Order is
// the 0-based position within the group (so position N has Order N-1); the
// season/episode numbers are the TMDB ones for the mapped episode.
type EpisodeGroupEntry struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	AirDate       string `json:"air_date"`
	SeasonNumber  int    `json:"season_number"`
	EpisodeNumber int    `json:"episode_number"`
	Order         int    `json:"order"`
}

// EpisodeGroupOrder is one season bucket inside an episode group.
type EpisodeGroupOrder struct {
	Order    int                 `json:"order"`
	Episodes []EpisodeGroupEntry `json:"episodes"`
}

type EpisodeGroupDetail struct {
	ID     string              `json:"id"`
	Name   string              `json:"name"`
	Groups []EpisodeGroupOrder `json:"groups"`
}

func (c *Client) GetEpisodeGroups(ctx context.Context, tvID int) ([]EpisodeGroup, error) {
	path := fmt.Sprintf("/tv/%d/episode_groups", tvID)
	var resp struct {
		Results []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Type int    `json:"type"`
		} `json:"results"`
	}
	if err := c.doGet(ctx, path, nil, &resp); err != nil {
		return nil, err
	}
	out := make([]EpisodeGroup, 0, len(resp.Results))
	for _, g := range resp.Results {
		out = append(out, EpisodeGroup{ID: g.ID, Name: g.Name, Type: g.Type})
	}
	return out, nil
}

func (c *Client) GetEpisodeGroup(ctx context.Context, groupID string) (*EpisodeGroupDetail, error) {
	path := "/tv/episode_group/" + url.PathEscape(groupID)
	var resp struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Groups []struct {
			Order    int `json:"order"`
			Episodes []struct {
				ID            int    `json:"id"`
				Name          string `json:"name"`
				AirDate       string `json:"air_date"`
				SeasonNumber  int    `json:"season_number"`
				EpisodeNumber int    `json:"episode_number"`
				Order         int    `json:"order"`
			} `json:"episodes"`
		} `json:"groups"`
	}
	if err := c.doGet(ctx, path, nil, &resp); err != nil {
		return nil, err
	}
	if resp.ID == "" {
		return nil, errors.New("tmdb episode group returned empty id")
	}
	detail := &EpisodeGroupDetail{ID: resp.ID, Name: resp.Name}
	for _, g := range resp.Groups {
		order := EpisodeGroupOrder{Order: g.Order}
		for _, e := range g.Episodes {
			order.Episodes = append(order.Episodes, EpisodeGroupEntry{
				ID:            e.ID,
				Name:          e.Name,
				AirDate:       e.AirDate,
				SeasonNumber:  e.SeasonNumber,
				EpisodeNumber: e.EpisodeNumber,
				Order:         e.Order,
			})
		}
		detail.Groups = append(detail.Groups, order)
	}
	return detail, nil
}

func (c *Client) GetSeasonEpisodes(ctx context.Context, tvID int, season int) ([]SeasonEpisode, error) {
	path := fmt.Sprintf("/tv/%d/season/%d", tvID, season)
	var resp struct {
		Episodes []struct {
			ID            int    `json:"id"`
			EpisodeNumber int    `json:"episode_number"`
			Name          string `json:"name"`
			AirDate       string `json:"air_date"`
		} `json:"episodes"`
	}
	if err := c.doGet(ctx, path, nil, &resp); err != nil {
		return nil, err
	}
	out := make([]SeasonEpisode, 0, len(resp.Episodes))
	for _, ep := range resp.Episodes {
		out = append(out, SeasonEpisode{
			ID:            ep.ID,
			EpisodeNumber: ep.EpisodeNumber,
			Name:          ep.Name,
			AirDate:       ep.AirDate,
		})
	}
	return out, nil
}
