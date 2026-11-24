package pairing

import (
	"errors"
	"testing"
	"time"
)

func TestPairOnceWithTheCode(t *testing.T) {
	dir := t.TempDir()
	p, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := p.Code()
	client, key, err := p.Pair(code, "Living Room Mac")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := p.Check(key); !ok || got.ID != client.ID {
		t.Fatal("the key given isn't accepted")
	}
	if _, _, err := p.Pair(code, "again"); !errors.Is(err, ErrWrongCode) {
		t.Fatal("a used code paired twice")
	}
	again, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := again.Check(key); !ok || again.RelayID() != p.RelayID() {
		t.Fatal("pairing didn't survive a restart")
	}
	if _, ok := p.Check("nonsense"); ok {
		t.Fatal("a wrong key was accepted")
	}
}

func TestCodeRunsOutAndTriesAreLimited(t *testing.T) {
	p, err := Open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	p.now = func() time.Time { return now }
	code, _ := p.Code()
	now = now.Add(CodeLife + time.Second)
	if next, _ := p.Code(); next == code {
		t.Fatal("the code didn't change once it ran out")
	}
	for range MaxTries {
		if _, _, err := p.Pair("0000-0000", ""); !errors.Is(err, ErrWrongCode) {
			t.Fatalf("got %v", err)
		}
	}
	current, _ := p.Code()
	if _, _, err := p.Pair(current, ""); !errors.Is(err, ErrTooMany) {
		t.Fatalf("a sixth try in a minute was taken: %v", err)
	}
	now = now.Add(time.Minute)
	if _, _, err := p.Pair(current, ""); err != nil {
		t.Fatalf("after a minute the right code failed: %v", err)
	}
}

func TestForget(t *testing.T) {
	p, _ := Open(t.TempDir(), nil)
	code, _ := p.Code()
	c, key, _ := p.Pair(code, "")
	if err := p.Forget(c.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Check(key); ok {
		t.Fatal("a forgotten client still gets in")
	}
}
