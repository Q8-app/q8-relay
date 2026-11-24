package focused

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/q8-app/q8-relay/internal/arr"
	"github.com/q8-app/q8-relay/internal/hub"
)

// fakeRadarr keeps a Recycle Bin setting, tags, one film file, and every
// write it's sent.
type fakeRadarr struct {
	mu         sync.Mutex
	recycleBin string
	tags       []arr.Tag
	fileSize   int64
	fileGone   bool
	writes     []string
}

func (f *fakeRadarr) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method != http.MethodGet {
		body, _ := io.ReadAll(r.Body)
		f.writes = append(f.writes, r.Method+" "+r.URL.Path+" "+strings.TrimSpace(string(body)))
	}
	switch {
	case r.URL.Path == "/api/v3/config/mediamanagement":
		json.NewEncoder(w).Encode(map[string]any{"id": 1, "recycleBin": f.recycleBin})
	case r.URL.Path == "/api/v3/tag" && r.Method == http.MethodGet:
		json.NewEncoder(w).Encode(f.tags)
	case r.URL.Path == "/api/v3/tag" && r.Method == http.MethodPost:
		t := arr.Tag{ID: len(f.tags) + 10, Label: "q8-relay-me"}
		f.tags = append(f.tags, t)
		json.NewEncoder(w).Encode(t)
	case strings.HasPrefix(r.URL.Path, "/api/v3/tag/") && r.Method == http.MethodDelete:
		f.tags = nil
	case r.URL.Path == "/api/v3/moviefile/42" && r.Method == http.MethodGet:
		if f.fileGone {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprintf(w, `{"path":"/data/Dune.mkv","size":%d}`, f.fileSize)
	case r.URL.Path == "/api/v3/moviefile/42" && r.Method == http.MethodDelete:
		f.fileGone = true
	case r.URL.Path == "/api/v3/movie/editor":
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func setUp(t *testing.T, fake *fakeRadarr) *Focused {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	h, _ := hub.Open(dir)
	h.Publish(hub.Arrival{ID: "radarr-42", App: "radarr", Kind: "movie", Size: 10, MovieID: 3})
	f, err := Open(dir, "me", []*arr.Client{{App: "radarr", BaseURL: srv.URL, APIKey: "k"}}, h,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestRefusedWithoutARecycleBinOrWhileAnotherRelayHoldsIt(t *testing.T) {
	ctx := context.Background()
	fake := &fakeRadarr{fileSize: 10}
	f := setUp(t, fake)
	r, err := f.Enable(ctx)
	if !errors.Is(err, ErrNotReady) || r.Apps[0].Ready || !strings.Contains(r.Apps[0].Problem, "Recycle Bin") {
		t.Fatalf("got %+v, %v", r, err)
	}
	fake.recycleBin = "/data/.recycle"
	fake.tags = []arr.Tag{{ID: 1, Label: "q8-relay-garage"}}
	r, err = f.Enable(ctx)
	if !errors.Is(err, ErrNotReady) || !strings.Contains(r.Apps[0].Problem, "garage") {
		t.Fatalf("got %+v, %v", r, err)
	}
	if len(fake.writes) != 0 {
		t.Fatalf("a refused switch wrote to the arr: %v", fake.writes)
	}
}

func TestOnConfirmOffUndo(t *testing.T) {
	ctx := context.Background()
	fake := &fakeRadarr{recycleBin: "/data/.recycle", fileSize: 10}
	f := setUp(t, fake)
	if _, err := f.Confirm(ctx, []string{"radarr-42"}); !errors.Is(err, ErrOff) {
		t.Fatalf("confirmed while off: %v", err)
	}
	if _, err := f.Enable(ctx); err != nil {
		t.Fatal(err)
	}
	out, err := f.Confirm(ctx, []string{"radarr-42"})
	if err != nil || out[0].Error != "" {
		t.Fatalf("got %+v, %v", out, err)
	}
	want := []string{
		`POST /api/v3/tag {"label":"q8-relay-me"}`,
		`PUT /api/v3/movie/editor {"monitored":false,"movieIds":[3]}`,
		`DELETE /api/v3/moviefile/42 `,
	}
	if strings.Join(fake.writes, "\n") != strings.Join(want, "\n") {
		t.Fatalf("writes:\n%s\nwant:\n%s", strings.Join(fake.writes, "\n"), strings.Join(want, "\n"))
	}
	// Asking again changes nothing.
	f.Confirm(ctx, []string{"radarr-42"})
	if len(fake.writes) != 3 {
		t.Fatalf("a second confirmation wrote again: %v", fake.writes)
	}
	if _, err := f.Disable(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(fake.writes[len(fake.writes)-1], "DELETE /api/v3/tag/10") || f.Enabled() {
		t.Fatalf("undo didn't take the tag away: %v", fake.writes)
	}
}

func TestAReplacedFileIsLeftAlone(t *testing.T) {
	ctx := context.Background()
	fake := &fakeRadarr{recycleBin: "/data/.recycle", fileSize: 99}
	f := setUp(t, fake)
	f.Enable(ctx)
	out, _ := f.Confirm(ctx, []string{"radarr-42"})
	if out[0].Error == "" {
		t.Fatal("a file of another size was deleted")
	}
	for _, w := range fake.writes {
		if strings.HasPrefix(w, "DELETE /api/v3/moviefile") || strings.HasPrefix(w, "PUT") {
			t.Fatalf("it wrote %q", w)
		}
	}
}
