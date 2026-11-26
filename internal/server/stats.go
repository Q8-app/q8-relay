package server

import (
	"sort"
	"sync"
	"time"

	"github.com/q8-app/q8-relay/internal/store"
)

// Stats is what the page counts: data served, which Mac fetched each
// arrival and how long after its import, the downloads running now and the
// Macs listening now. What it keeps survives a restart; what's running now
// doesn't need to.
type Stats struct {
	mu    sync.Mutex
	dir   string
	saved savedStats
	// Running now, by a key of client and arrival.
	downloads map[string]*Download
	// Event streams open now, by client ID.
	listening map[string]int
	now       func() time.Time
}

type savedStats struct {
	Served  int64            `json:"served"`
	Fetched map[string]Fetch `json:"fetched"`
}

// Fetch is a Mac having an arrival's file whole.
type Fetch struct {
	Client      string        `json:"client"`
	At          time.Time     `json:"at"`
	AfterImport time.Duration `json:"afterImport"`
}

// Download is a file being served now.
type Download struct {
	Client   string
	FileName string
	Sent     int64
	Size     int64
}

const statsFile = "stats.json"

// keepFetched is how many fetched arrivals the stats remember: as many as
// the hub keeps.
const keepFetched = 500

// OpenStats reads what was counted before from dir.
func OpenStats(dir string) (*Stats, error) {
	s := &Stats{dir: dir, downloads: map[string]*Download{}, listening: map[string]int{}, now: time.Now}
	err := store.Load(dir, statsFile, &s.saved)
	if s.saved.Fetched == nil {
		s.saved.Fetched = map[string]Fetch{}
	}
	return s, err
}

// Listening marks one more event stream open for client; call the returned
// function when it closes.
func (s *Stats) Listening(clientID string) func() {
	s.mu.Lock()
	s.listening[clientID]++
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.listening[clientID]--; s.listening[clientID] <= 0 {
			delete(s.listening, clientID)
		}
	}
}

// IsListening is client connected now.
func (s *Stats) IsListening(clientID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listening[clientID] > 0
}

// Started marks a download of an arrival begun; the returned functions add
// what's been sent and end it. A download that ends with the whole file
// counts as the arrival fetched.
func (s *Stats) Started(client, arrivalID, fileName string, size int64, imported time.Time) (sent func(int64), done func(whole bool)) {
	key := client + "\x00" + arrivalID
	s.mu.Lock()
	d := &Download{Client: client, FileName: fileName, Size: size}
	s.downloads[key] = d
	s.mu.Unlock()
	sent = func(n int64) {
		s.mu.Lock()
		d.Sent += n
		s.saved.Served += n
		s.mu.Unlock()
	}
	done = func(whole bool) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.downloads[key] == d {
			delete(s.downloads, key)
		}
		if whole {
			if _, known := s.saved.Fetched[arrivalID]; !known {
				now := s.now()
				s.saved.Fetched[arrivalID] = Fetch{Client: client, At: now, AfterImport: now.Sub(imported)}
				s.trim()
			}
		}
		_ = store.Save(s.dir, statsFile, s.saved)
	}
	return sent, done
}

func (s *Stats) trim() {
	over := len(s.saved.Fetched) - keepFetched
	if over <= 0 {
		return
	}
	type aged struct {
		id string
		at time.Time
	}
	all := make([]aged, 0, len(s.saved.Fetched))
	for id, f := range s.saved.Fetched {
		all = append(all, aged{id, f.At})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].at.Before(all[j].at) })
	for _, a := range all[:over] {
		delete(s.saved.Fetched, a.id)
	}
}

// Fetched is who fetched the arrival, and when.
func (s *Stats) Fetched(arrivalID string) (Fetch, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.saved.Fetched[arrivalID]
	return f, ok
}

// Served is every byte Relay has sent.
func (s *Stats) Served() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saved.Served
}

// Downloads are the files being served now.
func (s *Stats) Downloads() []Download {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Download, 0, len(s.downloads))
	for _, d := range s.downloads {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FileName < out[j].FileName })
	return out
}

// TypicalWait is the median time from an arr's import to a Mac having the
// file, over what's remembered; 0 before the first.
func (s *Stats) TypicalWait() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	waits := make([]time.Duration, 0, len(s.saved.Fetched))
	for _, f := range s.saved.Fetched {
		if f.AfterImport > 0 {
			waits = append(waits, f.AfterImport)
		}
	}
	if len(waits) == 0 {
		return 0
	}
	sort.Slice(waits, func(i, j int) bool { return waits[i] < waits[j] })
	return waits[len(waits)/2]
}
