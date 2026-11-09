// Package relay is the internet-play relay that the engine's co-op module dials with DM_COOP_RELAY (engine/dm/host/coop_int.h, "PKR1").
//
// Nobody forwards a port: the host and the visitors all connect out to the relay. A host opens its room with a control link (HOST); a
// visitor asks for the room (JOIN); the relay tells the host (VISITOR id, cookie), the host dials one more link for that visitor
// (ATTACH id, cookie), and the relay pairs the two and copies bytes between them without reading them. The room id is one-way from the
// room code (HKDF in coop_auth.c), and everything inside a pair is the players' own authenticated protocol 3, so the relay can neither
// learn the code nor forge or change a game message. It can read them: nothing is encrypted.
//
// Wire format (all integers little-endian):
//
//	hello, the first 32 bytes of every link: magic u32, kind u8, version u8 (1), 2 reserved, room id [16], visitor id u32, cookie u32
//	relay message, 16 bytes:                   magic u32, type u8, code u8, 2 reserved, arg u32 (visitor id), cookie u32
//
// The relay sends nothing on an ATTACH link (the host reads game frames there from the first byte) and exactly one OK on a JOIN link
// before the copy starts (the station sends its HELLO frame after it).
package relay

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	Magic    = 0x31524B50 // "PKR1"
	HelloLen = 32
	MsgLen   = 16
	Version  = 1
)

// Link kinds (hello byte 4).
const (
	KindHost = 1 + iota
	KindJoin
	KindAttach
)

// Relay message types (message byte 4).
const (
	MsgOK = 1 + iota
	MsgVisitor
	MsgGone
	MsgErr
	MsgPong
	MsgPing
)

// Error codes (message byte 5 of MsgErr).
const (
	ErrNoRoom = 1 + iota
	ErrTaken
	ErrFull
	ErrLimit
	ErrTimeout
	ErrBad
	ErrExpired
)

// Config holds the relay's limits. Zero fields take the defaults of Defaults().
type Config struct {
	MaxRooms        int           // rooms open at once
	MaxVisitors     int           // visitors per room, paired and waiting (the game takes 8 nodes: the host and 7)
	MaxLinks        int           // TCP links at once
	MaxLinksPerIP   int           // TCP links at once from one address
	NewLinksPerSec  float64       // new links a second from one address (burst twice)
	LookupsPerMin   float64       // JOINs for absent rooms a minute from one address
	HelloTimeout    time.Duration // the first 32 bytes must arrive within this
	PairTimeout     time.Duration // a visitor is paired within this or refused (ErrTimeout)
	HostIdle        time.Duration // a host's control link silent this long closes its room (hosts ping every 5 s)
	RoomMaxAge      time.Duration // a room closes after this (ErrExpired)
	RoomMaxBytes    int64         // bytes copied for a room's pairs before they close (ErrExpired for new visitors)
	LinkBytesPerSec int           // per direction of a pair (burst twice); reading waits, nothing is dropped
	Logf            func(format string, args ...any)
}

func Defaults() Config {
	return Config{
		MaxRooms:        64,
		MaxVisitors:     7,
		MaxLinks:        256,
		MaxLinksPerIP:   24,
		NewLinksPerSec:  10,
		LookupsPerMin:   60,
		HelloTimeout:    5 * time.Second,
		PairTimeout:     10 * time.Second,
		HostIdle:        30 * time.Second,
		RoomMaxAge:      12 * time.Hour,
		RoomMaxBytes:    4 << 30,
		LinkBytesPerSec: 256 << 10,
	}
}

func (c *Config) fill() {
	d := Defaults()
	if c.MaxRooms <= 0 {
		c.MaxRooms = d.MaxRooms
	}
	if c.MaxVisitors <= 0 {
		c.MaxVisitors = d.MaxVisitors
	}
	if c.MaxLinks <= 0 {
		c.MaxLinks = d.MaxLinks
	}
	if c.MaxLinksPerIP <= 0 {
		c.MaxLinksPerIP = d.MaxLinksPerIP
	}
	if c.NewLinksPerSec <= 0 {
		c.NewLinksPerSec = d.NewLinksPerSec
	}
	if c.LookupsPerMin <= 0 {
		c.LookupsPerMin = d.LookupsPerMin
	}
	if c.HelloTimeout <= 0 {
		c.HelloTimeout = d.HelloTimeout
	}
	if c.PairTimeout <= 0 {
		c.PairTimeout = d.PairTimeout
	}
	if c.HostIdle <= 0 {
		c.HostIdle = d.HostIdle
	}
	if c.RoomMaxAge <= 0 {
		c.RoomMaxAge = d.RoomMaxAge
	}
	if c.RoomMaxBytes <= 0 {
		c.RoomMaxBytes = d.RoomMaxBytes
	}
	if c.LinkBytesPerSec <= 0 {
		c.LinkBytesPerSec = d.LinkBytesPerSec
	}
	if c.Logf == nil {
		c.Logf = func(string, ...any) {}
	}
}

// Stats are the relay's counters since it started.
type Stats struct {
	Rooms, Links        int
	RoomsOpened, Paired uint64
	NotFound, Refused   uint64
	BytesCopied         uint64
}

type Relay struct {
	cfg Config

	mu      sync.Mutex
	rooms   map[[16]byte]*room
	links   int
	perIP   map[string]int
	newRate map[string]*bucket
	lookups map[string]*bucket
	nextID  uint32

	roomsOpened, paired, notFound, refused, bytesCopied atomic.Uint64
}

type room struct {
	id      [16]byte
	host    net.Conn
	wmu     sync.Mutex // writes on the host's control link
	created time.Time
	bytes   atomic.Int64
	closed  bool               // under Relay.mu
	waiting map[uint32]*waiter // under Relay.mu
	active  int                // paired visitors, under Relay.mu
}

type waiter struct {
	cookie uint32
	attach chan net.Conn // buffered 1
}

type bucket struct {
	tokens float64
	last   time.Time
}

func (b *bucket) take(rate, burst float64, now time.Time) bool {
	if b.last.IsZero() {
		b.tokens = burst
	} else {
		b.tokens += now.Sub(b.last).Seconds() * rate
		if b.tokens > burst {
			b.tokens = burst
		}
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func New(cfg Config) *Relay {
	cfg.fill()
	return &Relay{cfg: cfg, rooms: map[[16]byte]*room{}, perIP: map[string]int{}, newRate: map[string]*bucket{}, lookups: map[string]*bucket{}}
}

func (r *Relay) Stats() Stats {
	r.mu.Lock()
	s := Stats{Rooms: len(r.rooms), Links: r.links}
	r.mu.Unlock()
	s.RoomsOpened, s.Paired, s.NotFound, s.Refused, s.BytesCopied = r.roomsOpened.Load(), r.paired.Load(), r.notFound.Load(), r.refused.Load(), r.bytesCopied.Load()
	return s
}

// Serve accepts links until the listener closes.
func (r *Relay) Serve(ln net.Listener) error {
	stop := make(chan struct{})
	defer close(stop)
	go r.sweep(stop)
	for {
		c, err := ln.Accept()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			return err
		}
		ip := hostOf(c.RemoteAddr())
		if !r.admit(ip) {
			r.refused.Add(1)
			c.Close()
			continue
		}
		go func() {
			defer r.release(ip)
			r.handle(c, ip)
		}()
	}
}

// sweep forgets the rate buckets of addresses that went quiet, so the maps stay small.
func (r *Relay) sweep(stop <-chan struct{}) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		now := time.Now()
		r.mu.Lock()
		for k, b := range r.newRate {
			if now.Sub(b.last) > 2*time.Minute {
				delete(r.newRate, k)
			}
		}
		for k, b := range r.lookups {
			if now.Sub(b.last) > 5*time.Minute {
				delete(r.lookups, k)
			}
		}
		r.mu.Unlock()
	}
}

func hostOf(a net.Addr) string {
	if ta, ok := a.(*net.TCPAddr); ok {
		return ta.IP.String()
	}
	h, _, err := net.SplitHostPort(a.String())
	if err != nil {
		return a.String()
	}
	return h
}

func (r *Relay) admit(ip string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.links >= r.cfg.MaxLinks || r.perIP[ip] >= r.cfg.MaxLinksPerIP {
		return false
	}
	b := r.newRate[ip]
	if b == nil {
		b = &bucket{}
		r.newRate[ip] = b
	}
	if !b.take(r.cfg.NewLinksPerSec, 2*r.cfg.NewLinksPerSec, time.Now()) {
		return false
	}
	r.links++
	r.perIP[ip]++
	return true
}

func (r *Relay) release(ip string) {
	r.mu.Lock()
	r.links--
	if r.perIP[ip]--; r.perIP[ip] <= 0 {
		delete(r.perIP, ip)
	}
	r.mu.Unlock()
}

func msg(typ, code byte, arg, cookie uint32) []byte {
	m := make([]byte, MsgLen)
	binary.LittleEndian.PutUint32(m, Magic)
	m[4], m[5] = typ, code
	binary.LittleEndian.PutUint32(m[8:], arg)
	binary.LittleEndian.PutUint32(m[12:], cookie)
	return m
}

func refuse(c net.Conn, code byte) {
	_ = c.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_, _ = c.Write(msg(MsgErr, code, 0, 0))
	c.Close()
}

func short(id [16]byte) string { return hex.EncodeToString(id[:4]) }

func (r *Relay) handle(c net.Conn, ip string) {
	h := make([]byte, HelloLen)
	_ = c.SetReadDeadline(time.Now().Add(r.cfg.HelloTimeout))
	if _, err := io.ReadFull(c, h); err != nil {
		c.Close()
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	if binary.LittleEndian.Uint32(h) != Magic || h[5] != Version {
		r.refused.Add(1)
		refuse(c, ErrBad)
		return
	}
	var id [16]byte
	copy(id[:], h[8:24])
	vid, cookie := binary.LittleEndian.Uint32(h[24:]), binary.LittleEndian.Uint32(h[28:])
	switch h[4] {
	case KindHost:
		r.host(c, ip, id)
	case KindJoin:
		r.join(c, ip, id)
	case KindAttach:
		r.attach(c, id, vid, cookie)
	default:
		r.refused.Add(1)
		refuse(c, ErrBad)
	}
}

// host: the control link that holds a room open. It answers pings and carries VISITOR / GONE messages.
func (r *Relay) host(c net.Conn, ip string, id [16]byte) {
	r.mu.Lock()
	if old := r.rooms[id]; old != nil {
		r.mu.Unlock()
		r.refused.Add(1)
		refuse(c, ErrTaken)
		return
	}
	if len(r.rooms) >= r.cfg.MaxRooms {
		r.mu.Unlock()
		r.refused.Add(1)
		refuse(c, ErrLimit)
		return
	}
	rm := &room{id: id, host: c, created: time.Now(), waiting: map[uint32]*waiter{}}
	r.rooms[id] = rm
	r.mu.Unlock()
	r.roomsOpened.Add(1)
	r.cfg.Logf("room %s opened by %s", short(id), ip)
	r.send(rm, msg(MsgOK, 0, 0, 0))

	why := "closed by the host"
	m := make([]byte, MsgLen)
	for {
		if time.Since(rm.created) > r.cfg.RoomMaxAge {
			why = "reached its age limit"
			r.send(rm, msg(MsgErr, ErrExpired, 0, 0))
			break
		}
		_ = c.SetReadDeadline(time.Now().Add(r.cfg.HostIdle))
		if _, err := io.ReadFull(c, m); err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				why = "went silent"
			}
			break
		}
		if binary.LittleEndian.Uint32(m) != Magic {
			why = "sent a bad message"
			break
		}
		if m[4] == MsgPing {
			r.send(rm, msg(MsgPong, 0, 0, 0))
		}
	}
	r.mu.Lock()
	rm.closed = true
	if r.rooms[id] == rm {
		delete(r.rooms, id)
	}
	waiting := rm.waiting
	rm.waiting = map[uint32]*waiter{}
	r.mu.Unlock()
	for _, w := range waiting {
		close(w.attach) // the visitor answers ErrNoRoom
	}
	c.Close()
	r.cfg.Logf("room %s %s after %s, %d bytes copied", short(id), why, time.Since(rm.created).Round(time.Second), rm.bytes.Load())
}

func (r *Relay) send(rm *room, m []byte) bool {
	rm.wmu.Lock()
	defer rm.wmu.Unlock()
	_ = rm.host.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err := rm.host.Write(m)
	if err != nil {
		rm.host.Close() // the reading loop ends and closes the room
	}
	return err == nil
}

// join: a visitor asks for a room. It waits for the host's ATTACH link, then the two are copied into each other.
func (r *Relay) join(c net.Conn, ip string, id [16]byte) {
	var cb [4]byte
	_, _ = rand.Read(cb[:])
	cookie := binary.LittleEndian.Uint32(cb[:])

	r.mu.Lock()
	rm := r.rooms[id]
	if rm == nil || rm.closed {
		b := r.lookups[ip]
		if b == nil {
			b = &bucket{}
			r.lookups[ip] = b
		}
		ok := b.take(r.cfg.LookupsPerMin/60, r.cfg.LookupsPerMin, time.Now())
		r.mu.Unlock()
		r.notFound.Add(1)
		if ok {
			refuse(c, ErrNoRoom)
		} else {
			refuse(c, ErrLimit)
		}
		return
	}
	if time.Since(rm.created) > r.cfg.RoomMaxAge || rm.bytes.Load() >= r.cfg.RoomMaxBytes {
		r.mu.Unlock()
		r.refused.Add(1)
		refuse(c, ErrExpired)
		return
	}
	if rm.active+len(rm.waiting) >= r.cfg.MaxVisitors {
		r.mu.Unlock()
		r.refused.Add(1)
		refuse(c, ErrFull)
		return
	}
	r.nextID++
	if r.nextID == 0 {
		r.nextID = 1
	}
	vid := r.nextID
	w := &waiter{cookie: cookie, attach: make(chan net.Conn, 1)}
	rm.waiting[vid] = w
	r.mu.Unlock()

	if !r.send(rm, msg(MsgVisitor, 0, vid, cookie)) {
		r.drop(rm, vid)
		refuse(c, ErrNoRoom)
		return
	}
	t := time.NewTimer(r.cfg.PairTimeout)
	defer t.Stop()
	var hc net.Conn
	select {
	case hc = <-w.attach:
	case <-t.C:
	}
	if hc == nil {
		if r.drop(rm, vid) {
			r.send(rm, msg(MsgGone, 0, vid, 0))
			r.refused.Add(1)
			refuse(c, ErrTimeout)
			return
		}
		// no longer waiting: the host attached just as the timer fired (its link is on the way) or the room closed (the channel is closed)
		if hc = <-w.attach; hc == nil {
			refuse(c, ErrNoRoom)
			return
		}
	}
	_ = c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write(msg(MsgOK, 0, vid, 0)); err != nil {
		c.Close()
		hc.Close()
		r.unpair(rm)
		return
	}
	_ = c.SetWriteDeadline(time.Time{})
	r.paired.Add(1)
	r.cfg.Logf("room %s: visitor %d from %s paired", short(rm.id), vid, ip)
	start := time.Now()
	r.pipe(rm, c, hc)
	r.unpair(rm)
	r.cfg.Logf("room %s: visitor %d left after %s", short(rm.id), vid, time.Since(start).Round(time.Second))
}

// drop removes a waiting visitor; false when it was no longer waiting (paired or the room closed).
func (r *Relay) drop(rm *room, vid uint32) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := rm.waiting[vid]; !ok {
		return false
	}
	delete(rm.waiting, vid)
	return true
}

func (r *Relay) unpair(rm *room) {
	r.mu.Lock()
	rm.active--
	r.mu.Unlock()
}

// attach: the host's link for one waiting visitor. Nothing is written on it: the host expects game frames from the first byte.
func (r *Relay) attach(c net.Conn, id [16]byte, vid, cookie uint32) {
	r.mu.Lock()
	rm := r.rooms[id]
	var w *waiter
	if rm != nil && !rm.closed {
		w = rm.waiting[vid]
	}
	if w == nil || w.cookie != cookie {
		r.mu.Unlock()
		r.refused.Add(1)
		c.Close()
		return
	}
	delete(rm.waiting, vid)
	rm.active++
	r.mu.Unlock()
	w.attach <- c // the visitor's goroutine owns the link from here
}

// pipe copies both ways until either side ends, then closes both.
func (r *Relay) pipe(rm *room, a, b net.Conn) {
	var once sync.Once
	closeBoth := func() { once.Do(func() { a.Close(); b.Close() }) }
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		defer closeBoth()
		buf := make([]byte, 32<<10)
		lim := &bucket{}
		rate, burst := float64(r.cfg.LinkBytesPerSec), float64(2*r.cfg.LinkBytesPerSec)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				for !lim.takeN(float64(n), rate, burst, time.Now()) {
					time.Sleep(10 * time.Millisecond) // back-pressure, nothing is dropped
				}
				if _, werr := dst.Write(buf[:n]); werr != nil {
					return
				}
				r.bytesCopied.Add(uint64(n))
				if rm.bytes.Add(int64(n)) >= r.cfg.RoomMaxBytes {
					r.cfg.Logf("room %s reached its byte limit", short(rm.id))
					return
				}
			}
			if err != nil {
				return
			}
		}
	}
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
}

func (b *bucket) takeN(n, rate, burst float64, now time.Time) bool {
	if b.last.IsZero() {
		b.tokens = burst
	} else {
		b.tokens += now.Sub(b.last).Seconds() * rate
		if b.tokens > burst {
			b.tokens = burst
		}
	}
	b.last = now
	if n > burst {
		n = burst
	}
	if b.tokens < n {
		return false
	}
	b.tokens -= n
	return true
}
