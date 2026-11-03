package agents

import (
	"fmt"
	"math"
	"math/rand/v2"

	"github.com/anujkumarsingh-code/tradeforces/internal/book"
)

// Noise traders only see the last few prices. They chase green candles,
// panic-sell red ones, cross the spread with market orders and never set a
// stop. This is the flow everyone else profits from.
type Noise struct {
	id       string
	next     int64
	look     int     // seconds of history they react to
	chase    float64 // how strongly momentum tilts them
	activity float64 // probability of trading on a wake-up
	maxPos   int64
}

// NewNoise builds one noise trader.
func NewNoise(idx int, rng *rand.Rand) *Noise {
	return &Noise{
		id:       fmt.Sprintf("noise-%d", idx),
		next:     int64(rng.IntN(5)),
		look:     5 + rng.IntN(40),
		chase:    0.12 + rng.Float64()*0.18,
		activity: 0.3 + rng.Float64()*0.3,
		maxPos:   100 + int64(rng.IntN(300)),
	}
}

func (n *Noise) ID() string   { return n.id }
func (n *Noise) Tier() string { return "noise" }

func (n *Noise) Tick(v *View, rng *rand.Rand) []Action {
	if v.Now < n.next || v.Mid == 0 {
		return nil
	}
	n.next = v.Now + 1 + int64(rng.IntN(5))
	h := v.Hist
	if len(h) < n.look+1 {
		return nil
	}
	mom := pct(h[len(h)-1], h[len(h)-1-n.look])
	// A moving tape pulls the crowd in: activity rises with the recent range.
	w := tail(h, 240)
	lo, hi := w[0], w[0]
	for _, p := range w {
		if p < lo {
			lo = p
		}
		if p > hi {
			hi = p
		}
	}
	rng60 := float64(hi-lo) / float64(lo)
	excite := 0.8 + 2.2*math.Min(1, rng60/0.02)
	if rng.Float64() > n.activity*excite {
		return nil
	}
	// Momentum chase. Red moves scare harder than green moves attract.
	scale := 0.006
	if mom < 0 {
		scale = 0.0045
	}
	pBuy := 0.5 + n.chase*sigmoidish(mom, scale)
	// Soft inventory bound so one trader cannot drift to infinity.
	if v.Pos > n.maxPos {
		pBuy -= 0.25
	} else if v.Pos < -n.maxPos {
		pBuy += 0.25
	}
	qty := 1 + rng.Int64N(20)
	if rng.Float64() < pBuy {
		return []Action{place(book.Buy, book.Market, 0, qty)}
	}
	return []Action{place(book.Sell, book.Market, 0, qty)}
}
