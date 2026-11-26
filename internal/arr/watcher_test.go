package arr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/q8-app/q8-relay/internal/config"
	"github.com/q8-app/q8-relay/internal/hub"
)

// fakeRadarr answers history/since and moviefile like Radarr v3, and keeps
// every request so the test can check it only ever GETs.
type fakeRadarr struct {
	imports []map[string]any
	files   map[int]File
	methods []string
	query   string
}

func (f *fakeRadarr) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.methods = append(f.methods, r.Method)
	if r.Header.Get("X-Api-Key") != "key" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case r.URL.Path == "/api/v3/history/since":
		f.query = r.URL.RawQuery
		json.NewEncoder(w).Encode(f.imports)
	case strings.HasPrefix(r.URL.Path, "/api/v3/moviefile/"):
		var id int
		fmt.Sscan(strings.TrimPrefix(r.URL.Path, "/api/v3/moviefile/"), &id)
		file, ok := f.files[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(file)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func setUp(t *testing.T, fake *fakeRadarr) (*Watcher, *hub.Hub, *time.Time) {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	h, err := hub.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	w := &Watcher{
		Client:   &Client{App: "radarr", BaseURL: srv.URL, APIKey: "key"},
		Hub:      h,
		PathMaps: []config.PathMap{{From: "/data", To: dir}},
		Dir:      dir,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:      func() time.Time { return now },
	}
	if err := w.Load(); err != nil {
		t.Fatal(err)
	}
	return w, h, &now
}

func TestFirstCheckTakesNothingThenAWholeFileIsAnnounced(t *testing.T) {
	fake := &fakeRadarr{files: map[int]File{}}
	w, h, now := setUp(t, fake)
	ctx := context.Background()

	if err := w.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fake.methods) != 0 {
		t.Fatal("the first check asked the arr: it should only start the cursor")
	}

	movie := filepath.Join(w.Dir, "media", "Dune.mkv")
	os.MkdirAll(filepath.Dir(movie), 0o755)
	os.WriteFile(movie, []byte("half"), 0o644)
	fake.imports = []map[string]any{{
		"id": 10, "date": now.Add(time.Minute).Format(time.RFC3339), "movieId": 3,
		"data":  map[string]string{"fileId": "42"},
		"movie": map[string]any{"title": "Dune", "year": 2021, "tmdbId": 438631},
	}}
	fake.files[42] = File{Path: "/data/media/Dune.mkv", Size: 8}

	w.Check(ctx) // smaller than the arr says: still copying
	os.WriteFile(movie, []byte("complete"), 0o644)
	w.Check(ctx) // the right size once: wait for a second look
	if h.Latest() != 0 {
		t.Fatal("announced before the file was seen whole twice")
	}
	w.Check(ctx)
	got := h.Since(0)
	if len(got) != 1 {
		t.Fatalf("announced %d", len(got))
	}
	a := got[0]
	if a.ID != "radarr-42" || a.Kind != "movie" || a.Title != "Dune" || a.TMDbID != 438631 || a.Size != 8 || a.Path != movie {
		t.Fatalf("got %+v", a)
	}
	w.Check(ctx)
	if h.Latest() != 1 {
		t.Fatal("the same import was announced again")
	}
	for _, m := range fake.methods {
		if m != http.MethodGet {
			t.Fatalf("Relay sent a %s: in Connect what you have it only reads", m)
		}
	}
	if !strings.Contains(fake.query, "eventType=3") || !strings.Contains(fake.query, "includeMovie=true") {
		t.Fatalf("history asked as %q", fake.query)
	}
}

func TestAGoneFileIsPassedOverAndAnErrorKeepsTheCursor(t *testing.T) {
	fake := &fakeRadarr{files: map[int]File{}}
	w, h, now := setUp(t, fake)
	ctx := context.Background()
	w.Check(ctx)
	fake.imports = []map[string]any{{"id": 11, "date": now.Add(time.Minute).Format(time.RFC3339),
		"data": map[string]string{"fileId": "99"}}}
	if err := w.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if h.Latest() != 0 || len(w.state.Handled) != 1 {
		t.Fatalf("a deleted file wasn't passed over: %+v", w.state)
	}

	cursor := w.state.Cursor
	w.Client.APIKey = "wrong"
	if err := w.Check(ctx); err == nil || !strings.Contains(err.Error(), "refused the API key") {
		t.Fatalf("got %v", err)
	}
	if !w.state.Cursor.Equal(cursor) {
		t.Fatal("a failed check moved the cursor")
	}
	if _, problem := w.Status(); problem == "" {
		t.Fatal("the failure isn't shown")
	}
}

// fakeLidarr names a track but not its file, as Lidarr's history can.
type fakeLidarr struct {
	imports []map[string]any
}

func (f *fakeLidarr) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/v1/history/since":
		json.NewEncoder(w).Encode(f.imports)
	case "/api/v1/track/5":
		fmt.Fprint(w, `{"trackFileId":77}`)
	case "/api/v1/trackfile/77":
		fmt.Fprint(w, `{"path":"/data/music/Artist/Album/01 Song.flac","size":4}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestLidarrTrackFoundThroughItsTrackWithTheAlbumArtist(t *testing.T) {
	fake := &fakeLidarr{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	dir := t.TempDir()
	h, _ := hub.Open(dir)
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	w := &Watcher{Client: &Client{App: "lidarr", BaseURL: srv.URL, APIKey: "key"}, Hub: h,
		PathMaps: []config.PathMap{{From: "/data", To: dir}}, Dir: dir,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now }}
	w.Load()
	ctx := context.Background()
	w.Check(ctx)
	song := filepath.Join(dir, "music", "Artist", "Album", "01 Song.flac")
	os.MkdirAll(filepath.Dir(song), 0o755)
	os.WriteFile(song, []byte("flac"), 0o644)
	fake.imports = []map[string]any{{"id": 1, "date": now.Add(time.Minute).Format(time.RFC3339),
		"albumId": 9, "trackId": 5, "data": map[string]string{},
		"album": map[string]any{"title": "Album", "artist": map[string]string{"artistName": "Artist"}}}}
	w.Check(ctx)
	w.Check(ctx)
	got := h.Since(0)
	if len(got) != 1 || got[0].ID != "lidarr-77" || got[0].Kind != "track" || got[0].AlbumID != 9 ||
		got[0].Artist != "Artist" || got[0].Title != "Album" {
		t.Fatalf("got %+v", got)
	}
}

func TestHealthReadsWarningsDisksAndUpgrades(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/system/status":
			w.Write([]byte(`{"appName":"Radarr","version":"5.9"}`))
		case "/api/v3/health":
			w.Write([]byte(`[{"type":"warning","message":"Indexer down"},{"type":"error","message":"No download client"},{"type":"notice","message":"Update"}]`))
		case "/api/v3/config/mediamanagement":
			w.Write([]byte(`{"minimumFreeSpaceWhenImporting":100,"recycleBin":"/data/.bin","downloadPropersAndRepacks":"doNotUpgrade"}`))
		case "/api/v3/diskspace":
			w.Write([]byte(`[{"path":"/","label":"","freeSpace":5000000000,"totalSpace":10000000000},{"path":"/data","label":"Media","freeSpace":50000000000,"totalSpace":1000000000000}]`))
		case "/api/v3/rootfolder":
			w.Write([]byte(`[{"path":"/data/movies"}]`))
		case "/api/v3/qualityprofile":
			w.Write([]byte(`[{"name":"HD-1080p","upgradeAllowed":true},{"name":"Any","upgradeAllowed":false}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	h := (&Client{App: "radarr", BaseURL: srv.URL, APIKey: "k"}).Health(context.Background())
	if h.Version != "5.9" || h.Unreachable != "" {
		t.Fatalf("health %+v", h)
	}
	if len(h.Problems) != 3 || !h.Problems[0].Fault || h.Problems[0].Text != "No download client" {
		t.Fatalf("problems %+v", h.Problems)
	}
	if len(h.Disks) != 1 || h.Disks[0].Name != "Media" {
		t.Fatalf("disks %+v", h.Disks)
	}
	if !h.Upgrades.On() || len(h.Upgrades.Profiles) != 1 || h.Upgrades.Repacks || h.RecycleBin != "/data/.bin" {
		t.Fatalf("upgrades %+v, bin %q", h.Upgrades, h.RecycleBin)
	}
}
