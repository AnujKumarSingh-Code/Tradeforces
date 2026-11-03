package agents

import (
	"fmt"
	"math"
	"math/rand/v2"

	"github.com/anujkumarsingh-code/tradeforces/internal/book"
)

// Pro agents know fair value and see news a few seconds early. They work a
// large parent order in small child slices to hide size, drag price toward
// fair value, and sweep the book when the deviation is a real news gap.
type Pro struct {
	id         string
	threshold  float64
	urgentAt   float64
	maxPos     int64
	parentRem  int64
	parentSide book.Side
	urgent     bool
	nextChild  int64
	nextCheck  int64
	patience   int64
}

// NewPro builds one informed agent.
func NewPro(idx int, rng *rand.Rand) *Pro {
	return &Pro{
		id:        fmt.Sprintf("pro-%d", idx),
		threshold: 0.004 + rng.Float64()*0.006,
		urgentAt:  0.015 + rng.Float64()*0.01,
		maxPos:    100000 + int64(rng.IntN(50000)),
		patience:  int64(6 + rng.IntN(12)),
		nextCheck: int64(rng.IntN(10)),
	}
}

func (p *Pro) ID() string   { return p.id }
func (p *Pro) Tier() string { return "pro" }

func (p *Pro) Tick(v *View, rng *rand.Rand) []Action {
	if v.Mid == 0 || v.Fair == 0 {
		return nil
	}
	var acts []Action
	dev := (v.Fair*100 - float64(v.Mid)) / float64(v.Mid)

	if p.parentRem == 0 {
		if v.Now < p.nextCheck {
			return nil
		}
		p.nextCheck = v.Now + 3 + int64(rng.IntN(5))
		if math.Abs(dev) < p.threshold {
			return nil
		}
		side := book.Buy
		if dev < 0 {
			side = book.Sell
		}
		// Size scales with the edge; clipped to the 5k-50k parent range.
		size := int64(5000 + 45000*math.Min(1, math.Abs(dev)/0.02))
		size += rng.Int64N(3000)
		if size > 50000 {
			size = 50000
		}
		if side == book.Buy && v.Pos+size > p.maxPos {
			size = p.maxPos - v.Pos
		}
		if side == book.Sell && v.Pos-size < -p.maxPos {
			size = p.maxPos + v.Pos
		}
		if size < 500 {
			return nil
		}
		p.parentRem = size
		p.parentSide = side
		p.urgent = math.Abs(dev) >= p.urgentAt
		p.nextChild = v.Now
	}

	// Abort the parent if price has reached fair value.
	if (p.parentSide == book.Buy && dev <= 0) || (p.parentSide == book.Sell && dev >= 0) {
		p.parentRem = 0
		for _, o := range v.Open {
			acts = append(acts, cancel(o.ID))
		}
		return acts
	}
	// Re-evaluate urgency as the gap changes.
	p.urgent = math.Abs(dev) >= p.urgentAt

	if v.Now < p.nextChild {
		return acts
	}
	for _, o := range v.Open {
		if o.Age > 20 {
			acts = append(acts, cancel(o.ID))
		}
	}
	child := 100 + rng.Int64N(901)
	if p.urgent {
		child = 400 + rng.Int64N(601)
	}
	if child > p.parentRem {
		child = p.parentRem
	}
	p.parentRem -= child
	if p.urgent {
		p.nextChild = v.Now + 1 + int64(rng.IntN(2))
		acts = append(acts, place(p.parentSide, book.Market, 0, child))
		return acts
	}
	p.nextChild = v.Now + p.patience + int64(rng.IntN(6))
	price := v.BestBid
	if p.parentSide == book.Sell {
		price = v.BestAsk
	}
	if price == 0 {
		price = v.Mid
	}
	// Mostly join the touch to hide; sometimes cross to keep the schedule.
	if rng.Float64() < 0.3 {
		if p.parentSide == book.Buy {
			price = v.BestAsk
		} else {
			price = v.BestBid
		}
		if price == 0 {
			price = v.Mid
		}
	}
	acts = append(acts, place(p.parentSide, book.Limit, price, child))
	return acts
}
