// coop-relay: the internet-play relay for NX2Apple's local wireless play (DM_COOP_RELAY). See internal/relay and README.md.
package main

import (
	"flag"
	"log"
	"net"
	"time"

	"github.com/exynos6694/NX2Apple-muti/multiplayer/server/internal/relay"
)

func main() {
	d := relay.Defaults()
	listen := flag.String("listen", "127.0.0.1:47610", "address to listen on (TCP); 0.0.0.0:47610 for the internet")
	cfg := relay.Config{}
	flag.IntVar(&cfg.MaxRooms, "rooms", d.MaxRooms, "rooms open at once")
	flag.IntVar(&cfg.MaxVisitors, "visitors", d.MaxVisitors, "visitors per room")
	flag.IntVar(&cfg.MaxLinks, "links", d.MaxLinks, "links at once")
	flag.IntVar(&cfg.MaxLinksPerIP, "links-per-ip", d.MaxLinksPerIP, "links at once from one address")
	flag.DurationVar(&cfg.RoomMaxAge, "room-age", d.RoomMaxAge, "a room's longest life")
	flag.Int64Var(&cfg.RoomMaxBytes, "room-bytes", d.RoomMaxBytes, "bytes a room may carry")
	flag.IntVar(&cfg.LinkBytesPerSec, "link-rate", d.LinkBytesPerSec, "bytes a second per direction of a pair")
	stats := flag.Duration("stats", 10*time.Minute, "log the counters this often (0: never)")
	flag.Parse()
	cfg.Logf = log.Printf

	ln, err := net.Listen("tcp4", *listen)
	if err != nil {
		log.Fatalf("listen %s: %v", *listen, err)
	}
	if ta, ok := ln.Addr().(*net.TCPAddr); ok && !ta.IP.IsLoopback() {
		log.Printf("listening on %s: anyone who can reach it may open rooms. The relay never sees room codes and cannot change game messages, but it can read them (nothing is encrypted)", ln.Addr())
	} else {
		log.Printf("listening on %s", ln.Addr())
	}
	r := relay.New(cfg)
	if *stats > 0 {
		go func() {
			for range time.Tick(*stats) {
				s := r.Stats()
				log.Printf("stats: %d rooms, %d links; %d rooms opened, %d visitors paired, %d lookups not found, %d refused, %d bytes copied",
					s.Rooms, s.Links, s.RoomsOpened, s.Paired, s.NotFound, s.Refused, s.BytesCopied)
			}
		}()
	}
	log.Fatal(r.Serve(ln))
}
