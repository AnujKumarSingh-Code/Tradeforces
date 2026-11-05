// Command server runs the Tradeforces gateway: the live session loop, the
// WebSocket fan-out and the embedded web client.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/anujkumarsingh-code/tradeforces/internal/server"
)

func main() {
	addr := flag.String("addr", envOr("ADDR", ":8080"), "listen address")
	speed := flag.Int("speed", 20, "simulated seconds per real second (20 = a trading day in ~20 minutes)")
	session := flag.Int64("session", 23400, "simulated seconds per session (23,400 = one 6.5h day)")
	premarket := flag.Duration("premarket", 60*time.Second, "pre-market length")
	results := flag.Duration("results", 90*time.Second, "results screen length between rounds")
	seed := flag.Uint64("seed", 0, "base seed (0 = time-based)")
	data := flag.String("data", "data/sessions", "directory for session event logs (empty to disable)")
	flag.Parse()

	srv := server.New(server.Config{
		Addr: *addr, Speed: *speed, Session: *session, Premarket: *premarket, Results: *results, Seed: *seed, DataDir: *data,
	})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go srv.Run(ctx)

	hs := &http.Server{Addr: *addr, Handler: srv.Handler()}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		hs.Shutdown(shutdown)
	}()
	log.Printf("Tradeforces listening on %s (speed %dx, session %ds, coach key %s)", *addr, *speed, *session, keyState())
	if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func keyState() string {
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		return "set"
	}
	if os.Getenv("COACH") == "on" {
		return "via proxy"
	}
	return "not set, local coach"
}
