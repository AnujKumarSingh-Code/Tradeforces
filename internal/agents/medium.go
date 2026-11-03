package agents

import (
	"fmt"
	"math/rand/v2"

	"github.com/anujkumarsingh-code/tradeforces/internal/book"
)

// Medium agents see a delayed, noisy fair-value estimate and moving averages.
// They trend-follow and mean-revert with limit orders and carry stop losses
// that cluster at round numbers.
type Medium struct {
	id        string
	next      int64
	trendW    float64 // 1 = pure trend follower, 0 = pure mean reverter
	fast      int
	slow      int
	minSize   int64
	maxSize   int64
	maxPos    int64
	stopPct   float64
	stop      int64 // cents; 0 when flat
	prevPos   int64
	threshold float64
}

// NewMedium builds one medium agent.
func NewMedium(idx int, rng *rand.Rand) *Medium {
	return &Medium{
		id:        fmt.Sprintf("med-%d", idx),
		next:      int64(rng.IntN(20)),
		trendW:    rng.Float64(),
		fast:      20 + rng.IntN(40),
		slow:      120 + rng.IntN(240),
		minSize:   50,
		maxSize:   500,
		maxPos:    1000 + int64(rng.IntN(1500)),
		stopPct:   0.006 + rng.Float64()*0.012,
		threshold: 0.25 + rng.Float64()*0.25,
	}
}

func (m *Medium) ID() string   { return m.id }
func (m *Medium) Tier() string { return "medium" }

// roundStop snaps a stop to the nearest half-dollar on the protective side.
func roundStop(price int64, long bool) int64 {
	if long {
		return (price / 50) * 50
	}
	return ((price + 49) / 50) * 50
}

func (m *Medium) Tick(v *View, rng *rand.Rand) []Action {
	if v.Mid == 0 {
		return nil
	}
	var acts []Action

	// Stops are evaluated every tick, not just on wake-ups.
	if m.prevPos == 0 && v.Pos != 0 {
		if v.Pos > 0 {
			m.stop = roundStop(int64(float64(v.Last)*(1-m.stopPct)), true)
		} else {
			m.stop = roundStop(int64(float64(v.Last)*(1+m.stopPct)), false)
		}
	}
	if v.Pos == 0 {
		m.stop = 0
	}
	m.prevPos = v.Pos
	if m.stop > 0 && v.Last > 0 {
		if v.Pos > 0 && v.Last <= m.stop {
			for _, o := range v.Open {
				acts = append(acts, cancel(o.ID))
			}
			acts = append(acts, place(book.Sell, book.Market, 0, v.Pos))
			m.stop = 0
			m.next = v.Now + 30 + int64(rng.IntN(60)) // licks its wounds
			return acts
		}
		if v.Pos < 0 && v.Last >= m.stop {
			for _, o := range v.Open {
				acts = append(acts, cancel(o.ID))
			}
			acts = append(acts, place(book.Buy, book.Market, 0, -v.Pos))
			m.stop = 0
			m.next = v.Now + 30 + int64(rng.IntN(60))
			return acts
		}
	}

	if v.Now < m.next {
		return acts
	}
	m.next = v.Now + 5 + int64(rng.IntN(16))

	for _, o := range v.Open {
		if o.Age > 45 {
			acts = append(acts, cancel(o.ID))
		}
	}
	h := v.Hist
	if len(h) < m.slow+1 {
		return acts
	}
	fast := mean(tail(h, m.fast))
	slow := mean(tail(h, m.slow))
	trend := sigmoidish((fast-slow)/slow, 0.002)
	revert := 0.0
	if v.FairHint > 0 {
		revert = sigmoidish((v.FairHint*100-float64(v.Mid))/float64(v.Mid), 0.004)
	}
	score := m.trendW*trend + (1-m.trendW)*revert
	if score > -m.threshold && score < m.threshold {
		return acts
	}
	side := book.Buy
	if score < 0 {
		side = book.Sell
	}
	qty := m.minSize + rng.Int64N(m.maxSize-m.minSize+1)
	// Flip: close an opposing position first.
	if (side == book.Buy && v.Pos < 0) || (side == book.Sell && v.Pos > 0) {
		closeQty := v.Pos
		if closeQty < 0 {
			closeQty = -closeQty
		}
		acts = append(acts, place(side, book.Market, 0, closeQty))
		return acts
	}
	if (side == book.Buy && v.Pos+qty > m.maxPos) || (side == book.Sell && v.Pos-qty < -m.maxPos) {
		return acts
	}
	price := v.BestBid
	if side == book.Sell {
		price = v.BestAsk
	}
	if price == 0 {
		price = v.Mid
	}
	// Occasionally step inside the spread to get done faster.
	if rng.Float64() < 0.3 && v.Spread > 2 {
		if side == book.Buy {
			price++
		} else {
			price--
		}
	}
	acts = append(acts, place(side, book.Limit, price, qty))
	return acts
}
