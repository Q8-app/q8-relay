// Package server is Relay's HTTP side: what it is, pairing, the event
// stream Q8 listens on, and the files themselves.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/q8-app/q8-relay/internal/arr"
	"github.com/q8-app/q8-relay/internal/focused"
	"github.com/q8-app/q8-relay/internal/hub"
	"github.com/q8-app/q8-relay/internal/pairing"
)

// Protocol is the version of what Q8 and Relay say to each other. A Q8
// that needs a newer one says so to the user, and so does Relay's page.
const Protocol = 1

// Heartbeat is how often the event stream sends a comment, so proxies and
// Q8 both know it's alive.
var Heartbeat = 20 * time.Second

// Server holds what the handlers need.
type Server struct {
	Name     string
	Version  string
	ShowCode bool
	Hub      *hub.Hub
	Pairing  *pairing.Pairing
	Watchers []*arr.Watcher
	Focused  *focused.Focused
	Log      *slog.Logger
	// For the page: each arr's health, what's been served, when Relay
	// started, and why Bonjour isn't announcing it ("" while it is).
	Health  *arr.Board
	Stats   *Stats
	Started time.Time
	Bonjour string
}

type clientKey struct{}

// client is the paired Q8 a request came from.
func client(r *http.Request) pairing.Client {
	c, _ := r.Context().Value(clientKey{}).(pairing.Client)
	return c
}

// Handler routes Relay's requests.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.page)
	mux.HandleFunc("GET /v1/info", s.info)
	mux.HandleFunc("POST /v1/pair", s.pair)
	mux.Handle("GET /v1/events", s.paired(s.events))
	mux.Handle("GET /v1/arrivals", s.paired(s.arrivals))
	mux.Handle("GET /v1/files/{id}", s.paired(s.file))
	mux.Handle("GET /v1/focused", s.paired(s.focusedReport))
	mux.Handle("POST /v1/focused", s.paired(s.focusedSwitch))
	mux.Handle("POST /v1/confirm", s.paired(s.confirm))
	return protocolHeader(mux)
}

func protocolHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Q8-Relay-Protocol", strconv.Itoa(Protocol))
		next.ServeHTTP(w, r)
	})
}

// paired lets through only a request carrying a paired Q8's key.
func (s *Server) paired(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			writeError(w, http.StatusUnauthorized, "pair with this Relay first")
			return
		}
		c, ok := s.Pairing.Check(strings.TrimSpace(key))
		if !ok {
			writeError(w, http.StatusUnauthorized, "this key isn't paired with this Relay any more: pair again")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), clientKey{}, c)))
	})
}

// Info is what Relay says about itself before pairing.
type Info struct {
	Name     string   `json:"name"`
	ID       string   `json:"id"`
	Version  string   `json:"version"`
	Protocol int      `json:"protocol"`
	Mode     string   `json:"mode"`
	Apps     []string `json:"apps"`
}

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	info := Info{Name: s.Name, ID: s.Pairing.RelayID(), Version: s.Version, Protocol: Protocol,
		Mode: "connect", Apps: []string{}}
	if s.Focused != nil && s.Focused.Enabled() {
		info.Mode = "focused"
	}
	for _, w := range s.Watchers {
		info.Apps = append(info.Apps, w.Client.App)
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) pair(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "send {\"code\": \"1234-5678\", \"name\": \"…\"}")
		return
	}
	client, key, err := s.Pairing.Pair(body.Code, strings.TrimSpace(body.Name))
	switch {
	case errors.Is(err, pairing.ErrTooMany):
		writeError(w, http.StatusTooManyRequests, err.Error())
		return
	case errors.Is(err, pairing.ErrWrongCode):
		writeError(w, http.StatusForbidden, err.Error())
		return
	case err != nil:
		s.Log.Error("pairing couldn't be saved", "err", err)
		writeError(w, http.StatusInternalServerError, "Relay couldn't save the pairing: is its /config folder writable?")
		return
	}
	s.Log.Info("paired", "client", client.Name, "id", client.ID)
	writeJSON(w, http.StatusOK, map[string]string{"key": key, "clientId": client.ID, "relayId": s.Pairing.RelayID()})
}

// after reads where a client got to: Last-Event-ID when the stream
// reconnects by itself, else ?since=.
func after(r *http.Request) int64 {
	v := r.Header.Get("Last-Event-ID")
	if v == "" {
		v = r.URL.Query().Get("since")
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming isn't possible here")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	missed, ch, stop := s.Hub.Listen(after(r))
	defer stop()
	if s.Stats != nil {
		defer s.Stats.Listening(client(r).ID)()
	}
	fmt.Fprintf(w, "retry: 5000\n\n")
	for _, a := range missed {
		writeEvent(w, a)
	}
	flusher.Flush()
	beat := time.NewTicker(Heartbeat)
	defer beat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case a := <-ch:
			writeEvent(w, a)
			flusher.Flush()
		case <-beat.C:
			fmt.Fprintf(w, ": alive\n\n")
			flusher.Flush()
		}
	}
}

func writeEvent(w http.ResponseWriter, a hub.Arrival) {
	a.Path = ""
	data, _ := json.Marshal(a)
	fmt.Fprintf(w, "id: %d\nevent: arrival\ndata: %s\n\n", a.Seq, data)
}

func (s *Server) arrivals(w http.ResponseWriter, r *http.Request) {
	list := s.Hub.Since(after(r))
	for i := range list {
		list[i].Path = ""
	}
	if list == nil {
		list = []hub.Arrival{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"latest": s.Hub.Latest(), "arrivals": list})
}

// file serves an announced file — only those: Q8 asks by arrival ID,
// never by path. Range requests resume a download cut short.
func (s *Server) file(w http.ResponseWriter, r *http.Request) {
	a, ok := s.Hub.Find(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "Relay hasn't announced that file, or has forgotten it")
		return
	}
	f, err := os.Open(a.Path)
	if err != nil {
		writeError(w, http.StatusGone, "the file isn't there any more: the arr may have replaced or deleted it")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		writeError(w, http.StatusGone, "the file isn't there any more")
		return
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(a.Path)}))
	if s.Stats == nil {
		http.ServeContent(w, r, filepath.Base(a.Path), info.ModTime(), f)
		return
	}
	// Counted for the page: what's sent, and whether this request finished
	// the file — a resumed download finishes it from where it started.
	sent, done := s.Stats.Started(client(r).Name, a.ID, a.FileName, info.Size(), a.ImportedAt)
	cw := &countingWriter{ResponseWriter: w, sent: sent}
	http.ServeContent(cw, r, filepath.Base(a.Path), info.ModTime(), f)
	from := rangeStart(r.Header.Get("Range"))
	done(r.Method == http.MethodGet && cw.status < 300 && from+cw.total >= info.Size())
}

func (s *Server) focusedReport(w http.ResponseWriter, r *http.Request) {
	if s.Focused == nil {
		writeError(w, http.StatusNotFound, "Q8 focused isn't available on this Relay")
		return
	}
	writeJSON(w, http.StatusOK, s.Focused.Report(r.Context()))
}

func (s *Server) focusedSwitch(w http.ResponseWriter, r *http.Request) {
	if s.Focused == nil {
		writeError(w, http.StatusNotFound, "Q8 focused isn't available on this Relay")
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body); err != nil || body.Enabled == nil {
		writeError(w, http.StatusBadRequest, "send {\"enabled\": true} or false")
		return
	}
	var report focused.Report
	var err error
	if *body.Enabled {
		report, err = s.Focused.Enable(r.Context())
	} else {
		report, err = s.Focused.Disable(r.Context())
	}
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, focused.ErrNotReady) {
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]any{"error": err.Error(), "enabled": report.Enabled, "apps": report.Apps})
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) confirm(w http.ResponseWriter, r *http.Request) {
	if s.Focused == nil {
		writeError(w, http.StatusNotFound, "Q8 focused isn't available on this Relay")
		return
	}
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&body); err != nil || len(body.IDs) == 0 {
		writeError(w, http.StatusBadRequest, "send {\"ids\": [\"radarr-42\"]}")
		return
	}
	outcomes, err := s.Focused.Confirm(r.Context(), body.IDs)
	if errors.Is(err, focused.ErrOff) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		s.Log.Warn("couldn't save Q8 focused's state", "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"outcomes": outcomes})
}

// countingWriter counts the body bytes a response sends.
type countingWriter struct {
	http.ResponseWriter
	sent   func(int64)
	total  int64
	status int
}

func (c *countingWriter) WriteHeader(status int) {
	c.status = status
	c.ResponseWriter.WriteHeader(status)
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.ResponseWriter.Write(p)
	c.total += int64(n)
	c.sent(int64(n))
	return n, err
}

// rangeStart is where "bytes=N-" asks a download to start, 0 without one.
func rangeStart(header string) int64 {
	spec, ok := strings.CutPrefix(header, "bytes=")
	if !ok {
		return 0
	}
	from, _, _ := strings.Cut(spec, "-")
	n, _ := strconv.ParseInt(strings.TrimSpace(from), 10, 64)
	return n
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
