package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/q8-app/q8-relay/internal/arr"
	"github.com/q8-app/q8-relay/internal/hub"
	"github.com/q8-app/q8-relay/internal/pairing"
)

func TestPageShowsAppsMacsNumbersAndRecent(t *testing.T) {
	dir := t.TempDir()
	h, _ := hub.Open(dir)
	p, _ := pairing.Open(dir, nil)
	stats, _ := OpenStats(dir)
	board := arr.NewBoard()
	board.Set("radarr", arr.Health{Name: "Radarr", Version: "5.9",
		Problems: []arr.Problem{{Text: "Indexers unavailable: one"}},
		Disks:    []arr.Disk{{Name: "/data", Free: 50_000_000_000, Total: 1_000_000_000_000}},
		Upgrades: arr.Upgrades{Profiles: []string{"HD-1080p"}, Repacks: true, Known: true}})
	watcher := &arr.Watcher{Client: &arr.Client{App: "radarr", BaseURL: "http://nas.local:7878"}, Hub: h}
	s := &Server{Name: "Test Relay", Version: "test", ShowCode: true, Hub: h, Pairing: p,
		Watchers: []*arr.Watcher{watcher}, Health: board, Stats: stats, Started: time.Now().Add(-3 * time.Hour),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	h.Publish(hub.Arrival{ID: "radarr-1", App: "radarr", Kind: "movie", FileName: "Dune.mkv", Size: 8_000_000_000,
		ImportedAt: time.Now().Add(-time.Hour), Title: "Dune", Year: 2021})

	resp := get(t, srv.URL+"/", "")
	body, _ := io.ReadAll(resp.Body)
	page := string(body)
	for _, want := range []string{
		"Connect what you have", "up 3 h", "Radarr", "5.9", "Indexers unavailable: one", "95% full",
		"Upgrades are on: a better release would be downloaded and sent to Q8 as a second copy.",
		"untick Upgrades Allowed in “HD-1080p”", "Do Not Upgrade Automatically",
		"http://nas.local:7878/settings/profiles", "No Mac paired yet.", "Pair a Mac",
		"Announced today", "Dune (2021)", "not fetched yet", `http-equiv="refresh"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the page doesn't say %q", want)
		}
	}
}

func TestAWholeDownloadCountsAsFetched(t *testing.T) {
	srv, h, p, dir := setUp(t)
	key := pair(t, srv, p)
	file := filepath.Join(dir, "Dune.mkv")
	os.WriteFile(file, []byte("0123456789"), 0o600)
	a, _, _ := h.Publish(hub.Arrival{ID: "radarr-7", App: "radarr", FileName: "Dune.mkv", Size: 10, Path: file,
		ImportedAt: time.Now().Add(-time.Minute)})
	_ = a

	// setUp's server has no stats: give it some.
	stats, _ := OpenStats(dir)
	s := &Server{Hub: h, Pairing: p, Stats: stats, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv2 := httptest.NewServer(s.Handler())
	defer srv2.Close()

	part := get(t, srv2.URL+"/v1/files/radarr-7", key, "Range", "bytes=0-3")
	io.ReadAll(part.Body)
	part.Body.Close()
	if _, ok := stats.Fetched("radarr-7"); ok {
		t.Fatal("part of the file counted as fetched")
	}
	rest := get(t, srv2.URL+"/v1/files/radarr-7", key, "Range", "bytes=4-")
	io.ReadAll(rest.Body)
	rest.Body.Close()
	f, ok := stats.Fetched("radarr-7")
	if !ok || f.Client != "Q8 on a Mac" {
		t.Fatalf("the resumed download didn't count: %+v %v", f, ok)
	}
	if stats.Served() != 10 || stats.TypicalWait() < time.Minute {
		t.Fatalf("served %d, wait %v", stats.Served(), stats.TypicalWait())
	}
	reopened, _ := OpenStats(dir)
	if _, ok := reopened.Fetched("radarr-7"); !ok {
		t.Fatal("what was fetched didn't survive a restart")
	}
}

func TestHelpers(t *testing.T) {
	if rangeStart("bytes=4-") != 4 || rangeStart("") != 0 {
		t.Error("rangeStart")
	}
	if settingsURL("http://radarr:7878", arr.Upgrades{Profiles: []string{"HD"}}) != "" {
		t.Error("a container's name can't open in a browser")
	}
	if got := settingsURL("http://10.0.0.5:7878/", arr.Upgrades{Repacks: true}); got != "http://10.0.0.5:7878/settings/mediamanagement" {
		t.Errorf("settingsURL = %q", got)
	}
	if ago(90*time.Second) != "1 min" || ago(50*time.Hour) != "2 d" || size(8_123_000_000) != "8.1 GB" {
		t.Error("ago or size")
	}
	if arrivalTitle(hub.Arrival{Kind: "episode", Title: "Severance", Season: 2, Episode: 3}) != "Severance S02E03" {
		t.Error("arrivalTitle")
	}
	_ = http.StatusOK
}
