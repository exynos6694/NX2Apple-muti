package relay

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func start(t *testing.T, cfg Config) (*Relay, string) {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var done atomic.Bool // the relay's goroutines may log after the test returns
	cfg.Logf = func(f string, a ...any) {
		if !done.Load() {
			t.Logf(f, a...)
		}
	}
	r := New(cfg)
	go r.Serve(ln)
	t.Cleanup(func() { done.Store(true); ln.Close() })
	return r, ln.Addr().String()
}

func hello(kind byte, room [16]byte, id, cookie uint32) []byte {
	h := make([]byte, HelloLen)
	binary.LittleEndian.PutUint32(h, Magic)
	h[4], h[5] = kind, Version
	copy(h[8:], room[:])
	binary.LittleEndian.PutUint32(h[24:], id)
	binary.LittleEndian.PutUint32(h[28:], cookie)
	return h
}

func dial(t *testing.T, addr string, h []byte) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp4", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if _, err := c.Write(h); err != nil {
		t.Fatal(err)
	}
	return c
}

func read(t *testing.T, c net.Conn) (typ, code byte, arg, cookie uint32) {
	t.Helper()
	m := make([]byte, MsgLen)
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(c, m); err != nil {
		t.Fatalf("reading a relay message: %v", err)
	}
	if binary.LittleEndian.Uint32(m) != Magic {
		t.Fatalf("bad magic %x", m[:4])
	}
	return m[4], m[5], binary.LittleEndian.Uint32(m[8:]), binary.LittleEndian.Uint32(m[12:])
}

func roomID(b byte) (id [16]byte) {
	for i := range id {
		id[i] = b + byte(i)
	}
	return
}

// host opens a room, a visitor joins, the host attaches, bytes flow both ways untouched.
func TestPairAndCopy(t *testing.T) {
	r, addr := start(t, Config{})
	id := roomID(1)
	host := dial(t, addr, hello(KindHost, id, 0, 0))
	if typ, _, _, _ := read(t, host); typ != MsgOK {
		t.Fatalf("host: want OK, got %d", typ)
	}
	ping := make([]byte, MsgLen)
	binary.LittleEndian.PutUint32(ping, Magic)
	ping[4] = MsgPing
	host.Write(ping)
	if typ, _, _, _ := read(t, host); typ != MsgPong {
		t.Fatalf("host: want PONG, got %d", typ)
	}

	vis := dial(t, addr, hello(KindJoin, id, 0, 0))
	typ, _, vid, cookie := read(t, host)
	if typ != MsgVisitor || vid == 0 {
		t.Fatalf("host: want VISITOR, got %d id %d", typ, vid)
	}
	att := dial(t, addr, hello(KindAttach, id, vid, cookie))
	if typ, _, _, _ := read(t, vis); typ != MsgOK {
		t.Fatalf("visitor: want OK, got %d", typ)
	}
	// the station speaks first (its HELLO frame); the host answers; nothing extra may appear on the ATTACH link
	payload := bytes.Repeat([]byte("station->host "), 1000)
	vis.Write(payload)
	got := make([]byte, len(payload))
	_ = att.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(att, got); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("host side got %q... err %v", got[:20], err)
	}
	att.Write([]byte("host->station"))
	got = make([]byte, 13)
	_ = vis.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(vis, got); err != nil || string(got) != "host->station" {
		t.Fatalf("station side got %q err %v", got, err)
	}
	// the visitor leaving closes the host's link for it
	vis.Close()
	_ = att.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := att.Read(make([]byte, 1)); err == nil {
		t.Fatal("the attach link stayed open after the visitor left")
	}
	if s := r.Stats(); s.Paired != 1 || s.BytesCopied != uint64(len(payload)+13) {
		t.Fatalf("stats %+v", s)
	}
}

func TestNoRoomTakenAndBad(t *testing.T) {
	_, addr := start(t, Config{})
	id := roomID(9)
	vis := dial(t, addr, hello(KindJoin, id, 0, 0))
	if typ, code, _, _ := read(t, vis); typ != MsgErr || code != ErrNoRoom {
		t.Fatalf("want ERR NOROOM, got %d/%d", typ, code)
	}
	host := dial(t, addr, hello(KindHost, id, 0, 0))
	read(t, host)
	second := dial(t, addr, hello(KindHost, id, 0, 0))
	if typ, code, _, _ := read(t, second); typ != MsgErr || code != ErrTaken {
		t.Fatalf("want ERR TAKEN, got %d/%d", typ, code)
	}
	bad := hello(KindJoin, id, 0, 0)
	bad[0] = 'X'
	b := dial(t, addr, bad)
	if typ, code, _, _ := read(t, b); typ != MsgErr || code != ErrBad {
		t.Fatalf("want ERR BAD, got %d/%d", typ, code)
	}
	// an ATTACH with a wrong cookie is closed without a word
	vis2 := dial(t, addr, hello(KindJoin, id, 0, 0))
	_, _, vid, cookie := read(t, host)
	wrong := dial(t, addr, hello(KindAttach, id, vid, cookie^1))
	_ = wrong.SetReadDeadline(time.Now().Add(3 * time.Second))
	if n, err := wrong.Read(make([]byte, 16)); err == nil || n != 0 {
		t.Fatalf("wrong cookie: read %d, %v", n, err)
	}
	_ = vis2
}

func TestPairTimeoutAndHostLeaving(t *testing.T) {
	_, addr := start(t, Config{PairTimeout: 300 * time.Millisecond})
	id := roomID(3)
	host := dial(t, addr, hello(KindHost, id, 0, 0))
	read(t, host)
	vis := dial(t, addr, hello(KindJoin, id, 0, 0))
	_, _, vid, _ := read(t, host)
	if typ, code, _, _ := read(t, vis); typ != MsgErr || code != ErrTimeout {
		t.Fatalf("want ERR TIMEOUT, got %d/%d", typ, code)
	}
	if typ, _, gone, _ := read(t, host); typ != MsgGone || gone != vid {
		t.Fatalf("host: want GONE %d, got %d %d", vid, typ, gone)
	}
	// a visitor waiting when the host leaves hears NOROOM
	vis2 := dial(t, addr, hello(KindJoin, id, 0, 0))
	read(t, host)
	host.Close()
	if typ, code, _, _ := read(t, vis2); typ != MsgErr || code != ErrNoRoom {
		t.Fatalf("want ERR NOROOM, got %d/%d", typ, code)
	}
}

func TestFull(t *testing.T) {
	_, addr := start(t, Config{MaxVisitors: 1})
	id := roomID(5)
	host := dial(t, addr, hello(KindHost, id, 0, 0))
	read(t, host)
	dial(t, addr, hello(KindJoin, id, 0, 0))
	read(t, host)
	v2 := dial(t, addr, hello(KindJoin, id, 0, 0))
	if typ, code, _, _ := read(t, v2); typ != MsgErr || code != ErrFull {
		t.Fatalf("want ERR FULL, got %d/%d", typ, code)
	}
}
