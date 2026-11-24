// Package focused is Q8 focused: once Q8 has converted a file and checked
// the result, Relay unmonitors it in its arr and deletes the arr's copy into
// the arr's Recycle Bin, so the stack doesn't keep what Q8 already has. The
// only setting it adds to an arr is a tag marking it as this Relay's (one
// arr, one Relay); Undo removes it. Switched on and off from Q8.
package focused

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"github.com/q8-app/q8-relay/internal/arr"
	"github.com/q8-app/q8-relay/internal/hub"
	"github.com/q8-app/q8-relay/internal/store"
)

const fileName = "focused.json"

// ErrNotReady is turning it on while an arr can't be taken.
var ErrNotReady = errors.New("an app isn't ready for Q8 focused")

// ErrOff is a confirmation while Q8 focused is off: nothing is deleted.
var ErrOff = errors.New("Q8 focused is off: nothing is deleted")

// Report is what Q8 shows before and after switching.
type Report struct {
	Enabled bool            `json:"enabled"`
	Apps    []arr.Readiness `json:"apps"`
}

// Outcome of one confirmation.
type Outcome struct {
	ID    string `json:"id"`
	Error string `json:"error,omitempty"`
}

type state struct {
	Enabled bool           `json:"enabled"`
	Tags    map[string]int `json:"tags"`
	Done    []string       `json:"done"`
}

// Focused holds Q8 focused's state.
type Focused struct {
	mu      sync.Mutex
	dir     string
	relayID string
	clients map[string]*arr.Client
	hub     *hub.Hub
	log     *slog.Logger
	state   state
}

// Open reads the state from dir.
func Open(dir, relayID string, clients []*arr.Client, h *hub.Hub, log *slog.Logger) (*Focused, error) {
	f := &Focused{dir: dir, relayID: relayID, clients: map[string]*arr.Client{}, hub: h, log: log,
		state: state{Tags: map[string]int{}}}
	for _, c := range clients {
		f.clients[c.App] = c
	}
	err := store.Load(dir, fileName, &f.state)
	if f.state.Tags == nil {
		f.state.Tags = map[string]int{}
	}
	return f, err
}

// Enabled says whether it's on.
func (f *Focused) Enabled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state.Enabled
}

// Report checks every arr now.
func (f *Focused) Report(ctx context.Context) Report {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.report(ctx)
}

func (f *Focused) report(ctx context.Context) Report {
	r := Report{Enabled: f.state.Enabled, Apps: []arr.Readiness{}}
	for _, name := range []string{"radarr", "sonarr", "lidarr"} {
		if c, ok := f.clients[name]; ok {
			r.Apps = append(r.Apps, c.Readiness(ctx, f.relayID))
		}
	}
	return r
}

// Enable takes every arr, or none: one that isn't ready stops it, and any
// tag already added is taken back.
func (f *Focused) Enable(ctx context.Context) (Report, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.report(ctx)
	if len(r.Apps) == 0 {
		return r, ErrNotReady
	}
	for _, a := range r.Apps {
		if !a.Ready {
			return r, ErrNotReady
		}
	}
	added := map[string]int{}
	for _, a := range r.Apps {
		id, err := f.clients[a.App].Claim(ctx, f.relayID, a.TagID)
		if err != nil {
			for app, tag := range added {
				_ = f.clients[app].Release(ctx, tag)
			}
			return r, fmt.Errorf("couldn't mark %s as this Relay's: %w", a.App, err)
		}
		if a.TagID == 0 {
			added[a.App] = id
		}
		f.state.Tags[a.App] = id
	}
	f.state.Enabled = true
	if err := store.Save(f.dir, fileName, f.state); err != nil {
		return r, err
	}
	f.log.Info("Q8 focused is on")
	r.Enabled = true
	return r, nil
}

// Disable undoes it: the tags go, and nothing more is deleted.
func (f *Focused) Disable(ctx context.Context) (Report, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var problems []string
	for app, tag := range f.state.Tags {
		c, ok := f.clients[app]
		if !ok {
			delete(f.state.Tags, app)
			continue
		}
		if err := c.Release(ctx, tag); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", app, err))
			continue
		}
		delete(f.state.Tags, app)
	}
	f.state.Enabled = false
	err := store.Save(f.dir, fileName, f.state)
	f.log.Info("Q8 focused is off")
	r := f.report(ctx)
	if len(problems) > 0 {
		return r, fmt.Errorf("Q8 focused is off, but a tag stayed: %s", strings.Join(problems, "; "))
	}
	return r, err
}

// Confirm is Q8 saying it has these arrivals, converted and checked: each
// is unmonitored and its arr's file deleted. Asking twice is fine.
func (f *Focused) Confirm(ctx context.Context, ids []string) ([]Outcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.state.Enabled {
		return nil, ErrOff
	}
	out := make([]Outcome, 0, len(ids))
	for _, id := range ids {
		o := Outcome{ID: id}
		if err := f.confirm(ctx, id); err != nil {
			o.Error = err.Error()
			f.log.Warn("not deleted", "id", id, "err", err)
		}
		out = append(out, o)
	}
	return out, store.Save(f.dir, fileName, f.state)
}

func (f *Focused) confirm(ctx context.Context, id string) error {
	for _, done := range f.state.Done {
		if done == id {
			return nil
		}
	}
	a, ok := f.hub.Find(id)
	if !ok {
		return errors.New("Relay doesn't know that file any more")
	}
	c, ok := f.clients[a.App]
	if !ok {
		return fmt.Errorf("Relay no longer watches %s", a.App)
	}
	fileID, err := strconv.Atoi(strings.TrimPrefix(a.ID, a.App+"-"))
	if err != nil {
		return err
	}
	media := a.MovieID
	if a.App == "lidarr" {
		media = a.AlbumID
	}
	if err := c.Done(ctx, fileID, a.Size, media); err != nil {
		return err
	}
	f.log.Info("deleted the arr's copy: Q8 has it", "app", a.App, "file", a.FileName)
	f.state.Done = append(f.state.Done, id)
	if over := len(f.state.Done) - hub.Keep; over > 0 {
		f.state.Done = append([]string(nil), f.state.Done[over:]...)
	}
	return nil
}
