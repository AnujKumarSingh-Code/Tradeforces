package agents

import (
	"fmt"
	"math"
	"math/rand/v2"

	"github.com/anujkumarsingh-code/tradeforces/internal/book"
)

// MarketMaker quotes both sides around a smoothed trade price, widens with
// volatility, skews to unwind inventory and pulls its quotes on shock news.
// It always quotes both sides: liquidity thins under stress, it does not
// vanish unless news hits.
type MarketMaker struct {
	id            string
	baseHalf      int64   // cents of half-spread in calm conditions
	volMult       float64 // extra half-spread per unit of vol*price
	levels        int
	step          int64 // cents between levels
	minSize       int64
	maxSize       int64
	invScale      int64 // inventory at which skew saturates
	requoteEvery  int64
	ref           float64 // EWMA of trade prices, cents
	lastCenter    float64
	lastQuote     int64
	pulledUntil   int64
	widenUntil    int64
	lastOpenCount int
}

// NewMarketMaker builds a maker. idx varies the personality slightly.
func NewMarketMaker(idx int, rng *rand.Rand) *MarketMaker {
	return &MarketMaker{
		id:           fmt.Sprintf("mm-%d", idx),
		baseHalf:     int64(2 + rng.IntN(3)),
		volMult:      1.0 + rng.Float64()*0.8,
		levels:       5,
		step:         int64(2 + rng.IntN(3)),
		minSize:      100,
		maxSize:      500,
		invScale:     8000 + int64(rng.IntN(4000)),
		requoteEvery: int64(3 + rng.IntN(4)),
		lastQuote:    -1000,
	}
}

func (m *MarketMaker) ID() string   { return m.id }
func (m *MarketMaker) Tier() string { return "maker" }

func (m *MarketMaker) Tick(v *View, rng *rand.Rand) []Action {
	if v.Mid == 0 {
		return nil
	}
	// Reference price: a smoothed trade price so a single aggressive order
	// moves quotes by a fraction of the spread, not the whole spread.
	if m.ref == 0 {
		m.ref = float64(v.Mid)
	}
	if v.Last > 0 {
		m.ref += 0.35 * (float64(v.Last) - m.ref)
	}

	var acts []Action
	cancelAll := func() {
		for _, o := range v.Open {
			acts = append(acts, cancel(o.ID))
		}
	}

	// Shock news: pull quotes entirely for a few seconds, then quote wide.
	if v.NewsAge >= 0 && v.NewsAge <= 1 && v.NewsSize >= 0.01 {
		m.pulledUntil = v.Now + 30 + int64(rng.IntN(60))
		m.widenUntil = m.pulledUntil + 240
		cancelAll()
		return acts
	}
	if v.Now < m.pulledUntil {
		if len(v.Open) > 0 {
			cancelAll()
		}
		return acts
	}

	price := m.ref
	vol := math.Min(v.Vol, 0.002)
	half := float64(m.baseHalf) + m.volMult*vol*price*6
	if v.Now < m.widenUntil {
		rem := float64(m.widenUntil-v.Now) / 240.0
		half *= 1 + 3*rem
	}
	half = math.Max(1, math.Min(half, price*0.006))

	// Inventory skew, bounded in absolute terms so it cannot ratchet.
	inv := float64(v.Pos) / float64(m.invScale)
	inv = math.Max(-1.5, math.Min(1.5, inv))
	skew := math.Max(-price*0.004, math.Min(price*0.004, -inv*half*2))
	center := price + skew

	moved := math.Abs(center-m.lastCenter) >= float64(m.baseHalf)
	stale := v.Now-m.lastQuote >= m.requoteEvery
	filled := len(v.Open) < m.lastOpenCount
	if !moved && !stale && !filled {
		return nil
	}
	cancelAll()

	// Shrink the side that would grow inventory, grow the side that unwinds.
	buyMult, sellMult := 1.0, 1.0
	if inv > 0 {
		buyMult = math.Max(0.2, 1-inv)
		sellMult = 1 + inv
	} else {
		sellMult = math.Max(0.2, 1+inv)
		buyMult = 1 - inv
	}
	for i := 0; i < m.levels; i++ {
		off := half + float64(i)*float64(m.step)
		bid := int64(math.Floor(center - off))
		ask := int64(math.Ceil(center + off))
		if ask <= bid {
			ask = bid + 1
		}
		size := m.minSize + rng.Int64N(m.maxSize-m.minSize+1)
		if i >= 2 {
			size *= 2
		}
		acts = append(acts, place(book.Buy, book.Limit, bid, int64(float64(size)*buyMult)))
		acts = append(acts, place(book.Sell, book.Limit, ask, int64(float64(size)*sellMult)))
	}
	m.lastCenter = center
	m.lastQuote = v.Now
	m.lastOpenCount = 2 * m.levels
	return acts
}
