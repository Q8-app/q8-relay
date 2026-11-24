package config

import "testing"

func TestMapTakesLongestWholeFolder(t *testing.T) {
	maps, err := ParsePathMaps("/data=/mnt/data, /data/media=/media")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"/data/media/Movies/Dune (2021)/Dune.mkv": "/media/Movies/Dune (2021)/Dune.mkv",
		"/data/mediaextra/a.mkv":                  "/mnt/data/mediaextra/a.mkv",
		"/downloads/a.mkv":                        "/downloads/a.mkv",
		"/data":                                   "/mnt/data",
	}
	for in, want := range cases {
		if got := Map(maps, in); got != want {
			t.Errorf("Map(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParsePathMapsRefusesRelative(t *testing.T) {
	if _, err := ParsePathMaps("data=/data"); err == nil {
		t.Fatal("a relative path was taken")
	}
}

func TestFromEnvNeedsURLAndKey(t *testing.T) {
	t.Setenv("RADARR_URL", "http://radarr:7878")
	if _, err := FromEnv(); err == nil {
		t.Fatal("Radarr without a key was taken")
	}
	t.Setenv("RADARR_API_KEY", "k")
	c, err := FromEnv()
	if err != nil || len(c.Apps) != 1 || c.Apps[0].Name != "radarr" || c.Port != 8484 {
		t.Fatalf("got %+v, %v", c, err)
	}
}
