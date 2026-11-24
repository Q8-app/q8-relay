// Package hub holds what Relay has announced — each file an arr imported —
// numbered in order, and hands every new one to the clients listening.
// A client that was away asks for everything after the last number it saw.
package hub

import (
	"sync"
	"time"

	"github.com/q8-app/q8-relay/internal/store"
)

// Arrival is one imported file, as Q8 is told of it.
type Arrival struct {
	Seq        int64     `json:"seq"`
	ID         string    `json:"id"`   // "radarr-123": the app and its file id
	App        string    `json:"app"`  // radarr, sonarr, lidarr
	Kind       string    `json:"kind"` // movie, episode, track
	FileName   string    `json:"fileName"`
	Size       int64     `json:"size"`
	ImportedAt time.Time `json:"importedAt"`
	FileURL    string    `json:"fileUrl"` // "/v1/files/radarr-123"

	Title         string `json:"title,omitempty"` // the film, show or album
	Year          int    `json:"year,omitempty"`
	TMDbID        int    `json:"tmdbId,omitempty"`
	TVDbID        int    `json:"tvdbId,omitempty"` // the show's
	Season        int    `json:"season,omitempty"`
	Episode       int    `json:"episode,omitempty"`
	EpisodeTVDbID int    `json:"episodeTvdbId,omitempty"`
	EpisodeTitle  string `json:"episodeTitle,omitempty"`
	Artist        string `json:"artist,omitempty"`
	AlbumID       int    `json:"albumId,omitempty"`
	MovieID       int    `json:"movieId,omitempty"`

	// Path is where Relay reads the file. Never sent: Q8 asks by ID.
	Path string `json:"path,omitempty"`
}

// Keep is how many arrivals Relay remembers for clients catching up.
const Keep = 500

const fileName = "arrivals.json"

// Hub numbers arrivals and fans them out.
type Hub struct {
	mu       sync.Mutex
	dir      string
	state    state
	watchers map[chan Arrival]struct{}
}

type state struct {
	Next     int64     `json:"next"`
	Arrivals []Arrival `json:"arrivals"`
}

// Open reads what was announced before from dir.
func Open(dir string) (*Hub, error) {
	h := &Hub{dir: dir, watchers: map[chan Arrival]struct{}{}, state: state{Next: 1}}
	if err := store.Load(dir, fileName, &h.state); err != nil {
		return h, err
	}
	if h.state.Next < 1 {
		h.state.Next = 1
	}
	return h, nil
}

// Publish numbers a and hands it to every listener. An ID already
// announced is passed over: one file, one arrival.
func (h *Hub) Publish(a Arrival) (Arrival, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, known := range h.state.Arrivals {
		if known.ID == a.ID {
			return known, false, nil
		}
	}
	a.Seq = h.state.Next
	a.FileURL = "/v1/files/" + a.ID
	h.state.Next++
	h.state.Arrivals = append(h.state.Arrivals, a)
	if over := len(h.state.Arrivals) - Keep; over > 0 {
		h.state.Arrivals = append([]Arrival(nil), h.state.Arrivals[over:]...)
	}
	err := store.Save(h.dir, fileName, h.state)
	for w := range h.watchers {
		select {
		case w <- a:
		default: // a listener that stopped reading catches up when it reconnects
		}
	}
	return a, true, err
}

// Since gives every arrival after seq, oldest first.
func (h *Hub) Since(seq int64) []Arrival {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []Arrival
	for _, a := range h.state.Arrivals {
		if a.Seq > seq {
			out = append(out, a)
		}
	}
	return out
}

// Find gives the arrival with id.
func (h *Hub) Find(id string) (Arrival, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, a := range h.state.Arrivals {
		if a.ID == id {
			return a, true
		}
	}
	return Arrival{}, false
}

// Latest is the newest number given out, 0 before the first.
func (h *Hub) Latest() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state.Next - 1
}

// Listen returns what comes after seq, and a channel for what's published
// from now on, with nothing lost in between. Call the returned function
// to stop.
func (h *Hub) Listen(seq int64) ([]Arrival, <-chan Arrival, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var missed []Arrival
	for _, a := range h.state.Arrivals {
		if a.Seq > seq {
			missed = append(missed, a)
		}
	}
	ch := make(chan Arrival, 64)
	h.watchers[ch] = struct{}{}
	return missed, ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.watchers, ch)
	}
}
