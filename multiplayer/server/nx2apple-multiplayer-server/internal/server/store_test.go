package server

import (
	"testing"
	"time"
)

func testPlayer(id string) Player {
	return Player{ClientID: id, Game: Game{TitleID: "0100000000000000", GameVersion: "1.0.0", Protocol: "ldn-v1"}}
}

func TestPresenceGroupingAndExpiry(t *testing.T) {
	s := NewStore(20 * time.Millisecond)
	if err := s.Register(testPlayer("a")); err != nil {
		t.Fatal(err)
	}
	if err := s.Register(testPlayer("b")); err != nil {
		t.Fatal(err)
	}
	other := testPlayer("c")
	other.Game.GameVersion = "2.0.0"
	if err := s.Register(other); err != nil {
		t.Fatal(err)
	}

	got := s.Players("0100000000000000", "1.0.0", "ldn-v1")
	if len(got) != 2 {
		t.Fatalf("want 2 compatible players, got %d", len(got))
	}

	time.Sleep(30 * time.Millisecond)
	if got := s.Players("0100000000000000", "1.0.0", "ldn-v1"); len(got) != 0 {
		t.Fatalf("expired players still present: %d", len(got))
	}
}

func TestSignalsArePrivateToRecipient(t *testing.T) {
	s := NewStore(time.Minute)
	_ = s.Register(testPlayer("a"))
	_ = s.Register(testPlayer("b"))
	item, err := s.AddSignal("a", "b", "candidate", map[string]any{"host": "127.0.0.1:1234"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Signals("b", item.Sequence-1)
	if err != nil || len(got) != 1 || got[0].From != "a" {
		t.Fatalf("unexpected signals: %#v, %v", got, err)
	}
	got, err = s.Signals("a", 0)
	if err != nil || len(got) != 0 {
		t.Fatalf("signal leaked to sender: %#v, %v", got, err)
	}
}

func TestLeaveRemovesQueuedSignals(t *testing.T) {
	s := NewStore(time.Minute)
	_ = s.Register(testPlayer("a"))
	_ = s.Register(testPlayer("b"))
	_, _ = s.AddSignal("a", "b", "offer", nil)
	s.Leave("a")
	got, err := s.Signals("b", 0)
	if err != nil || len(got) != 0 {
		t.Fatalf("stale signal remains: %#v, %v", got, err)
	}
}
