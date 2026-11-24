// Package arr reads Radarr, Sonarr and Lidarr: their import history and
// the files it names. GET only — in "Connect what you have" Relay never
// changes an arr's settings or its files.
package arr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrNotFound is a file the arr no longer has: deleted, or replaced by an
// upgrade (which has an import of its own).
var ErrNotFound = errors.New("not found")

// Client asks one arr.
type Client struct {
	App     string // radarr, sonarr, lidarr
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// Import is one "imported" event from the arr's history.
type Import struct {
	ID        int       `json:"id"`
	Date      time.Time `json:"date"`
	MovieID   int       `json:"movieId"`
	SeriesID  int       `json:"seriesId"`
	EpisodeID int       `json:"episodeId"`
	AlbumID   int       `json:"albumId"`
	TrackID   int       `json:"trackId"`
	Data      struct {
		FileID       string `json:"fileId"`
		ImportedPath string `json:"importedPath"`
	} `json:"data"`
	Movie *struct {
		Title  string `json:"title"`
		Year   int    `json:"year"`
		TMDbID int    `json:"tmdbId"`
	} `json:"movie"`
	Series *struct {
		Title  string `json:"title"`
		Year   int    `json:"year"`
		TVDbID int    `json:"tvdbId"`
		TMDbID int    `json:"tmdbId"`
	} `json:"series"`
	Episode *struct {
		Season  int    `json:"seasonNumber"`
		Number  int    `json:"episodeNumber"`
		TVDbID  int    `json:"tvdbId"`
		Title   string `json:"title"`
	} `json:"episode"`
	Album *struct {
		Title  string `json:"title"`
		Artist *struct {
			Name string `json:"artistName"`
		} `json:"artist"`
	} `json:"album"`
	Artist *struct {
		Name string `json:"artistName"`
	} `json:"artist"`
}

// FileID is the imported file's id, 0 when the event names none.
func (i Import) FileID() int {
	var id int
	_, _ = fmt.Sscan(i.Data.FileID, &id)
	return id
}

// File is an imported file as it is now.
type File struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

func (c *Client) version() string {
	if c.App == "lidarr" {
		return "v1"
	}
	return "v3"
}

// ImportsSince lists the files imported since date, with what they are.
func (c *Client) ImportsSince(ctx context.Context, date time.Time) ([]Import, error) {
	q := url.Values{"date": {date.UTC().Format(time.RFC3339)}, "eventType": {"3"}}
	switch c.App {
	case "radarr":
		q.Set("includeMovie", "true")
	case "sonarr":
		q.Set("includeSeries", "true")
		q.Set("includeEpisode", "true")
	case "lidarr":
		q.Set("includeAlbum", "true")
		q.Set("includeArtist", "true")
	}
	var out []Import
	err := c.get(ctx, "api/"+c.version()+"/history/since", q, &out)
	return out, err
}

// File asks for the file id as it is now.
func (c *Client) File(ctx context.Context, id int) (File, error) {
	kind := map[string]string{"radarr": "moviefile", "sonarr": "episodefile", "lidarr": "trackfile"}[c.App]
	var f File
	err := c.get(ctx, fmt.Sprintf("api/%s/%s/%d", c.version(), kind, id), nil, &f)
	return f, err
}

// TrackFileID is the file Lidarr holds for track id now, 0 when none:
// for a history event that names the track but not its file.
func (c *Client) TrackFileID(ctx context.Context, id int) (int, error) {
	var t struct {
		TrackFileID int `json:"trackFileId"`
	}
	err := c.get(ctx, fmt.Sprintf("api/v1/track/%d", id), nil, &t)
	return t.TrackFileID, err
}

// Status is the arr's name and version, which also proves the key works.
func (c *Client) Status(ctx context.Context) (name, version string, err error) {
	var s struct {
		AppName string `json:"appName"`
		Version string `json:"version"`
	}
	err = c.get(ctx, "api/"+c.version()+"/system/status", nil, &s)
	return s.AppName, s.Version, err
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	return c.send(ctx, http.MethodGet, path, q, nil, out)
}

// send makes one request. Only Q8 focused sends anything but GET.
func (c *Client) send(ctx context.Context, method, path string, q url.Values, body any, out any) error {
	u := strings.TrimRight(c.BaseURL, "/") + "/" + path
	if len(q) > 0 {
		// A "+" in a date's offset reads as a space to ASP.NET.
		u += "?" + strings.ReplaceAll(q.Encode(), "+", "%2B")
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", c.APIKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s didn't answer: %w", c.App, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("%s refused the API key", c.App)
	case resp.StatusCode >= 300:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s answered %d: %s", c.App, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
