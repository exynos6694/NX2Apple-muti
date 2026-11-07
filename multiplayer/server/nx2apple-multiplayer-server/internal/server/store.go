package server

import (
	"errors"
	"sort"
	"sync"
	"time"
)

const (
	maxClientID = 80
	maxTitleID  = 32
	maxVersion  = 48
	maxNickname = 32
	maxSignals  = 256
)

var (
	ErrInvalid = errors.New("invalid request")
	ErrMissing = errors.New("client not registered")
)

type Game struct {
	TitleID     string `json:"title_id"`
	GameVersion string `json:"game_version"`
	Protocol    string `json:"protocol"`
}

type Player struct {
	ClientID string    `json:"client_id"`
	Nickname string    `json:"nickname,omitempty"`
	Game     Game      `json:"game"`
	SeenAt   time.Time `json:"seen_at"`
	UDPAddr  string    `json:"udp_addr,omitempty"`
}

type Signal struct {
	Sequence uint64         `json:"sequence"`
	From     string         `json:"from"`
	To       string         `json:"to"`
	Kind     string         `json:"kind"`
	Data     map[string]any `json:"data,omitempty"`
	Created  time.Time      `json:"created_at"`
}

type Store struct {
	mu       sync.Mutex
	ttl      time.Duration
	players  map[string]Player
	signals  map[string][]Signal
	sequence uint64
}

func NewStore(ttl time.Duration) *Store {
	return &Store{
		ttl: ttl, players: make(map[string]Player),
		signals: make(map[string][]Signal),
	}
}

func validText(s string, max int) bool {
	return s != "" && len(s) <= max
}

func (s *Store) Register(p Player) error {
	if !validText(p.ClientID, maxClientID) ||
		!validText(p.Game.TitleID, maxTitleID) ||
		!validText(p.Game.GameVersion, maxVersion) ||
		!validText(p.Game.Protocol, maxVersion) ||
		len(p.Nickname) > maxNickname {
		return ErrInvalid
	}
	now := time.Now().UTC()
	p.SeenAt = now
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked(now)
	if old, ok := s.players[p.ClientID]; ok && old.Game != p.Game {
		delete(s.players, p.ClientID)
		delete(s.signals, p.ClientID)
	}
	s.players[p.ClientID] = p
	return nil
}

func (s *Store) Heartbeat(clientID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	s.expireLocked(now)
	p, ok := s.players[clientID]
	if !ok {
		return ErrMissing
	}
	p.SeenAt = now
	s.players[clientID] = p
	return nil
}

func (s *Store) Leave(clientID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.players, clientID)
	delete(s.signals, clientID)
	for id, queue := range s.signals {
		kept := queue[:0]
		for _, item := range queue {
			if item.From != clientID && item.To != clientID {
				kept = append(kept, item)
			}
		}
		s.signals[id] = kept
	}
}

func (s *Store) SetUDPAddr(clientID, addr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	s.expireLocked(now)
	p, ok := s.players[clientID]
	if !ok {
		return ErrMissing
	}
	p.UDPAddr = addr
	p.SeenAt = now
	s.players[clientID] = p
	return nil
}

func (s *Store) Players(titleID, gameVersion, protocol string) []Player {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked(time.Now().UTC())
	out := make([]Player, 0)
	for _, p := range s.players {
		if p.Game.TitleID == titleID &&
			p.Game.GameVersion == gameVersion &&
			p.Game.Protocol == protocol {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ClientID < out[j].ClientID })
	return out
}

func (s *Store) AddSignal(from, to, kind string, data map[string]any) (Signal, error) {
	if !validText(from, maxClientID) || !validText(to, maxClientID) ||
		!validText(kind, 32) || from == to {
		return Signal{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	s.expireLocked(now)
	if _, ok := s.players[from]; !ok {
		return Signal{}, ErrMissing
	}
	if _, ok := s.players[to]; !ok {
		return Signal{}, ErrMissing
	}
	s.sequence++
	item := Signal{Sequence: s.sequence, From: from, To: to, Kind: kind, Data: data, Created: now}
	q := append(s.signals[to], item)
	if len(q) > maxSignals {
		q = append([]Signal(nil), q[len(q)-maxSignals:]...)
	}
	s.signals[to] = q
	return item, nil
}

func (s *Store) Signals(clientID string, after uint64) ([]Signal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked(time.Now().UTC())
	if _, ok := s.players[clientID]; !ok {
		return nil, ErrMissing
	}
	out := make([]Signal, 0)
	for _, item := range s.signals[clientID] {
		if item.Sequence > after {
			out = append(out, item)
		}
	}
	return out, nil
}

func (s *Store) expireLocked(now time.Time) {
	for id, p := range s.players {
		if now.Sub(p.SeenAt) > s.ttl {
			delete(s.players, id)
			delete(s.signals, id)
			for target, queue := range s.signals {
				kept := queue[:0]
				for _, item := range queue {
					if item.From != id && item.To != id {
						kept = append(kept, item)
					}
				}
				s.signals[target] = kept
			}
		}
	}
}
