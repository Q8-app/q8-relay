// Package server is Relay's HTTP side: what it is, pairing, the event
// stream Q8 listens on, and the files themselves.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
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
		if _, ok := s.Pairing.Check(strings.TrimSpace(key)); !ok {
			writeError(w, http.StatusUnauthorized, "this key isn't paired with this Relay any more: pair again")
			return
		}
		next(w, r)
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
	http.ServeContent(w, r, filepath.Base(a.Path), info.ModTime(), f)
}

var pageTemplate = template.Must(template.New("page").Parse(`<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<title>{{.Name}}</title>
<style>
body{font:15px/1.5 -apple-system,system-ui,sans-serif;max-width:34em;margin:3em auto;padding:0 1em;color:#222;background:#fafafa}
h1{font-size:1.4em;margin:0 0 .2em}.dim{color:#777}.code{font:600 2.2em ui-monospace,monospace;letter-spacing:.06em;margin:.2em 0}
table{border-collapse:collapse;width:100%}td{padding:.3em 0;border-bottom:1px solid #eee}.bad{color:#b4462c}
@media (prefers-color-scheme:dark){body{background:#1c1c1e;color:#eee}td{border-color:#333}}
</style></head><body>
<h1>{{.Name}}</h1>
<p class="dim">Version {{.Version}} · protocol {{.Protocol}} · ID {{.ID}}</p>
{{if .ShowCode}}<p>To connect Q8, type this code in Connect to Relay…:</p>
<p class="code">{{.Code}}</p><p class="dim">It changes once used, and every 15 minutes.</p>
{{else}}<p>The pairing code is in Relay's log.</p>{{end}}
<h2>Apps</h2><table>{{range .Apps}}<tr><td>{{.Name}}</td><td>{{if .Error}}<span class="bad">{{.Error}}</span>{{else if .Checked}}checked {{.Checked}}{{else}}not checked yet{{end}}</td></tr>
{{else}}<tr><td class="bad">No app set: give Relay RADARR_URL and RADARR_API_KEY (or Sonarr's, Lidarr's).</td></tr>{{end}}</table>
<h2>Paired</h2><table>{{range .Clients}}<tr><td>{{.Name}}</td><td class="dim">since {{.PairedAt.Format "2 Jan 2006"}}</td></tr>
{{else}}<tr><td class="dim">No Q8 yet.</td></tr>{{end}}</table>
<p class="dim">{{.Latest}} files announced.</p>
</body></html>`))

func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	type app struct{ Name, Checked, Error string }
	data := struct {
		Name, Version, ID, Code string
		Protocol                int
		ShowCode                bool
		Apps                    []app
		Clients                 []pairing.Client
		Latest                  int64
	}{Name: s.Name, Version: s.Version, ID: s.Pairing.RelayID(), Protocol: Protocol, ShowCode: s.ShowCode,
		Clients: s.Pairing.Clients(), Latest: s.Hub.Latest()}
	if s.ShowCode {
		data.Code, _ = s.Pairing.Code()
	}
	for _, wt := range s.Watchers {
		checked, problem := wt.Status()
		a := app{Name: wt.Client.App, Error: problem}
		if !checked.IsZero() {
			a.Checked = time.Since(checked).Round(time.Second).String() + " ago"
		}
		data.Apps = append(data.Apps, a)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = pageTemplate.Execute(w, data)
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
