package arr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// TagPrefix marks an arr as held by a Relay in Q8 focused: one arr, one
// Relay. The tag is the only thing Q8 focused adds to an arr's settings.
const TagPrefix = "q8-relay-"

// Tag is one of the arr's tags.
type Tag struct {
	ID    int    `json:"id,omitempty"`
	Label string `json:"label"`
}

// Readiness says whether Q8 focused can take this arr, and why not.
type Readiness struct {
	App     string `json:"app"`
	Ready   bool   `json:"ready"`
	Problem string `json:"problem,omitempty"`
	TagID   int    `json:"-"` // our tag, when it's there already
}

func (c *Client) name() string {
	return map[string]string{"radarr": "Radarr", "sonarr": "Sonarr", "lidarr": "Lidarr"}[c.App]
}

// Readiness checks the arr can be taken by the Relay relayID: it has a
// Recycle Bin (deleted files go there, never gone for good) and no other
// Relay holds it.
func (c *Client) Readiness(ctx context.Context, relayID string) Readiness {
	r := Readiness{App: c.App}
	var mm map[string]any
	if err := c.get(ctx, "api/"+c.version()+"/config/mediamanagement", nil, &mm); err != nil {
		r.Problem = err.Error()
		return r
	}
	if bin, _ := mm["recycleBin"].(string); strings.TrimSpace(bin) == "" {
		r.Problem = fmt.Sprintf("Set a Recycle Bin folder in %s's Settings › Media Management first: deleted files go there.", c.name())
		return r
	}
	var tags []Tag
	if err := c.get(ctx, "api/"+c.version()+"/tag", nil, &tags); err != nil {
		r.Problem = err.Error()
		return r
	}
	for _, t := range tags {
		if !strings.HasPrefix(strings.ToLower(t.Label), TagPrefix) {
			continue
		}
		if strings.EqualFold(t.Label, TagPrefix+relayID) {
			r.TagID = t.ID
			continue
		}
		r.Problem = fmt.Sprintf("Another Relay (%s) has %s in Q8 focused: undo it there first.", strings.TrimPrefix(t.Label, TagPrefix), c.name())
		return r
	}
	r.Ready = true
	return r
}

// Claim adds the tag marking the arr as relayID's, unless it's there.
func (c *Client) Claim(ctx context.Context, relayID string, existing int) (int, error) {
	if existing > 0 {
		return existing, nil
	}
	var t Tag
	err := c.send(ctx, http.MethodPost, "api/"+c.version()+"/tag", nil, Tag{Label: TagPrefix + relayID}, &t)
	return t.ID, err
}

// Release removes the tag; one already gone is fine.
func (c *Client) Release(ctx context.Context, tagID int) error {
	if tagID <= 0 {
		return nil
	}
	err := c.send(ctx, http.MethodDelete, fmt.Sprintf("api/%s/tag/%d", c.version(), tagID), nil, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// Done is what's left once Q8 has the file: the arr stops wanting it
// (unmonitored, so it isn't downloaded again), then its file is deleted,
// into the Recycle Bin. A file that's gone already, or no longer the one
// announced (an upgrade replaced it), is left alone.
func (c *Client) Done(ctx context.Context, fileID int, size int64, mediaID int) error {
	f, err := c.File(ctx, fileID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if f.Size != size {
		return fmt.Errorf("%s's file %d isn't the one announced any more: left alone", c.name(), fileID)
	}
	if err := c.unmonitor(ctx, fileID, mediaID); err != nil {
		return fmt.Errorf("couldn't unmonitor it, so nothing was deleted: %w", err)
	}
	kind := map[string]string{"radarr": "moviefile", "sonarr": "episodefile", "lidarr": "trackfile"}[c.App]
	err = c.send(ctx, http.MethodDelete, fmt.Sprintf("api/%s/%s/%d", c.version(), kind, fileID), nil, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

func (c *Client) unmonitor(ctx context.Context, fileID, mediaID int) error {
	switch c.App {
	case "radarr":
		if mediaID <= 0 {
			return errors.New("the film's id isn't known")
		}
		return c.send(ctx, http.MethodPut, "api/v3/movie/editor", nil,
			map[string]any{"movieIds": []int{mediaID}, "monitored": false}, nil)
	case "sonarr":
		var episodes []struct {
			ID int `json:"id"`
		}
		q := url.Values{"episodeFileId": {strconv.Itoa(fileID)}}
		if err := c.get(ctx, "api/v3/episode", q, &episodes); err != nil {
			return err
		}
		ids := make([]int, 0, len(episodes))
		for _, e := range episodes {
			ids = append(ids, e.ID)
		}
		if len(ids) == 0 {
			return errors.New("Sonarr lists no episode for the file")
		}
		return c.send(ctx, http.MethodPut, "api/v3/episode/monitor", nil,
			map[string]any{"episodeIds": ids, "monitored": false}, nil)
	case "lidarr":
		if mediaID <= 0 {
			return errors.New("the album's id isn't known")
		}
		return c.send(ctx, http.MethodPut, "api/v1/album/monitor", nil,
			map[string]any{"albumIds": []int{mediaID}, "monitored": false}, nil)
	}
	return fmt.Errorf("%s can't be unmonitored", c.App)
}
