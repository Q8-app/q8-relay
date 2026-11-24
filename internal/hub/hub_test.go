package hub

import "testing"

func TestPublishNumbersOnceAndCatchesUp(t *testing.T) {
	dir := t.TempDir()
	h, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, isNew, _ := h.Publish(Arrival{ID: "radarr-1"})
	if !isNew || a.Seq != 1 || a.FileURL != "/v1/files/radarr-1" {
		t.Fatalf("got %+v", a)
	}
	if _, isNew, _ := h.Publish(Arrival{ID: "radarr-1"}); isNew {
		t.Fatal("one file announced twice")
	}
	missed, ch, stop := h.Listen(0)
	defer stop()
	if len(missed) != 1 {
		t.Fatalf("missed %d", len(missed))
	}
	h.Publish(Arrival{ID: "sonarr-7"})
	if got := <-ch; got.Seq != 2 {
		t.Fatalf("got %+v", got)
	}
	again, _ := Open(dir)
	if again.Latest() != 2 || len(again.Since(1)) != 1 {
		t.Fatal("arrivals didn't survive a restart")
	}
}

func TestKeepsOnlyTheNewest(t *testing.T) {
	h, _ := Open(t.TempDir())
	for i := range Keep + 10 {
		h.Publish(Arrival{ID: string(rune('a'+i%26)) + string(rune(i))})
	}
	if n := len(h.Since(0)); n != Keep {
		t.Fatalf("kept %d", n)
	}
	if h.Since(0)[0].Seq != 11 {
		t.Fatal("the oldest weren't the ones let go")
	}
}
