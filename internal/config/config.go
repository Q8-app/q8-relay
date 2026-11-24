// Package config reads Relay's settings from its environment.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// App is one arr Relay watches.
type App struct {
	Name    string // radarr, sonarr or lidarr
	BaseURL string
	APIKey  string
}

// PathMap rewrites the start of a path as the arr reports it into the
// same file as Relay sees it, for stacks that mount it elsewhere.
type PathMap struct {
	From string
	To   string
}

// Config is everything Relay is told at start.
type Config struct {
	Name          string
	Port          int
	DataDir       string
	PollInterval  time.Duration
	Apps          []App
	PathMaps      []PathMap
	ShowCode      bool
	Announce      bool
}

// FromEnv reads the RELAY_* and <APP>_URL / <APP>_API_KEY variables.
func FromEnv() (Config, error) {
	c := Config{
		Name:         env("RELAY_NAME", "Q8 Relay"),
		DataDir:      env("RELAY_DATA", "/config"),
		ShowCode:     env("RELAY_SHOW_CODE", "true") != "false",
		Announce:     env("RELAY_ANNOUNCE", "true") != "false",
		Port:         8484,
		PollInterval: 10 * time.Second,
	}
	if v := os.Getenv("RELAY_PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p < 1 || p > 65535 {
			return c, fmt.Errorf("RELAY_PORT %q is not a port", v)
		}
		c.Port = p
	}
	if v := os.Getenv("RELAY_POLL_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 2*time.Second {
			return c, fmt.Errorf("RELAY_POLL_INTERVAL %q: give a duration of 2s or more, like 10s", v)
		}
		c.PollInterval = d
	}
	for _, name := range []string{"radarr", "sonarr", "lidarr"} {
		upper := strings.ToUpper(name)
		url, key := os.Getenv(upper+"_URL"), os.Getenv(upper+"_API_KEY")
		switch {
		case url == "" && key == "":
			continue
		case url == "" || key == "":
			return c, fmt.Errorf("%s needs both %s_URL and %s_API_KEY", name, upper, upper)
		}
		c.Apps = append(c.Apps, App{Name: name, BaseURL: strings.TrimRight(url, "/"), APIKey: key})
	}
	maps, err := ParsePathMaps(os.Getenv("RELAY_PATH_MAP"))
	if err != nil {
		return c, err
	}
	c.PathMaps = maps
	return c, nil
}

// ParsePathMaps reads "from=to" pairs separated by commas:
// "/downloads=/data/downloads,/movies=/data/media/movies".
func ParsePathMaps(text string) ([]PathMap, error) {
	var maps []PathMap
	for _, pair := range strings.Split(text, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		from, to, ok := strings.Cut(pair, "=")
		if !ok || !strings.HasPrefix(from, "/") || !strings.HasPrefix(to, "/") {
			return nil, fmt.Errorf("RELAY_PATH_MAP: %q should be /arr/path=/relay/path", pair)
		}
		maps = append(maps, PathMap{From: strings.TrimRight(from, "/"), To: strings.TrimRight(to, "/")})
	}
	return maps, nil
}

// Map gives the path Relay reads for one the arr reported: the longest
// matching map, folder by folder, so /data/media never takes
// /data/mediaextra. Unmapped paths are used as they are.
func Map(maps []PathMap, path string) string {
	best := -1
	for i, m := range maps {
		if path == m.From || strings.HasPrefix(path, m.From+"/") {
			if best < 0 || len(m.From) > len(maps[best].From) {
				best = i
			}
		}
	}
	if best < 0 {
		return path
	}
	return maps[best].To + strings.TrimPrefix(path, maps[best].From)
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
