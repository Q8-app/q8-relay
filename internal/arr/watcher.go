package arr

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/q8-app/q8-relay/internal/config"
	"github.com/q8-app/q8-relay/internal/hub"
	"github.com/q8-app/q8-relay/internal/store"
)

// Lookback is how far before the cursor each check asks, so an import
// written a moment late is still seen.
const Lookback = 10 * time.Minute

// Watcher asks one arr every few seconds what it imported, waits for each
// file to be whole, and announces it.
type Watcher struct {
	Client   *Client
	Hub      *hub.Hub
	PathMaps []config.PathMap
	Dir      string
	Log      *slog.Logger
	Now      func() time.Time

	state watchState
	// What each waiting file measured on the last check: a file is whole
	// once it's the arr's size twice running.
	sizes map[int]int64
	// Paths already said to be missing, so the log says it once.
	missing map[string]bool
	// For the web page, read from another goroutine.
	mu        sync.Mutex
	lastCheck time.Time
	lastError string
}

// Status is when the last check ran and what stopped it, if anything.
func (w *Watcher) Status() (time.Time, string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastCheck, w.lastError
}

type watchState struct {
	Cursor  time.Time `json:"cursor"`
	Handled []int     `json:"handled"`
}

func (w *Watcher) fileName() string { return "watch-" + w.Client.App + ".json" }

// Load reads where the watcher got to.
func (w *Watcher) Load() error {
	w.sizes = map[int]int64{}
	w.missing = map[string]bool{}
	if w.Now == nil {
		w.Now = time.Now
	}
	return store.Load(w.Dir, w.fileName(), &w.state)
}

// Run checks every interval until ctx ends.
func (w *Watcher) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := w.Check(ctx); err != nil && ctx.Err() == nil {
			w.Log.Warn("check failed", "app", w.Client.App, "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Check takes what was imported since the last check. The first check
// ever takes nothing: what the arr had before Relay came is the user's
// library, not news.
func (w *Watcher) Check(ctx context.Context) error {
	err := w.check(ctx)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lastCheck = w.Now()
	w.lastError = ""
	if err != nil {
		w.lastError = err.Error()
	}
	return err
}

func (w *Watcher) check(ctx context.Context) error {
	if w.state.Cursor.IsZero() {
		w.state.Cursor = w.Now()
		w.Log.Info("watching from now on", "app", w.Client.App)
		return store.Save(w.Dir, w.fileName(), w.state)
	}
	events, err := w.Client.ImportsSince(ctx, w.state.Cursor.Add(-Lookback))
	if err != nil {
		return err
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].Date.Before(events[j].Date) })
	handled := map[int]bool{}
	for _, id := range w.state.Handled {
		handled[id] = true
	}
	var oldestWaiting *time.Time
	for _, ev := range events {
		if handled[ev.ID] {
			continue
		}
		done, err := w.take(ctx, ev)
		if err != nil {
			return err // the arr stopped answering: the cursor stays, the next check tries again
		}
		if done {
			handled[ev.ID] = true
		} else if oldestWaiting == nil {
			d := ev.Date
			oldestWaiting = &d
		}
	}
	// The cursor waits at the oldest import still waiting, else moves to
	// the newest seen; only the handled ids that window returns are kept.
	if oldestWaiting != nil {
		w.state.Cursor = *oldestWaiting
	} else if n := len(events); n > 0 && events[n-1].Date.After(w.state.Cursor) {
		w.state.Cursor = events[n-1].Date
	}
	w.state.Handled = w.state.Handled[:0]
	for _, ev := range events {
		if handled[ev.ID] {
			w.state.Handled = append(w.state.Handled, ev.ID)
		}
	}
	return store.Save(w.Dir, w.fileName(), w.state)
}

// take announces ev's file once it's whole. done when nothing more is to
// be done with ev: announced, or the file is gone.
func (w *Watcher) take(ctx context.Context, ev Import) (done bool, err error) {
	fileID := ev.FileID()
	if fileID == 0 && w.Client.App == "lidarr" && ev.TrackID > 0 {
		// Lidarr doesn't always name the file in its history: ask the track.
		id, err := w.Client.TrackFileID(ctx, ev.TrackID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return false, err
		}
		fileID = id
	}
	if fileID == 0 {
		return true, nil
	}
	f, err := w.Client.File(ctx, fileID)
	if errors.Is(err, ErrNotFound) {
		delete(w.sizes, fileID)
		return true, nil
	}
	if err != nil {
		return false, err
	}
	path := config.Map(w.PathMaps, f.Path)
	info, statErr := os.Stat(path)
	if statErr != nil {
		if !w.missing[path] {
			w.missing[path] = true
			w.Log.Warn("can't see the arr's file: mount the same folder into Relay, or set RELAY_PATH_MAP",
				"app", w.Client.App, "arrPath", f.Path, "relayPath", path)
		}
		return false, nil
	}
	delete(w.missing, path)
	size := info.Size()
	last, seen := w.sizes[fileID]
	w.sizes[fileID] = size
	if size != f.Size || !seen || last != size {
		return false, nil
	}
	delete(w.sizes, fileID)
	a := arrival(w.Client.App, fileID, ev, path, size)
	a, isNew, err := w.Hub.Publish(a)
	if err != nil {
		w.Log.Warn("couldn't save the arrivals file", "err", err)
	}
	if isNew {
		w.Log.Info("new file", "app", a.App, "seq", a.Seq, "file", a.FileName)
	}
	return true, nil
}

func arrival(app string, fileID int, ev Import, path string, size int64) hub.Arrival {
	a := hub.Arrival{
		ID:         fmt.Sprintf("%s-%d", app, fileID),
		App:        app,
		FileName:   filepath.Base(path),
		Size:       size,
		ImportedAt: ev.Date,
		Path:       path,
	}
	switch app {
	case "radarr":
		a.Kind = "movie"
		a.MovieID = ev.MovieID
		if m := ev.Movie; m != nil {
			a.Title, a.Year, a.TMDbID = m.Title, m.Year, m.TMDbID
		}
	case "sonarr":
		a.Kind = "episode"
		if s := ev.Series; s != nil {
			a.Title, a.Year, a.TVDbID, a.TMDbID = s.Title, s.Year, s.TVDbID, s.TMDbID
		}
		if e := ev.Episode; e != nil {
			a.Season, a.Episode, a.EpisodeTVDbID, a.EpisodeTitle = e.Season, e.Number, e.TVDbID, e.Title
		}
	case "lidarr":
		a.Kind = "track"
		a.AlbumID = ev.AlbumID
		if al := ev.Album; al != nil {
			a.Title = al.Title
		}
		if ar := ev.Artist; ar != nil {
			a.Artist = ar.Name
		} else if al := ev.Album; al != nil && al.Artist != nil {
			a.Artist = al.Artist.Name
		}
	}
	return a
}
