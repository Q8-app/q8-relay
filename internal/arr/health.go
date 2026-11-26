package arr

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Health is what Relay's page says about one arr: its version, its own
// health warnings, how full its library disks are, and whether it would
// download a better release of what it has. Read with GET only, the same
// things Q8's Media Server row reads.
type Health struct {
	Name     string
	Version  string
	Problems []Problem
	Disks    []Disk
	Upgrades Upgrades
	// RecycleBin is the arr's Recycle Bin folder, "" when none is set.
	RecycleBin string
	// Unreachable says why it couldn't be read, "" when it could.
	Unreachable string
	Checked     time.Time
}

// Problem is one of the arr's own health warnings, or a full disk.
type Problem struct {
	Fault bool // stopped working, rather than worth a look
	Text  string
}

// Disk is a disk holding one of the arr's root folders.
type Disk struct {
	Name  string
	Free  int64
	Total int64
}

// UsedShare is how full the disk is, 0 to 1.
func (d Disk) UsedShare() float64 {
	if d.Total <= 0 {
		return 0
	}
	return 1 - float64(d.Free)/float64(d.Total)
}

// Upgrades says whether the arr would replace a file it has with a better
// release: Q8 converts each file once, so that's a second copy.
type Upgrades struct {
	Profiles []string // quality profiles with Upgrades Allowed
	Repacks  bool     // Propers and Repacks is Prefer and Upgrade
	Known    bool     // read at all
}

// On is upgrades of either kind.
func (u Upgrades) On() bool { return len(u.Profiles) > 0 || u.Repacks }

// FullShare is how full a disk is before the page says so.
const FullShare = 0.9

// Health reads the arr's health. A part that can't be read is left out;
// Unreachable is set only when the arr doesn't answer at all.
func (c *Client) Health(ctx context.Context) Health {
	h := Health{Name: c.name(), Checked: time.Now()}
	_, version, err := c.Status(ctx)
	if err != nil {
		h.Unreachable = err.Error()
		return h
	}
	h.Version = version
	api := "api/" + c.version() + "/"

	var checks []struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if c.get(ctx, api+"health", nil, &checks) == nil {
		for _, ch := range checks {
			text := strings.TrimSpace(ch.Message)
			switch strings.ToLower(ch.Type) {
			case "error":
				h.Problems = append(h.Problems, Problem{Fault: true, Text: text})
			case "warning":
				h.Problems = append(h.Problems, Problem{Text: text})
			}
		}
	}

	var mm struct {
		MinimumFree    int    `json:"minimumFreeSpaceWhenImporting"`
		RecycleBin     string `json:"recycleBin"`
		PropersRepacks string `json:"downloadPropersAndRepacks"`
	}
	mmErr := c.get(ctx, api+"config/mediamanagement", nil, &mm)
	if mmErr == nil {
		h.RecycleBin = strings.TrimSpace(mm.RecycleBin)
	}

	var disks []struct {
		Path       string `json:"path"`
		Label      string `json:"label"`
		FreeSpace  int64  `json:"freeSpace"`
		TotalSpace int64  `json:"totalSpace"`
	}
	var roots []struct {
		Path string `json:"path"`
	}
	if c.get(ctx, api+"diskspace", nil, &disks) == nil && c.get(ctx, api+"rootfolder", nil, &roots) == nil {
		minimum := int64(100)
		if mmErr == nil && mm.MinimumFree > 0 {
			minimum = int64(mm.MinimumFree)
		}
		seen := map[string]bool{}
		for _, root := range roots {
			best := -1
			for i, d := range disks {
				if under(root.Path, d.Path) && (best < 0 || len(d.Path) > len(disks[best].Path)) {
					best = i
				}
			}
			if best < 0 || seen[disks[best].Path] || disks[best].TotalSpace <= 0 {
				continue
			}
			d := disks[best]
			seen[d.Path] = true
			name := strings.TrimSpace(d.Label)
			if name == "" {
				name = d.Path
			}
			disk := Disk{Name: name, Free: d.FreeSpace, Total: d.TotalSpace}
			h.Disks = append(h.Disks, disk)
			switch {
			case d.FreeSpace <= minimum*1_000_000:
				h.Problems = append(h.Problems, Problem{Fault: true, Text: fmt.Sprintf("%s is full.", name)})
			case disk.UsedShare() >= FullShare:
				h.Problems = append(h.Problems, Problem{Text: fmt.Sprintf("%s is %.0f%% full.", name, disk.UsedShare()*100)})
			}
		}
	}

	var profiles []struct {
		Name           string `json:"name"`
		UpgradeAllowed bool   `json:"upgradeAllowed"`
	}
	profilesErr := c.get(ctx, api+"qualityprofile", nil, &profiles)
	if profilesErr == nil || mmErr == nil {
		h.Upgrades.Known = true
		for _, p := range profiles {
			if p.UpgradeAllowed {
				h.Upgrades.Profiles = append(h.Upgrades.Profiles, p.Name)
			}
		}
		h.Upgrades.Repacks = strings.EqualFold(mm.PropersRepacks, "preferAndUpgrade")
	}
	sort.SliceStable(h.Problems, func(i, j int) bool { return h.Problems[i].Fault && !h.Problems[j].Fault })
	return h
}

// under is path inside folder, by whole parts, ignoring case.
func under(path, folder string) bool {
	p := strings.Split(strings.Trim(path, "/"), "/")
	f := strings.Split(strings.Trim(folder, "/"), "/")
	if folder == "/" || strings.Trim(folder, "/") == "" {
		return true
	}
	if len(f) > len(p) {
		return false
	}
	for i := range f {
		if !strings.EqualFold(p[i], f[i]) {
			return false
		}
	}
	return true
}

// Board keeps each arr's health, read again every few minutes, for the page.
type Board struct {
	mu     sync.Mutex
	health map[string]Health
}

// NewBoard is an empty board.
func NewBoard() *Board { return &Board{health: map[string]Health{}} }

// Get is the app's health as last read, and whether it has been.
func (b *Board) Get(app string) (Health, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	h, ok := b.health[app]
	return h, ok
}

// Set keeps an app's health.
func (b *Board) Set(app string, h Health) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.health[app] = h
}

// Run reads every client's health now and every interval until ctx ends.
func (b *Board) Run(ctx context.Context, clients []*Client, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		for _, c := range clients {
			read, cancel := context.WithTimeout(ctx, 30*time.Second)
			b.Set(c.App, c.Health(read))
			cancel()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
