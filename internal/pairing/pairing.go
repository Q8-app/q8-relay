// Package pairing lets a Q8 in: Relay shows a short code, Q8 sends it back
// once and gets a key it keeps in its Keychain. Relay keeps only each key's
// hash.
package pairing

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/q8-app/q8-relay/internal/store"
)

// CodeLife is how long a pairing code works.
const CodeLife = 15 * time.Minute

// MaxTries is how many wrong codes are taken a minute, so eight digits
// can't be guessed.
const MaxTries = 5

const fileName = "pairing.json"

// ErrWrongCode is a code that isn't the current one, or has run out.
var ErrWrongCode = errors.New("that code isn't right, or it has run out: use the one Relay shows now")

// ErrTooMany is too many wrong codes in a minute.
var ErrTooMany = errors.New("too many wrong codes: wait a minute")

// Client is one paired Q8.
type Client struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	KeyHash  string    `json:"keyHash"`
	PairedAt time.Time `json:"pairedAt"`
	LastSeen time.Time `json:"lastSeen,omitempty"`
}

type state struct {
	RelayID string   `json:"relayId"`
	Clients []Client `json:"clients"`
}

// Pairing holds the current code and the paired clients.
type Pairing struct {
	mu        sync.Mutex
	dir       string
	state     state
	code      string
	expires   time.Time
	tries     []time.Time
	now       func() time.Time
	onNewCode func(code string)
}

// Open reads the paired clients from dir, giving Relay its ID on first run.
func Open(dir string, onNewCode func(string)) (*Pairing, error) {
	p := &Pairing{dir: dir, now: time.Now, onNewCode: onNewCode}
	if err := store.Load(dir, fileName, &p.state); err != nil {
		return p, err
	}
	if p.state.RelayID == "" {
		p.state.RelayID = randomHex(8)
		if err := store.Save(dir, fileName, p.state); err != nil {
			return p, err
		}
	}
	return p, nil
}

// RelayID names this Relay to Q8, across restarts.
func (p *Pairing) RelayID() string { return p.state.RelayID }

// Code gives the code that pairs now, making a new one when the last ran
// out, and when it runs out.
func (p *Pairing) Code() (string, time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.currentCode()
}

func (p *Pairing) currentCode() (string, time.Time) {
	if p.code == "" || !p.now().Before(p.expires) {
		n, _ := rand.Int(rand.Reader, big.NewInt(100_000_000))
		p.code = fmt.Sprintf("%04d-%04d", n.Int64()/10_000, n.Int64()%10_000)
		p.expires = p.now().Add(CodeLife)
		if p.onNewCode != nil {
			p.onNewCode(p.code)
		}
	}
	return p.code, p.expires
}

// Pair takes code from a Q8 named name and gives back its key. The code is
// used up: the next Q8 needs the next one.
func (p *Pairing) Pair(code, name string) (Client, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	recent := p.tries[:0]
	for _, t := range p.tries {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	p.tries = recent
	if len(p.tries) >= MaxTries {
		return Client{}, "", ErrTooMany
	}
	current, _ := p.currentCode()
	if subtle.ConstantTimeCompare([]byte(normalise(code)), []byte(normalise(current))) != 1 {
		p.tries = append(p.tries, now)
		return Client{}, "", ErrWrongCode
	}
	if name == "" {
		name = "Q8"
	}
	key := randomHex(32)
	client := Client{ID: randomHex(8), Name: name, KeyHash: hash(key), PairedAt: now}
	p.state.Clients = append(p.state.Clients, client)
	if err := store.Save(p.dir, fileName, p.state); err != nil {
		p.state.Clients = p.state.Clients[:len(p.state.Clients)-1]
		return Client{}, "", err
	}
	p.code = "" // used: a new one is made when it's next asked for
	p.currentCode()
	return client, key, nil
}

// Check gives the client key belongs to.
func (p *Pairing) Check(key string) (Client, bool) {
	if key == "" {
		return Client{}, false
	}
	h := hash(key)
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, c := range p.state.Clients {
		if subtle.ConstantTimeCompare([]byte(c.KeyHash), []byte(h)) == 1 {
			p.state.Clients[i].LastSeen = p.now()
			return p.state.Clients[i], true
		}
	}
	return Client{}, false
}

// Clients lists the paired Q8s.
func (p *Pairing) Clients() []Client {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Client(nil), p.state.Clients...)
}

// Forget unpairs the client id.
func (p *Pairing) Forget(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	kept := p.state.Clients[:0]
	for _, c := range p.state.Clients {
		if c.ID != id {
			kept = append(kept, c)
		}
	}
	p.state.Clients = kept
	return store.Save(p.dir, fileName, p.state)
}

func normalise(code string) string {
	out := make([]byte, 0, len(code))
	for i := 0; i < len(code); i++ {
		if c := code[i]; c >= '0' && c <= '9' {
			out = append(out, c)
		}
	}
	return string(out)
}

func hash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func randomHex(bytes int) string {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		panic("no randomness: " + err.Error()) // nothing safe can be made without it
	}
	return hex.EncodeToString(b)
}
