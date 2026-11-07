package main

import (
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/exynos6694/NX2Apple-muti/multiplayer/server/internal/server"
)

func main() {
	httpAddr := env("HTTP_ADDR", ":8080")
	udpAddr := env("UDP_ADDR", ":3479")
	ttl := envDuration("PRESENCE_TTL", 45*time.Second)

	store := server.NewStore(ttl)
	udpConn, err := net.ListenPacket("udp", udpAddr)
	if err != nil {
		log.Fatalf("listen UDP %s: %v", udpAddr, err)
	}
	defer udpConn.Close()

	go server.ServeRendezvousUDP(udpConn, store)

	mux := http.NewServeMux()
	api := server.NewAPI(store)
	api.Register(mux)

	srv := &http.Server{
		Addr:              httpAddr,
		Handler:           server.SecurityHeaders(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("multiplayer directory listening: http=%s udp=%s ttl=%s", httpAddr, udpAddr, ttl)
	log.Fatal(srv.ListenAndServe())
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 5*time.Second {
		log.Fatalf("invalid %s=%q (minimum 5s)", key, v)
	}
	return d
}
