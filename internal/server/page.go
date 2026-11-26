package server

import (
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/q8-app/q8-relay/internal/arr"
	"github.com/q8-app/q8-relay/internal/hub"
)

// The page: what Relay is doing, in Q8's own look — its dark background,
// its tangerine, cards like Q8's rows. One plain page, no scripts, that
// refreshes itself every 10 seconds. It shows and changes nothing on the
// stack: whatever it recommends is done in the app it names.

type pageApp struct {
	Name, Light, Version, Checked, LastImport string
	Problems                                  []arr.Problem
	Disks                                     []pageDisk
	Upgrades                                  string
	UpgradesHow                               []string
	SettingsURL                               string
	RecycleBin                                string
}

type pageDisk struct {
	Name, Free string
	Percent    int
	Full       bool
}

type pageClient struct {
	Name, State string
	Connected   bool
	Downloads   []pageDownload
}

type pageDownload struct {
	FileName string
	Percent  int
}

type pageArrival struct {
	Title, App, Size, When, Fetched string
	Waiting                         bool
}

type pageStat struct{ Label, Value string }

type pageData struct {
	Name, Version, ID, Code, Uptime, Mode string
	Focused, ShowCode                     bool
	Protocol                              int
	Recommendations                       []pageRecommendation
	Apps                                  []pageApp
	Clients                               []pageClient
	Stats                                 []pageStat
	Recent                                []pageArrival
}

type pageRecommendation struct {
	Text string
	Fix  []string
	Link string
}

func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	d := pageData{Name: s.Name, Version: shortVersion(s.Version), ID: s.Pairing.RelayID(), Protocol: Protocol,
		ShowCode: s.ShowCode, Mode: "Connect what you have"}
	if !s.Started.IsZero() {
		d.Uptime = "up " + ago(now.Sub(s.Started))
	}
	if s.Focused != nil && s.Focused.Enabled() {
		d.Focused, d.Mode = true, "Q8 focused"
	}
	if s.ShowCode {
		d.Code, _ = s.Pairing.Code()
	}
	all := s.Hub.Since(0)

	if len(s.Watchers) == 0 {
		d.Recommendations = append(d.Recommendations, pageRecommendation{
			Text: "No app set yet.",
			Fix:  []string{"Give Relay RADARR_URL and RADARR_API_KEY (or Sonarr's, Lidarr's) in its compose file."}})
	}
	for _, wt := range s.Watchers {
		d.Apps = append(d.Apps, s.pageApp(wt, all, now, &d))
	}

	clients := s.Pairing.Clients()
	if len(clients) == 0 {
		d.Recommendations = append(d.Recommendations, pageRecommendation{
			Text: "No Mac paired yet.", Fix: []string{"In Q8: Media Server › + › Connect to Relay…, then type the code below."}})
	}
	downloads := s.statsDownloads()
	for _, c := range clients {
		pc := pageClient{Name: c.Name}
		switch {
		case s.Stats != nil && s.Stats.IsListening(c.ID):
			pc.Connected, pc.State = true, "Connected now"
		case !c.LastSeen.IsZero():
			pc.State = "Last seen " + ago(now.Sub(c.LastSeen)) + " ago"
		default:
			pc.State = "Paired " + c.PairedAt.Format("2 Jan 2006")
		}
		for _, dl := range downloads {
			if dl.Client == c.Name {
				pc.Downloads = append(pc.Downloads, pageDownload{FileName: dl.FileName, Percent: percent(dl.Sent, dl.Size)})
			}
		}
		d.Clients = append(d.Clients, pc)
	}

	if s.Bonjour != "" {
		d.Recommendations = append(d.Recommendations, pageRecommendation{
			Text: "Q8 can't find this Relay by itself: " + s.Bonjour + ".",
			Fix:  []string{"Type its address in Q8's Connect to Relay…, or give Relay network_mode: host (see the README)."}})
	}

	d.Stats = s.pageStats(all, now)
	for i := len(all) - 1; i >= 0 && len(d.Recent) < 10; i-- {
		d.Recent = append(d.Recent, s.pageArrival(all[i], now))
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = pageTemplate.Execute(w, d)
}

func (s *Server) statsDownloads() []Download {
	if s.Stats == nil {
		return nil
	}
	return s.Stats.Downloads()
}

func (s *Server) pageApp(wt *arr.Watcher, all []hub.Arrival, now time.Time, d *pageData) pageApp {
	checked, problem := wt.Status()
	a := pageApp{Name: appName(wt.Client.App), Light: "grey"}
	if !checked.IsZero() {
		a.Checked = "checked " + ago(now.Sub(checked)) + " ago"
	}
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].App == wt.Client.App {
			a.LastImport = "last import " + ago(now.Sub(all[i].ImportedAt)) + " ago"
			break
		}
	}
	h, read := arr.Health{}, false
	if s.Health != nil {
		h, read = s.Health.Get(wt.Client.App)
	}
	switch {
	case problem != "":
		a.Light = "red"
		a.Problems = append(a.Problems, arr.Problem{Fault: true, Text: problem})
	case read && h.Unreachable != "":
		a.Light = "red"
		a.Problems = append(a.Problems, arr.Problem{Fault: true, Text: h.Unreachable})
	case !checked.IsZero():
		a.Light = "green"
	}
	if !read {
		return a
	}
	a.Version = h.Version
	a.Problems = append(a.Problems, h.Problems...)
	for _, p := range h.Problems {
		if p.Fault {
			a.Light = "red"
		} else if a.Light == "green" {
			a.Light = "amber"
		}
	}
	for _, disk := range h.Disks {
		a.Disks = append(a.Disks, pageDisk{Name: disk.Name, Free: size(disk.Free) + " free",
			Percent: int(disk.UsedShare()*100 + 0.5), Full: disk.UsedShare() >= arr.FullShare})
	}
	if h.Upgrades.On() {
		a.Upgrades = "Upgrades are on: a better release would be downloaded and sent to Q8 as a second copy."
		a.UpgradesHow = upgradeSteps(h.Upgrades)
		a.SettingsURL = settingsURL(wt.Client.BaseURL, h.Upgrades)
		d.Recommendations = append(d.Recommendations, pageRecommendation{
			Text: "Turn off upgrades in " + a.Name + ": Q8 converts each file once, and an upgrade comes back as a second copy.",
			Fix:  a.UpgradesHow, Link: a.SettingsURL})
	}
	if d.Focused {
		if h.RecycleBin == "" {
			a.RecycleBin = "No Recycle Bin set: Q8 focused deletes nothing here."
			d.Recommendations = append(d.Recommendations, pageRecommendation{
				Text: a.Name + " has no Recycle Bin, so Q8 focused can't clear its copies.",
				Fix:  []string{"Settings › Media Management › Recycle Bin: set a folder."}})
		} else {
			a.RecycleBin = "Recycle Bin: " + h.RecycleBin
		}
	}
	return a
}

// shortVersion is a commit's first seven characters, as GitHub shows it:
// each push to main is built with its commit as the version.
func shortVersion(v string) string {
	if len(v) == 40 && strings.Trim(v, "0123456789abcdef") == "" {
		return v[:7]
	}
	return v
}

func upgradeSteps(u arr.Upgrades) []string {
	var steps []string
	if len(u.Profiles) > 0 {
		names := make([]string, len(u.Profiles))
		for i, n := range u.Profiles {
			names[i] = "“" + n + "”"
		}
		steps = append(steps, "Settings › Profiles: untick Upgrades Allowed in "+list(names)+".")
	}
	if u.Repacks {
		steps = append(steps, "Settings › Media Management (Show Advanced) › Propers and Repacks: Do Not Upgrade Automatically.")
	}
	return steps
}

// settingsURL is the arr's own settings page where upgrades are turned
// off — only when its address could open in a browser, not a container's
// name like "radarr".
func settingsURL(base string, u arr.Upgrades) string {
	parsed, err := url.Parse(base)
	if err != nil || !strings.Contains(parsed.Hostname(), ".") && parsed.Hostname() != "localhost" {
		return ""
	}
	page := "settings/profiles"
	if len(u.Profiles) == 0 {
		page = "settings/mediamanagement"
	}
	return strings.TrimRight(base, "/") + "/" + page
}

func (s *Server) pageStats(all []hub.Arrival, now time.Time) []pageStat {
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	var today, week int
	for _, a := range all {
		if !a.ImportedAt.Before(midnight) {
			today++
		}
		if now.Sub(a.ImportedAt) <= 7*24*time.Hour {
			week++
		}
	}
	stats := []pageStat{
		{"Announced today", fmt.Sprint(today)},
		{"This week", fmt.Sprint(week)},
		{"In total", fmt.Sprint(s.Hub.Latest())},
	}
	if s.Stats != nil {
		stats = append(stats, pageStat{"Sent to Q8", size(s.Stats.Served())})
		if wait := s.Stats.TypicalWait(); wait > 0 {
			stats = append(stats, pageStat{"Import to Q8, typically", ago(wait)})
		}
	}
	if s.Focused != nil {
		if cleared, freed := s.Focused.Totals(); cleared > 0 {
			stats = append(stats, pageStat{"Cleared from the arrs", fmt.Sprint(cleared)},
				pageStat{"Space freed", size(freed)})
		}
	}
	return stats
}

func (s *Server) pageArrival(a hub.Arrival, now time.Time) pageArrival {
	p := pageArrival{Title: arrivalTitle(a), App: appName(a.App), Size: size(a.Size),
		When: ago(now.Sub(a.ImportedAt)) + " ago", Waiting: true}
	if s.Stats != nil {
		if f, ok := s.Stats.Fetched(a.ID); ok {
			p.Waiting = false
			p.Fetched = "fetched by " + f.Client
		}
	}
	if p.Waiting {
		p.Fetched = "not fetched yet"
	}
	return p
}

func arrivalTitle(a hub.Arrival) string {
	switch {
	case a.Kind == "episode" && a.Title != "":
		return fmt.Sprintf("%s S%02dE%02d", a.Title, a.Season, a.Episode)
	case a.Kind == "track" && a.Artist != "" && a.Title != "":
		return a.Artist + " – " + a.Title
	case a.Title != "" && a.Year > 0:
		return fmt.Sprintf("%s (%d)", a.Title, a.Year)
	case a.Title != "":
		return a.Title
	}
	return a.FileName
}

func appName(app string) string {
	if n, ok := map[string]string{"radarr": "Radarr", "sonarr": "Sonarr", "lidarr": "Lidarr"}[app]; ok {
		return n
	}
	return app
}

func list(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

func percent(part, whole int64) int {
	if whole <= 0 {
		return 0
	}
	return int(float64(part) / float64(whole) * 100)
}

// ago is a duration as people say it: "12s", "5 min", "3 h", "2 d".
func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h", int(d.Hours()))
	}
	return fmt.Sprintf("%d d", int(d.Hours()/24))
}

// size is bytes as Finder says them, in powers of 1000.
func size(b int64) string {
	units := []string{"bytes", "KB", "MB", "GB", "TB"}
	v, i := float64(b), 0
	for v >= 1000 && i < len(units)-1 {
		v /= 1000
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d bytes", b)
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

var pageTemplate = template.Must(template.New("page").Parse(`<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<meta http-equiv="refresh" content="10">
<title>{{.Name}}</title>
<style>
:root{--bg:#121214;--card:#1E1E22;--line:#2A2A2F;--text:#ECECEE;--dim:#8E8E93;--accent:#F28C3E;--caution:#C7A33D;--alert:#C25C4F;--ok:#4CC468}
*{box-sizing:border-box}
body{font:14px/1.5 -apple-system,BlinkMacSystemFont,system-ui,sans-serif;max-width:46em;margin:0 auto;padding:2.5em 1.2em 4em;color:var(--text);background:var(--bg)}
h1{font-size:1.6em;margin:0}h2{font-size:.78em;font-weight:600;letter-spacing:.08em;text-transform:uppercase;color:var(--dim);margin:2.2em 0 .6em}
.dim{color:var(--dim)}.small{font-size:.86em}.mono{font-family:ui-monospace,SFMono-Regular,Menlo,monospace}
.head{display:flex;align-items:baseline;gap:.8em;flex-wrap:wrap}
.mode{font-size:.8em;padding:.15em .6em;border-radius:1em;border:1px solid var(--line);color:var(--dim)}
.mode.focused{color:var(--caution);border-color:var(--caution)}
.card{background:var(--card);border-radius:10px;padding:.2em 1em;margin:.5em 0}
.row{display:flex;align-items:center;gap:.6em;padding:.7em 0;border-bottom:1px solid var(--line)}
.card>.row:last-child,.card>.block:last-child{border-bottom:none}
.block{padding:.7em 0;border-bottom:1px solid var(--line)}
.grow{flex:1;min-width:0}.right{text-align:right}
.dot{width:.6em;height:.6em;border-radius:50%;flex:none;background:var(--dim)}
.dot.green{background:var(--ok)}.dot.amber{background:var(--caution)}.dot.red{background:var(--alert)}
.caution{color:var(--caution)}.alert{color:var(--alert)}.line{margin:.25em 0 0 1.2em}
.bar{height:4px;border-radius:2px;background:var(--line);overflow:hidden;margin-top:.3em}.bar>i{display:block;height:100%;background:var(--accent)}
.bar.full>i{background:var(--caution)}
.stats{display:grid;grid-template-columns:repeat(auto-fill,minmax(10em,1fr));gap:.5em}
.stat{background:var(--card);border-radius:10px;padding:.7em 1em}.stat b{display:block;font-size:1.35em;font-weight:600}
.code{font:600 1.6em ui-monospace,SFMono-Regular,Menlo,monospace;letter-spacing:.08em;color:var(--accent)}
a{color:var(--accent);text-decoration:none}a:hover{text-decoration:underline}
ul{margin:.3em 0 0 1.2em;padding:0}li{margin:.1em 0}
</style></head><body>
<div class="head"><h1>{{.Name}}</h1><span class="mode{{if .Focused}} focused{{end}}">{{.Mode}}</span></div>
<p class="dim small">Version {{.Version}} · protocol {{.Protocol}}{{if .Uptime}} · {{.Uptime}}{{end}} · ID <span class="mono">{{.ID}}</span>
{{if .Focused}}<br><span class="caution">Once Q8 has a file, Relay deletes the arr's copy into its Recycle Bin.</span>{{end}}</p>

{{if .Recommendations}}<h2>Worth doing</h2><div class="card">{{range .Recommendations}}<div class="block">
<div class="caution">{{.Text}}</div>{{if .Fix}}<ul class="dim small">{{range .Fix}}<li>{{.}}</li>{{end}}</ul>{{end}}
{{if .Link}}<div class="small line"><a href="{{.Link}}" target="_blank" rel="noopener">Open its settings</a></div>{{end}}</div>{{end}}</div>{{end}}

<h2>Apps</h2>
{{range .Apps}}<div class="card"><div class="row"><span class="dot {{.Light}}"></span><span class="grow"><b>{{.Name}}</b>{{if .Version}} <span class="dim small">{{.Version}}</span>{{end}}</span>
<span class="dim small right">{{if .Checked}}{{.Checked}}{{else}}not checked yet{{end}}{{if .LastImport}}<br>{{.LastImport}}{{end}}</span></div>
{{if or .Problems .Upgrades .Disks .RecycleBin}}<div class="block small">
{{range .Problems}}<div class="{{if .Fault}}alert{{else}}caution{{end}}">{{.Text}}</div>{{end}}
{{if .Upgrades}}<div class="caution">{{.Upgrades}}</div>{{end}}
{{range .Disks}}<div style="margin-top:.4em"><span>{{.Name}}</span> <span class="dim">{{.Percent}}% full · {{.Free}}</span><div class="bar{{if .Full}} full{{end}}"><i style="width:{{.Percent}}%"></i></div></div>{{end}}
{{if .RecycleBin}}<div class="dim" style="margin-top:.4em">{{.RecycleBin}}</div>{{end}}
</div>{{end}}</div>
{{else}}<div class="card"><div class="row alert">No app set.</div></div>{{end}}

<h2>Macs</h2><div class="card">
{{range .Clients}}<div class="block"><div class="row" style="padding:0;border:none"><span class="dot {{if .Connected}}green{{end}}"></span><span class="grow">{{.Name}}</span><span class="dim small">{{.State}}</span></div>
{{range .Downloads}}<div class="small line">Sending {{.FileName}} <span class="dim">{{.Percent}}%</span><div class="bar"><i style="width:{{.Percent}}%"></i></div></div>{{end}}</div>
{{else}}<div class="row dim">No Mac paired yet.</div>{{end}}
<div class="block"><div class="dim small">Pair a Mac: in Q8, Media Server › + › Connect to Relay…, and type</div>
{{if .ShowCode}}<div class="code">{{.Code}}</div><div class="dim small">It changes once used, and every 15 minutes.</div>
{{else}}<div class="dim small">the code in Relay's log.</div>{{end}}</div></div>

<h2>Numbers</h2><div class="stats">{{range .Stats}}<div class="stat"><b>{{.Value}}</b><span class="dim small">{{.Label}}</span></div>{{end}}</div>

<h2>Recent</h2><div class="card">
{{range .Recent}}<div class="row"><span class="grow">{{.Title}}<br><span class="dim small">{{.App}} · {{.Size}} · {{.When}}</span></span><span class="small {{if .Waiting}}dim{{end}}">{{.Fetched}}</span></div>
{{else}}<div class="row dim">Nothing announced yet. Relay announces what your arrs import from now on.</div>{{end}}</div>
</body></html>`))
