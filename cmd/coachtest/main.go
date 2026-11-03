// Command coachtest runs a headless session with a scripted human who
// trades badly on purpose, scores it, and runs the two-stage coach. With
// ANTHROPIC_API_KEY set it exercises the real Claude calls and prints the
// tags, the narrative and the estimated cost; without it, the local fallback.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/anujkumarsingh-code/tradeforces/internal/book"
	"github.com/anujkumarsingh-code/tradeforces/internal/coach"
	"github.com/anujkumarsingh-code/tradeforces/internal/engine"
	"github.com/anujkumarsingh-code/tradeforces/internal/scoring"
)

func main() {
	seed := flag.Uint64("seed", 44, "session seed")
	flag.Parse()

	cfg := engine.DefaultConfig(*seed)
	e := engine.New(cfg)
	owner := "tester"
	acct := e.Join(owner, "Tester")
	e.SetLimit(owner, 500)

	// Script: a trader with most of the taxonomy's habits.
	step := func(n int) {
		for i := 0; i < n; i++ {
			e.Step()
		}
	}
	must := func(_ uint64, _ []engine.FillRec, err error) {
		if err != nil {
			fmt.Fprintln(os.Stderr, "order rejected:", err)
		}
	}
	step(1200)
	e.SetThesis(owner, "Ride the trend", 1, 80)
	must(e.Submit(owner, book.Buy, book.Market, 0, 400)) // market in, no stop
	step(800)
	must(e.Submit(owner, book.Buy, book.Market, 0, 400))  // adds (maybe underwater), now oversized vs 500
	step(3000)                                            // held through the first headline with no stop
	must(e.Submit(owner, book.Sell, book.Market, 0, 800)) // flatten
	step(300)
	must(e.Submit(owner, book.Sell, book.Market, 0, 900)) // quick re-entry, bigger: revenge if the last one lost
	step(600)
	must(e.Submit(owner, book.Buy, book.Market, 0, 900))
	step(2000)
	// One disciplined trade: passive entry with a stop.
	bid, _, _ := e.Book.BestBid()
	e.SetThesis(owner, "Fade the panic", 1, 60)
	must(e.Submit(owner, book.Buy, book.Limit, bid, 200))
	step(200)
	if acct.Pos > 0 {
		e.PlaceStop(owner, book.Sell, e.Book.LastPrice-e.Book.LastPrice/100, acct.Pos)
	}
	step(4000)
	if acct.Pos != 0 {
		side := book.Sell
		if acct.Pos < 0 {
			side = book.Buy
		}
		q := acct.Pos
		if q < 0 {
			q = -q
		}
		must(e.Submit(owner, side, book.Market, 0, q))
	}
	e.RunHeadless()

	var news []scoring.NewsAt
	var heads []string
	for _, ev := range e.Published {
		news = append(news, scoring.NewsAt{At: ev.At, Headline: ev.Headline})
		heads = append(heads, ev.Headline)
	}
	res := scoring.Score(acct, e.ClosePrice, news)
	fmt.Printf("== %s\n", e.Describe())
	fmt.Printf("score %.1f (return %.0f / risk %.0f / process %.0f)  P&L %+.2f%%  DD %.2f%%  episodes %d  fills %d\n",
		res.Score, res.ReturnScore, res.RiskScore, res.ProcessScore, res.ReturnPct, res.MaxDrawdown, res.Episodes, res.Fills)
	for _, m := range res.Mistakes {
		fmt.Printf("  - %-14s x%d  %s\n", m.Tag, m.Count, m.Detail)
	}
	if res.Calibration != nil {
		fmt.Printf("  calibration: claimed %.0f%%, hit %.0f%%: %s\n", res.Calibration.AvgClaimed, res.Calibration.HitRate, res.Calibration.Description)
	}

	c := coach.New()
	fmt.Printf("\ncoach enabled: %v (models %s)\n", c.Enabled(), os.Getenv("COACH_TAG_MODEL")+" "+os.Getenv("COACH_MODEL"))
	t0 := time.Now()
	rev := c.Review(context.Background(), coach.Input{
		Player: "Tester", Ticker: e.Scenario.Ticker, Company: e.Scenario.Company, Brief: e.Scenario.Brief,
		Result: res, News: heads, ClosePx: float64(e.ClosePrice) / 100, StartPx: e.Scenario.StartPrice, FairEnd: e.FairValue(),
	})
	fmt.Printf("source=%s models=%v cost=$%.4f elapsed=%s\n", rev.Source, rev.Models, rev.CostUSD, time.Since(t0).Round(time.Millisecond))
	if rev.Error != "" {
		fmt.Println("ERROR:", rev.Error)
	}
	if len(rev.Tags) > 0 {
		b, _ := json.MarshalIndent(rev.Tags, "", "  ")
		fmt.Printf("\n-- tags --\n%s\n", b)
	}
	fmt.Printf("\n-- narrative --\n%s\n", rev.Narrative)
	if rev.Source != "claude" && c.Enabled() {
		os.Exit(1)
	}
}
