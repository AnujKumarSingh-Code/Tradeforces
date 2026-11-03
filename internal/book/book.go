// Package book implements a deterministic, in-memory limit order book with
// price-time priority. Prices are integer cents, quantities are whole shares.
// Nothing in here does I/O; it is the hot loop of the simulation.
package book

import (
	"errors"
	"sort"
)

// Side of an order.
type Side int8

const (
	Buy Side = iota
	Sell
)

func (s Side) String() string {
	if s == Buy {
		return "buy"
	}
	return "sell"
}

// Opposite returns the other side.
func (s Side) Opposite() Side {
	if s == Buy {
		return Sell
	}
	return Buy
}

// Type of an order.
type Type int8

const (
	Limit Type = iota
	Market
)

func (t Type) String() string {
	if t == Limit {
		return "limit"
	}
	return "market"
}

// Order is a resting or incoming order.
type Order struct {
	ID        uint64
	Owner     string
	Side      Side
	Type      Type
	Price     int64 // cents; ignored for market orders
	Qty       int64
	Remaining int64
	Time      int64 // simulated seconds at submission
	seq       uint64
}

// Fill records a match between a taker and a resting maker order.
type Fill struct {
	Price     int64
	Qty       int64
	TakerID   uint64
	MakerID   uint64
	Taker     string
	Maker     string
	Aggressor Side // side of the taker
	Time      int64
}

// Level aggregates resting orders at one price.
type Level struct {
	Price  int64
	Qty    int64
	orders []*Order
}

// LevelView is a read-only depth row.
type LevelView struct {
	Price int64 `json:"p"`
	Qty   int64 `json:"q"`
	Count int   `json:"n"`
}

// Book is a single-instrument limit order book.
type Book struct {
	bids   []*Level // descending price
	asks   []*Level // ascending price
	orders map[uint64]*Order
	nextID uint64
	seq    uint64

	// LastPrice is the price of the most recent trade (0 before any).
	LastPrice int64
	// Volume is cumulative traded shares.
	Volume int64
}

// New returns an empty book.
func New() *Book {
	return &Book{orders: make(map[uint64]*Order), nextID: 1}
}

var (
	ErrBadQty   = errors.New("quantity must be positive")
	ErrBadPrice = errors.New("limit price must be positive")
)

// Submit matches an incoming order against the book and rests any remainder
// (limit orders only). It assigns an ID when the order has none. The returned
// fills are in execution order.
func (b *Book) Submit(o *Order, now int64) ([]Fill, error) {
	if o.Qty <= 0 {
		return nil, ErrBadQty
	}
	if o.Type == Limit && o.Price <= 0 {
		return nil, ErrBadPrice
	}
	if o.ID == 0 {
		o.ID = b.nextID
		b.nextID++
	} else if o.ID >= b.nextID {
		b.nextID = o.ID + 1
	}
	b.seq++
	o.seq = b.seq
	o.Remaining = o.Qty
	o.Time = now

	var fills []Fill
	if o.Side == Buy {
		for o.Remaining > 0 && len(b.asks) > 0 {
			lvl := b.asks[0]
			if o.Type == Limit && lvl.Price > o.Price {
				break
			}
			fills = b.matchLevel(o, lvl, now, fills)
			if lvl.Qty == 0 {
				b.asks = b.asks[1:]
			}
		}
	} else {
		for o.Remaining > 0 && len(b.bids) > 0 {
			lvl := b.bids[0]
			if o.Type == Limit && lvl.Price < o.Price {
				break
			}
			fills = b.matchLevel(o, lvl, now, fills)
			if lvl.Qty == 0 {
				b.bids = b.bids[1:]
			}
		}
	}
	if o.Remaining > 0 && o.Type == Limit {
		b.rest(o)
	}
	return fills, nil
}

func (b *Book) matchLevel(taker *Order, lvl *Level, now int64, fills []Fill) []Fill {
	for taker.Remaining > 0 && len(lvl.orders) > 0 {
		maker := lvl.orders[0]
		q := taker.Remaining
		if maker.Remaining < q {
			q = maker.Remaining
		}
		taker.Remaining -= q
		maker.Remaining -= q
		lvl.Qty -= q
		b.LastPrice = lvl.Price
		b.Volume += q
		fills = append(fills, Fill{
			Price: lvl.Price, Qty: q,
			TakerID: taker.ID, MakerID: maker.ID,
			Taker: taker.Owner, Maker: maker.Owner,
			Aggressor: taker.Side, Time: now,
		})
		if maker.Remaining == 0 {
			lvl.orders = lvl.orders[1:]
			delete(b.orders, maker.ID)
		}
	}
	return fills
}

func (b *Book) rest(o *Order) {
	b.orders[o.ID] = o
	if o.Side == Buy {
		i := sort.Search(len(b.bids), func(i int) bool { return b.bids[i].Price <= o.Price })
		if i < len(b.bids) && b.bids[i].Price == o.Price {
			b.bids[i].orders = append(b.bids[i].orders, o)
			b.bids[i].Qty += o.Remaining
			return
		}
		lvl := &Level{Price: o.Price, Qty: o.Remaining, orders: []*Order{o}}
		b.bids = append(b.bids, nil)
		copy(b.bids[i+1:], b.bids[i:])
		b.bids[i] = lvl
		return
	}
	i := sort.Search(len(b.asks), func(i int) bool { return b.asks[i].Price >= o.Price })
	if i < len(b.asks) && b.asks[i].Price == o.Price {
		b.asks[i].orders = append(b.asks[i].orders, o)
		b.asks[i].Qty += o.Remaining
		return
	}
	lvl := &Level{Price: o.Price, Qty: o.Remaining, orders: []*Order{o}}
	b.asks = append(b.asks, nil)
	copy(b.asks[i+1:], b.asks[i:])
	b.asks[i] = lvl
}

// Cancel removes a resting order. It returns the cancelled order and true, or
// nil and false when no such order rests.
func (b *Book) Cancel(id uint64) (*Order, bool) {
	o, ok := b.orders[id]
	if !ok {
		return nil, false
	}
	delete(b.orders, id)
	side := &b.asks
	if o.Side == Buy {
		side = &b.bids
	}
	for i, lvl := range *side {
		if lvl.Price != o.Price {
			continue
		}
		for j, r := range lvl.orders {
			if r.ID == id {
				lvl.orders = append(lvl.orders[:j], lvl.orders[j+1:]...)
				lvl.Qty -= r.Remaining
				break
			}
		}
		if lvl.Qty == 0 {
			*side = append((*side)[:i], (*side)[i+1:]...)
		}
		break
	}
	return o, true
}

// Get returns a resting order by ID.
func (b *Book) Get(id uint64) (*Order, bool) {
	o, ok := b.orders[id]
	return o, ok
}

// BestBid returns the top-of-book bid.
func (b *Book) BestBid() (price, qty int64, ok bool) {
	if len(b.bids) == 0 {
		return 0, 0, false
	}
	return b.bids[0].Price, b.bids[0].Qty, true
}

// BestAsk returns the top-of-book ask.
func (b *Book) BestAsk() (price, qty int64, ok bool) {
	if len(b.asks) == 0 {
		return 0, 0, false
	}
	return b.asks[0].Price, b.asks[0].Qty, true
}

// Mid returns the midpoint in cents, falling back to the last trade when one
// side is empty.
func (b *Book) Mid() int64 {
	bp, _, bok := b.BestBid()
	ap, _, aok := b.BestAsk()
	switch {
	case bok && aok:
		return (bp + ap) / 2
	case b.LastPrice > 0:
		// One-sided book: the last print is a better reference than the
		// surviving side, which may be far away.
		return b.LastPrice
	case bok:
		return bp
	case aok:
		return ap
	default:
		return 0
	}
}

// Spread returns ask minus bid in cents, or 0 when one side is empty.
func (b *Book) Spread() int64 {
	bp, _, bok := b.BestBid()
	ap, _, aok := b.BestAsk()
	if !bok || !aok {
		return 0
	}
	return ap - bp
}

// Depth returns up to n levels per side.
func (b *Book) Depth(n int) (bids, asks []LevelView) {
	for i := 0; i < len(b.bids) && i < n; i++ {
		l := b.bids[i]
		bids = append(bids, LevelView{l.Price, l.Qty, len(l.orders)})
	}
	for i := 0; i < len(b.asks) && i < n; i++ {
		l := b.asks[i]
		asks = append(asks, LevelView{l.Price, l.Qty, len(l.orders)})
	}
	return
}

// Resting returns the number of resting orders.
func (b *Book) Resting() int { return len(b.orders) }

// OrdersBy returns the resting orders of one owner.
func (b *Book) OrdersBy(owner string) []*Order {
	var out []*Order
	for _, o := range b.orders {
		if o.Owner == owner {
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].seq < out[j].seq })
	return out
}
