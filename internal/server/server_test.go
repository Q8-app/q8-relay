package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/q8-app/q8-relay/internal/hub"
	"github.com/q8-app/q8-relay/internal/pairing"
)

func setUp(t *testing.T) (*httptest.Server, *hub.Hub, *pairing.Pairing, string) {
	t.Helper()
	dir := t.TempDir()
	h, _ := hub.Open(dir)
	p, _ := pairing.Open(dir, nil)
	s := &Server{Name: "Test Relay", Version: "test", ShowCode: true, Hub: h, Pairing: p,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, h, p, dir
}

func pair(t *testing.T, srv *httptest.Server, p *pairing.Pairing) string {
	t.Helper()
	code, _ := p.Code()
	resp, err := http.Post(srv.URL+"/v1/pair", "application/json",
		strings.NewReader(`{"code":"`+code+`","name":"Q8 on a Mac"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct{ Key string }
	json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != http.StatusOK || body.Key == "" {
		t.Fatalf("pairing answered %d", resp.StatusCode)
	}
	return body.Key
}

func get(t *testing.T, url, key string, header ...string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestInfoIsOpenAndTheRestNeedsAKey(t *testing.T) {
	srv, _, p, _ := setUp(t)
	resp := get(t, srv.URL+"/v1/info", "")
	var info Info
	json.NewDecoder(resp.Body).Decode(&info)
	resp.Body.Close()
	if info.Protocol != Protocol || info.ID != p.RelayID() || resp.Header.Get("Q8-Relay-Protocol") != "1" {
		t.Fatalf("got %+v", info)
	}
	for _, path := range []string{"/v1/arrivals", "/v1/events", "/v1/files/radarr-1"} {
		if r := get(t, srv.URL+path, ""); r.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s without a key answered %d", path, r.StatusCode)
		}
		if r := get(t, srv.URL+path, "made-up"); r.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s with a made-up key answered %d", path, r.StatusCode)
		}
	}
	resp, _ = http.Post(srv.URL+"/v1/pair", "application/json", strings.NewReader(`{"code":"0000-0000"}`))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a wrong code answered %d", resp.StatusCode)
	}
}

func TestFilesOnlyByIDWithRange(t *testing.T) {
	srv, h, p, dir := setUp(t)
	key := pair(t, srv, p)
	path := filepath.Join(dir, "Dune.mkv")
	os.WriteFile(path, []byte("0123456789"), 0o644)
	h.Publish(hub.Arrival{ID: "radarr-42", Path: path, FileName: "Dune.mkv"})

	resp := get(t, srv.URL+"/v1/files/radarr-42", key, "Range", "bytes=4-")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || string(body) != "456789" {
		t.Fatalf("got %d %q", resp.StatusCode, body)
	}
	if r := get(t, srv.URL+"/v1/files/..%2F..%2Fetc%2Fpasswd", key); r.StatusCode != http.StatusNotFound {
		t.Fatalf("a path for an ID answered %d", r.StatusCode)
	}
	os.Remove(path)
	if r := get(t, srv.URL+"/v1/files/radarr-42", key); r.StatusCode != http.StatusGone {
		t.Fatalf("a deleted file answered %d", r.StatusCode)
	}

	resp = get(t, srv.URL+"/v1/arrivals?since=0", key)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(raw), dir) {
		t.Fatal("the arrivals list gave away Relay's paths")
	}
}

func TestEventsCatchUpThenStream(t *testing.T) {
	srv, h, p, _ := setUp(t)
	key := pair(t, srv, p)
	h.Publish(hub.Arrival{ID: "radarr-1", Path: "/secret/a.mkv"})
	h.Publish(hub.Arrival{ID: "radarr-2"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Last-Event-ID", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	lines := bufio.NewScanner(resp.Body)
	var ids []string
	go h.Publish(hub.Arrival{ID: "sonarr-3"})
	for lines.Scan() && len(ids) < 2 {
		line := lines.Text()
		if strings.Contains(line, "/secret/") {
			t.Fatal("the stream gave away a path")
		}
		if id, ok := strings.CutPrefix(line, "id: "); ok {
			ids = append(ids, id)
		}
	}
	if strings.Join(ids, ",") != "2,3" {
		t.Fatalf("got ids %v: wanted what came after 1, then the new one", ids)
	}
}

func TestPageShowsTheCode(t *testing.T) {
	srv, _, p, _ := setUp(t)
	code, _ := p.Code()
	resp := get(t, srv.URL+"/", "")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), code) {
		t.Fatal("the page doesn't show the code")
	}
}
