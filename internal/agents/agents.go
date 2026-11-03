// Package agents implements the four bot tiers that supply liquidity, noise,
// trend and informed flow. Agents are plain deterministic code: no I/O, no
// model calls, just a view of the market and a shared seeded RNG.
package agents

import (
	"math"
	"math/rand/v2"

	"github.com/anujkumarsingh-code/tradeforces/internal/book"
)

// OpenOrder is an agent's own resting order.
type OpenOrder struct {
	ID        uint64
	Side      book.Side
	Price     int64
	Remaining int64
	Age       int64
}

// View is everything an agent may look at on one tick. The engine fills only
// the fields a tier is entitled to see: Fair for pros, FairHint for medium
// agents, and nothing but prices for noise traders.
type View struct {
	Now      int64
	BestBid  int64
	BestAsk  int64
	BidQty   int64
	AskQty   int64
	Mid      int64 // cents
	Last     int64 // cents
	Spread   int64
	Hist     []int64 // mid sampled once per simulated second since the open
	Vol      float64 // EWMA std of one-second log returns
	Fair     float64 // dollars; zero unless the agent is a pro
	FairHint float64 // dollars; delayed and noisy; zero unless medium
	NewsAge  int64   // seconds since the last public headline, -1 if none
	NewsSize float64 // absolute fair-value impact of that headline
	Pos      int64
	Open     []OpenOrder
}

// Action is an order placement or a cancel.
type Action struct {
	Cancel bool
	ID     uint64
	Side   book.Side
	Type   book.Type
	Price  int64
	Qty    int64
}

// Agent is one bot.
type Agent interface {
	ID() string
	Tier() string
	Tick(v *View, rng *rand.Rand) []Action
}

func place(side book.Side, typ book.Type, price, qty int64) Action {
	return Action{Side: side, Type: typ, Price: price, Qty: qty}
}

func cancel(id uint64) Action { return Action{Cancel: true, ID: id} }

func clampI(v, lo, hi int64) int64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func pct(a, b int64) float64 {
	if b == 0 {
		return 0
	}
	return float64(a-b) / float64(b)
}

func mean(xs []int64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var s float64
	for _, x := range xs {
		s += float64(x)
	}
	return s / float64(len(xs))
}

func tail(h []int64, n int) []int64 {
	if n > len(h) {
		n = len(h)
	}
	return h[len(h)-n:]
}

func sigmoidish(x, scale float64) float64 { return math.Tanh(x / scale) }
