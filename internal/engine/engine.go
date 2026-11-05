// Package engine runs one simulated trading session: the order book, the
// hidden fair value, the agent population, server-authoritative accounts and
// the append-only event log. A session is a seed plus the ordered human
// actions; replaying those reproduces it tick for tick.
package engine

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"sync"

	"github.com/anujkumarsingh-code/tradeforces/internal/agents"
	"github.com/anujkumarsingh-code/tradeforces/internal/book"
	"github.com/anujkumarsingh-code/tradeforces/internal/market"
)

// Config sizes a session.
type Config struct {
	Seed           uint64
	ScenarioIndex  int // -1 derives from the seed
	SessionSeconds int64
	CandleSeconds  int64
	Makers         int
	Noise          int
	Medium         int
	Pros           int
	StartCash      float64
	HistoryDays    int
}

// DefaultConfig is the roster the build plan calls for.
func DefaultConfig(seed uint64) Config {
	return Config{
		Seed: seed, ScenarioIndex: -1,
		SessionSeconds: 23400, CandleSeconds: 300,
		Makers: 3, Noise: 120, Medium: 30, Pros: 4,
		StartCash: 100000, HistoryDays: 30,
	}
}

// Trade is one print on the tape.
type Trade struct {
	Time  int64     `json:"t"`
	Price int64     `json:"p"`
	Qty   int64     `json:"q"`
	Side  book.Side `json:"s"` // aggressor side
}

// FillRec is a fill from one player's point of view, enriched with the
// hidden state that scoring needs.
type FillRec struct {
	Time      int64     `json:"t"`
	OrderID   uint64    `json:"id"`
	Side      book.Side `json:"side"`
	Price     int64     `json:"price"`
	Qty       int64     `json:"qty"`
	Fair      float64   `json:"fair"`
	Mid       int64     `json:"mid"`
	Spread    int64     `json:"spread"`
	Aggressor bool      `json:"aggr"`
	PosAfter  int64     `json:"pos_after"`
	Realized  float64   `json:"realized"` // realized on this fill
}

// OrderRec is a player's order-level action.
type OrderRec struct {
	Time  int64     `json:"t"`
	ID    uint64    `json:"id"`
	Kind  string    `json:"kind"` // order, cancel, stop, stop_cancel, stop_fire
	Side  book.Side `json:"side"`
	Type  book.Type `json:"type"`
	Price int64     `json:"price"`
	Qty   int64     `json:"qty"`
}

// Thesis is the pre-trade thesis and confidence a player declared.
type Thesis struct {
	Time       int64  `json:"t"`
	Thesis     string `json:"thesis"`
	Direction  int    `json:"dir"` // +1 bullish, -1 bearish
	Confidence int    `json:"confidence"`
}

// Account is server-authoritative cash and position state.
type Account struct {
	Owner      string
	Name       string
	Tier       string // maker, noise, medium, pro, human
	Human      bool
	Cash       float64
	Pos        int64
	AvgCost    float64 // dollars
	Realized   float64
	StartCash  float64
	Limit      int64 // self-declared position limit
	PeakEquity float64
	MaxDD      float64 // fraction of peak
	FillCount  int

	// Player records for scoring (humans only).
	Fills          []FillRec
	Orders         []OrderRec
	Theses         []Thesis
	Equity         []float64 // sampled every 10 s
	UnprotectedSec int64
	ProtectedSec   int64
	MaxUnprotected int64
	curUnprot      int64
}

// EquityAt marks the account at a price in cents.
func (a *Account) EquityAt(mark int64) float64 {
	return a.Cash + float64(a.Pos)*float64(mark)/100
}

// StopOrder is a human protective order held off-book.
type StopOrder struct {
	ID      uint64    `json:"id"`
	Owner   string    `json:"-"`
	Side    book.Side `json:"side"`
	Trigger int64     `json:"trigger"`
	Qty     int64     `json:"qty"`
	Time    int64     `json:"t"`
}

// FlowBucket is taker volume by tier over one 10-second window.
type FlowBucket struct {
	Time   int64 `json:"t"`
	Maker  int64 `json:"maker"` // makers are passive: volume where a maker was the resting side
	Noise  int64 `json:"noise"`
	Medium int64 `json:"medium"`
	Pro    int64 `json:"pro"`
	Human  int64 `json:"human"`
}

func (f *FlowBucket) add(tier string, qty int64) {
	switch tier {
	case "maker":
		f.Maker += qty
	case "noise":
		f.Noise += qty
	case "medium":
		f.Medium += qty
	case "pro":
		f.Pro += qty
	case "human":
		f.Human += qty
	}
}

// Flow returns up to n closed buckets ending `delay` seconds before now.
func (e *Engine) Flow(n int, delay int64) []FlowBucket {
	cut := e.Now - delay
	end := len(e.FlowHist)
	for end > 0 && e.FlowHist[end-1].Time > cut {
		end--
	}
	start := end - n
	if start < 0 {
		start = 0
	}
	return e.FlowHist[start:end]
}

// LogEntry is one append-only event.
type LogEntry struct {
	Seq  uint64 `json:"seq"`
	Time int64  `json:"t"`
	Kind string `json:"kind"`
	Data any    `json:"data"`
}

// Engine is one session.
type Engine struct {
	mu sync.Mutex

	Cfg      Config
	Scenario market.Scenario
	Events   []market.Event
	Prior    []market.Candle

	rng        *rand.Rand
	Book       *book.Book
	fv         *market.FairValue
	Now        int64
	Closed     bool
	ClosePrice int64

	agentList []agents.Agent
	accounts  map[string]*Account
	humans    []string
	open      map[string]map[uint64]int64 // owner -> order id -> submit time
	stops     map[uint64]*StopOrder
	nextStop  uint64

	hist   []int64   // mid per second
	fvHist []float64 // fair value per second
	// Debug holds one row per second of top-of-book state when enabled.
	Debug     bool
	DebugRows [][6]int64 // now, bid, ask, bidQty, askQty, last
	vol       float64
	prevMid   int64

	Candles []market.Candle
	cur     *market.Candle
	tape    []Trade
	Log     []LogEntry
	seq     uint64

	lastNews     *market.Event
	lastNewsTime int64
	Published    []market.Event

	// Flow by tier: taker volume per tier in the current 10-second bucket,
	// and the history of closed buckets. Served to clients with a delay so
	// it teaches reading the tape without giving away informed flow live.
	flowCur  FlowBucket
	FlowHist []FlowBucket

	// Fills produced during the most recent Step or human action,
	// restricted to humans. The server drains this for notifications.
	humanFills []FillRec
}

// New builds a session from a config. It is deterministic in cfg.Seed.
func New(cfg Config) *Engine {
	rng := rand.New(rand.NewPCG(cfg.Seed, cfg.Seed^0x9E3779B97F4A7C15))
	idx := cfg.ScenarioIndex
	if idx < 0 {
		idx = int(cfg.Seed % uint64(len(market.Library)))
	}
	sc := market.Pick(idx)
	e := &Engine{
		Cfg: cfg, Scenario: sc, rng: rng,
		Book:     book.New(),
		accounts: make(map[string]*Account),
		open:     make(map[string]map[uint64]int64),
		stops:    make(map[uint64]*StopOrder),
		nextStop: 1,
	}
	e.Events = sc.Resolve(rng)
	e.Prior = market.PriorHistory(sc.StartPrice, sc.DailyVol, cfg.HistoryDays, rng)
	e.fv = market.NewFairValue(sc.StartPrice, sc.DailyVol, rng)
	start := int64(math.Round(sc.StartPrice * 100))
	e.Book.LastPrice = start
	e.prevMid = start

	for i := 0; i < cfg.Makers; i++ {
		e.addAgent(agents.NewMarketMaker(i, rng))
	}
	for i := 0; i < cfg.Noise; i++ {
		e.addAgent(agents.NewNoise(i, rng))
	}
	for i := 0; i < cfg.Medium; i++ {
		e.addAgent(agents.NewMedium(i, rng))
	}
	for i := 0; i < cfg.Pros; i++ {
		e.addAgent(agents.NewPro(i, rng))
	}
	// Seed the book so the first prints have something to hit.
	for i := int64(1); i <= 5; i++ {
		e.Book.Submit(&book.Order{Owner: "seed", Side: book.Buy, Type: book.Limit, Price: start - 3*i, Qty: 300}, 0)
		e.Book.Submit(&book.Order{Owner: "seed", Side: book.Sell, Type: book.Limit, Price: start + 3*i, Qty: 300}, 0)
	}
	e.accounts["seed"] = &Account{Owner: "seed"}
	e.cur = &market.Candle{Time: 0, Open: sc.StartPrice, High: sc.StartPrice, Low: sc.StartPrice, Close: sc.StartPrice}
	e.append("session", map[string]any{"seed": cfg.Seed, "scenario": sc.Ticker, "start": sc.StartPrice})
	return e
}

func (e *Engine) addAgent(a agents.Agent) {
	e.agentList = append(e.agentList, a)
	e.accounts[a.ID()] = &Account{Owner: a.ID(), Name: a.ID(), Tier: a.Tier()}
}

func (e *Engine) append(kind string, data any) {
	e.seq++
	e.Log = append(e.Log, LogEntry{Seq: e.seq, Time: e.Now, Kind: kind, Data: data})
}

// Lock exposes the engine mutex for callers that need a consistent snapshot.
func (e *Engine) Lock()   { e.mu.Lock() }
func (e *Engine) Unlock() { e.mu.Unlock() }

// FairValue returns the hidden fair value in dollars. Never send to clients
// during a live session.
func (e *Engine) FairValue() float64 { return e.fv.V }

// FairHistory returns the per-second fair value series.
func (e *Engine) FairHistory() []float64 { return e.fvHist }

// MidHistory returns the per-second mid series.
func (e *Engine) MidHistory() []int64 { return e.hist }

// Vol returns the EWMA per-second return volatility.
func (e *Engine) Vol() float64 { return e.vol }

// Tape returns the last n prints.
func (e *Engine) Tape(n int) []Trade {
	if n > len(e.tape) {
		n = len(e.tape)
	}
	return e.tape[len(e.tape)-n:]
}

// Current returns the in-progress candle.
func (e *Engine) Current() market.Candle { return *e.cur }

// LastNews returns the most recent public headline, or nil.
func (e *Engine) LastNews() *market.Event { return e.lastNews }

// Account returns an account by owner.
func (e *Engine) Account(owner string) (*Account, bool) {
	a, ok := e.accounts[owner]
	return a, ok
}

// Humans returns human owners in join order.
func (e *Engine) Humans() []string { return e.humans }

// Agents returns the bot roster, for display.
func (e *Engine) Agents() []agents.Agent { return e.agentList }

// DrainHumanFills returns and clears fills on human accounts.
func (e *Engine) DrainHumanFills() []FillRec {
	f := e.humanFills
	e.humanFills = nil
	return f
}

// Join adds a human. It is idempotent on owner.
func (e *Engine) Join(owner, name string) *Account {
	e.mu.Lock()
	defer e.mu.Unlock()
	if a, ok := e.accounts[owner]; ok {
		return a
	}
	a := &Account{Owner: owner, Name: name, Tier: "human", Human: true, Cash: e.Cfg.StartCash, StartCash: e.Cfg.StartCash, Limit: 1000, PeakEquity: e.Cfg.StartCash}
	e.accounts[owner] = a
	e.humans = append(e.humans, owner)
	e.append("join", map[string]any{"owner": owner, "name": name})
	return a
}

// SetLimit records the self-declared position limit (pre-market only).
func (e *Engine) SetLimit(owner string, limit int64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.accounts[owner]
	if !ok || !a.Human {
		return errors.New("unknown player")
	}
	if limit < 100 || limit > 5000 {
		return errors.New("limit must be between 100 and 5000 shares")
	}
	a.Limit = limit
	e.append("limit", map[string]any{"owner": owner, "limit": limit})
	return nil
}

// SetThesis records a pre-trade thesis.
func (e *Engine) SetThesis(owner, thesis string, dir, conf int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.accounts[owner]
	if !ok || !a.Human {
		return errors.New("unknown player")
	}
	if conf < 50 || conf > 95 {
		return errors.New("confidence must be between 50 and 95")
	}
	t := Thesis{Time: e.Now, Thesis: thesis, Direction: dir, Confidence: conf}
	a.Theses = append(a.Theses, t)
	e.append("thesis", map[string]any{"owner": owner, "thesis": t})
	return nil
}

var (
	ErrClosed    = errors.New("the market is closed")
	ErrUnknown   = errors.New("unknown player")
	ErrExposure  = errors.New("order would exceed your maximum exposure (2x starting capital)")
	ErrHardLimit = errors.New("order would exceed five times your declared position limit")
	ErrQty       = errors.New("quantity must be between 1 and 10,000")
	ErrPrice     = errors.New("price out of range")
)

// Submit places a human order. Validation is server-side only.
func (e *Engine) Submit(owner string, side book.Side, typ book.Type, price, qty int64) (uint64, []FillRec, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.Closed {
		return 0, nil, ErrClosed
	}
	a, ok := e.accounts[owner]
	if !ok || !a.Human {
		return 0, nil, ErrUnknown
	}
	if qty <= 0 || qty > 10000 {
		return 0, nil, ErrQty
	}
	mid := e.Book.Mid()
	ref := price
	if typ == book.Market {
		ref = mid
	}
	if ref <= 0 || ref < mid/2 || ref > mid*2 {
		return 0, nil, ErrPrice
	}
	after := a.Pos
	if side == book.Buy {
		after += qty
	} else {
		after -= qty
	}
	if abs64(after) > 5*a.Limit {
		return 0, nil, ErrHardLimit
	}
	if float64(abs64(after))*float64(ref)/100 > 2*a.StartCash+a.Realized {
		return 0, nil, ErrExposure
	}
	o := &book.Order{Owner: owner, Side: side, Type: typ, Price: price, Qty: qty}
	fills := e.place(o)
	a.Orders = append(a.Orders, OrderRec{Time: e.Now, ID: o.ID, Kind: "order", Side: side, Type: typ, Price: price, Qty: qty})
	e.append("order", map[string]any{"owner": owner, "id": o.ID, "side": side, "type": typ, "price": price, "qty": qty})
	out := e.DrainHumanFills()
	_ = fills
	return o.ID, out, nil
}

// Cancel removes a human's resting order.
func (e *Engine) Cancel(owner string, id uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	o, ok := e.Book.Get(id)
	if !ok || o.Owner != owner {
		return errors.New("no such order")
	}
	e.Book.Cancel(id)
	delete(e.open[owner], id)
	if a := e.accounts[owner]; a != nil {
		a.Orders = append(a.Orders, OrderRec{Time: e.Now, ID: id, Kind: "cancel", Side: o.Side, Type: o.Type, Price: o.Price, Qty: o.Remaining})
	}
	e.append("cancel", map[string]any{"owner": owner, "id": id})
	return nil
}

// PlaceStop adds a protective stop-market order.
func (e *Engine) PlaceStop(owner string, side book.Side, trigger, qty int64) (uint64, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.Closed {
		return 0, ErrClosed
	}
	a, ok := e.accounts[owner]
	if !ok || !a.Human {
		return 0, ErrUnknown
	}
	if qty <= 0 || qty > 10000 {
		return 0, ErrQty
	}
	last := e.Book.LastPrice
	if trigger <= 0 || (side == book.Sell && trigger >= last) || (side == book.Buy && trigger <= last) {
		return 0, errors.New("a sell stop must be below the last price and a buy stop above it")
	}
	s := &StopOrder{ID: e.nextStop, Owner: owner, Side: side, Trigger: trigger, Qty: qty, Time: e.Now}
	e.nextStop++
	e.stops[s.ID] = s
	a.Orders = append(a.Orders, OrderRec{Time: e.Now, ID: s.ID, Kind: "stop", Side: side, Type: book.Market, Price: trigger, Qty: qty})
	e.append("stop", map[string]any{"owner": owner, "id": s.ID, "side": side, "trigger": trigger, "qty": qty})
	return s.ID, nil
}

// CancelStop removes a stop.
func (e *Engine) CancelStop(owner string, id uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, ok := e.stops[id]
	if !ok || s.Owner != owner {
		return errors.New("no such stop")
	}
	delete(e.stops, id)
	if a := e.accounts[owner]; a != nil {
		a.Orders = append(a.Orders, OrderRec{Time: e.Now, ID: id, Kind: "stop_cancel", Side: s.Side, Price: s.Trigger, Qty: s.Qty})
	}
	e.append("stop_cancel", map[string]any{"owner": owner, "id": id})
	return nil
}

// Stops returns a player's resting stops.
func (e *Engine) Stops(owner string) []StopOrder {
	var out []StopOrder
	for _, s := range e.stops {
		if s.Owner == owner {
			out = append(out, *s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// OpenOrders returns a player's resting book orders.
func (e *Engine) OpenOrders(owner string) []agents.OpenOrder {
	return e.openFor(owner)
}

func (e *Engine) openFor(owner string) []agents.OpenOrder {
	m := e.open[owner]
	if len(m) == 0 {
		return nil
	}
	ids := make([]uint64, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]agents.OpenOrder, 0, len(ids))
	for _, id := range ids {
		o, ok := e.Book.Get(id)
		if !ok {
			delete(m, id)
			continue
		}
		out = append(out, agents.OpenOrder{ID: id, Side: o.Side, Price: o.Price, Remaining: o.Remaining, Age: e.Now - m[id]})
	}
	return out
}

// place submits any order and books the resulting fills.
func (e *Engine) place(o *book.Order) []book.Fill {
	fairBefore := e.fv.V
	midBefore := e.Book.Mid()
	spreadBefore := e.Book.Spread()
	// Market orders carry a price collar, as on real venues: they may walk
	// the book up to 0.5% through the touch and the remainder is cancelled.
	wasMarket := o.Type == book.Market
	if wasMarket {
		ref := int64(0)
		if o.Side == book.Buy {
			ref, _, _ = e.Book.BestAsk()
		} else {
			ref, _, _ = e.Book.BestBid()
		}
		if ref == 0 {
			ref = e.Book.LastPrice
		}
		if ref == 0 {
			return nil
		}
		collar := ref / 200
		if collar < 2 {
			collar = 2
		}
		o.Type = book.Limit
		if o.Side == book.Buy {
			o.Price = ref + collar
		} else {
			o.Price = ref - collar
		}
	}
	fills, err := e.Book.Submit(o, e.Now)
	if err != nil {
		return nil
	}
	if wasMarket {
		if o.Remaining > 0 {
			e.Book.Cancel(o.ID)
		}
		o.Type = book.Market
		o.Price = 0
	}
	if o.Remaining > 0 && o.Type == book.Limit {
		m := e.open[o.Owner]
		if m == nil {
			m = make(map[uint64]int64)
			e.open[o.Owner] = m
		}
		m[o.ID] = e.Now
	}
	for _, f := range fills {
		e.applyFill(f, fairBefore, midBefore, spreadBefore)
	}
	return fills
}

func (e *Engine) applyFill(f book.Fill, fair float64, mid, spread int64) {
	taker := e.accounts[f.Taker]
	maker := e.accounts[f.Maker]
	if taker != nil {
		e.flowCur.add(taker.Tier, f.Qty)
	}
	if maker != nil && maker.Tier == "maker" {
		e.flowCur.add("maker", f.Qty)
	}
	if maker != nil {
		if m := e.open[f.Maker]; m != nil {
			if o, ok := e.Book.Get(f.MakerID); !ok || o.Remaining == 0 {
				delete(m, f.MakerID)
			}
		}
	}
	e.tape = append(e.tape, Trade{Time: f.Time, Price: f.Price, Qty: f.Qty, Side: f.Aggressor})
	if len(e.tape) > 400 {
		e.tape = e.tape[len(e.tape)-400:]
	}
	px := float64(f.Price) / 100
	if e.cur.Volume == 0 && e.cur.Open == 0 {
		e.cur.Open, e.cur.High, e.cur.Low = px, px, px
	}
	e.cur.High = math.Max(e.cur.High, px)
	e.cur.Low = math.Min(e.cur.Low, px)
	e.cur.Close = px
	e.cur.Volume += f.Qty

	book := func(a *Account, side book.Side, id uint64, aggr bool) {
		if a == nil {
			return
		}
		realized := a.apply(side, f.Price, f.Qty)
		if a.Human {
			rec := FillRec{Time: f.Time, OrderID: id, Side: side, Price: f.Price, Qty: f.Qty, Fair: fair, Mid: mid, Spread: spread, Aggressor: aggr, PosAfter: a.Pos, Realized: realized}
			a.Fills = append(a.Fills, rec)
			e.humanFills = append(e.humanFills, rec)
			e.append("fill", map[string]any{"owner": a.Owner, "fill": rec})
		}
	}
	book(taker, f.Aggressor, f.TakerID, true)
	book(maker, f.Aggressor.Opposite(), f.MakerID, false)
}

// apply updates cash, position, average cost and realized P&L. Returns the
// realized P&L of this fill.
func (a *Account) apply(side book.Side, price, qty int64) float64 {
	px := float64(price) / 100
	signed := qty
	if side == book.Sell {
		signed = -qty
	}
	a.FillCount++
	a.Cash -= float64(signed) * px
	var realized float64
	switch {
	case a.Pos == 0 || (a.Pos > 0) == (signed > 0):
		// Opening or adding: blend average cost.
		tot := float64(abs64(a.Pos) + qty)
		a.AvgCost = (a.AvgCost*float64(abs64(a.Pos)) + px*float64(qty)) / tot
		a.Pos += signed
	case abs64(signed) <= abs64(a.Pos):
		// Reducing.
		if a.Pos > 0 {
			realized = (px - a.AvgCost) * float64(qty)
		} else {
			realized = (a.AvgCost - px) * float64(qty)
		}
		a.Pos += signed
		if a.Pos == 0 {
			a.AvgCost = 0
		}
	default:
		// Flipping through zero.
		closeQty := abs64(a.Pos)
		if a.Pos > 0 {
			realized = (px - a.AvgCost) * float64(closeQty)
		} else {
			realized = (a.AvgCost - px) * float64(closeQty)
		}
		a.Pos += signed
		a.AvgCost = px
	}
	a.Realized += realized
	return realized
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// Step advances the session by one simulated second.
func (e *Engine) Step() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.step()
}

// RunHeadless advances to the close without sleeping.
func (e *Engine) RunHeadless() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for !e.Closed {
		e.step()
	}
}

func (e *Engine) step() {
	if e.Closed {
		return
	}
	e.Now++
	e.fv.Step(1)

	// News: pros learn at LeakAt via the fair-value jump; everyone else sees
	// the headline at At.
	for i := range e.Events {
		ev := &e.Events[i]
		if !ev.Leaked && e.Now >= ev.LeakAt {
			ev.Leaked = true
			e.fv.Jump(ev.Impact)
		}
		if !ev.Fired && e.Now >= ev.At {
			ev.Fired = true
			e.lastNews = ev
			e.lastNewsTime = e.Now
			e.Published = append(e.Published, *ev)
			e.append("news", map[string]any{"id": ev.ID, "headline": ev.Headline, "kind": ev.Kind})
		}
	}

	mid := e.Book.Mid()
	if mid == 0 {
		mid = e.Book.LastPrice
	}
	e.hist = append(e.hist, mid)
	e.fvHist = append(e.fvHist, e.fv.V)
	if e.Debug {
		bb, bq, _ := e.Book.BestBid()
		ba, aq, _ := e.Book.BestAsk()
		e.DebugRows = append(e.DebugRows, [6]int64{e.Now, bb, ba, bq, aq, e.Book.LastPrice})
	}
	if e.prevMid > 0 && mid > 0 {
		r := math.Log(float64(mid) / float64(e.prevMid))
		const lambda = 0.995
		e.vol = math.Sqrt(lambda*e.vol*e.vol + (1-lambda)*r*r)
	}
	e.prevMid = mid

	e.fireStops()
	e.tickAgents()
	e.trackProtection()

	if e.Now%10 == 0 {
		e.flowCur.Time = e.Now
		e.FlowHist = append(e.FlowHist, e.flowCur)
		e.flowCur = FlowBucket{}
		mark := e.Book.Mid()
		for _, h := range e.humans {
			a := e.accounts[h]
			eq := a.EquityAt(mark)
			a.Equity = append(a.Equity, eq)
			if eq > a.PeakEquity {
				a.PeakEquity = eq
			}
			if a.PeakEquity > 0 {
				dd := (a.PeakEquity - eq) / a.PeakEquity
				if dd > a.MaxDD {
					a.MaxDD = dd
				}
			}
		}
		e.append("fv", map[string]any{"fv": math.Round(e.fv.V*100) / 100, "mid": mark})
	}

	if e.Now%e.Cfg.CandleSeconds == 0 {
		c := *e.cur
		if c.Open == 0 {
			px := float64(mid) / 100
			c.Open, c.High, c.Low, c.Close = px, px, px, px
		}
		e.Candles = append(e.Candles, c)
		e.append("candle", c)
		e.cur = &market.Candle{Time: e.Now, Open: c.Close, High: c.Close, Low: c.Close, Close: c.Close}
	}

	if e.Now >= e.Cfg.SessionSeconds {
		e.close()
	}
}

func (e *Engine) fireStops() {
	last := e.Book.LastPrice
	if last == 0 || len(e.stops) == 0 {
		return
	}
	ids := make([]uint64, 0, len(e.stops))
	for id := range e.stops {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		s := e.stops[id]
		if (s.Side == book.Sell && last <= s.Trigger) || (s.Side == book.Buy && last >= s.Trigger) {
			delete(e.stops, id)
			a := e.accounts[s.Owner]
			qty := s.Qty
			// A protective stop never opens a new position.
			if a != nil {
				if s.Side == book.Sell && a.Pos < qty {
					qty = a.Pos
				}
				if s.Side == book.Buy && -a.Pos < qty {
					qty = -a.Pos
				}
			}
			if qty <= 0 {
				continue
			}
			o := &book.Order{Owner: s.Owner, Side: s.Side, Type: book.Market, Qty: qty}
			e.place(o)
			if a != nil {
				a.Orders = append(a.Orders, OrderRec{Time: e.Now, ID: s.ID, Kind: "stop_fire", Side: s.Side, Type: book.Market, Price: s.Trigger, Qty: qty})
			}
			e.append("stop_fire", map[string]any{"owner": s.Owner, "id": s.ID, "qty": qty})
		}
	}
}

func (e *Engine) trackProtection() {
	for _, h := range e.humans {
		a := e.accounts[h]
		if a.Pos == 0 {
			a.curUnprot = 0
			continue
		}
		protected := false
		for _, s := range e.stops {
			if s.Owner == h && ((a.Pos > 0 && s.Side == book.Sell) || (a.Pos < 0 && s.Side == book.Buy)) {
				protected = true
				break
			}
		}
		if protected {
			a.ProtectedSec++
			a.curUnprot = 0
		} else {
			a.UnprotectedSec++
			a.curUnprot++
			if a.curUnprot > a.MaxUnprotected {
				a.MaxUnprotected = a.curUnprot
			}
		}
	}
}

func (e *Engine) tickAgents() {
	bb, bq, _ := e.Book.BestBid()
	ba, aq, _ := e.Book.BestAsk()
	base := agents.View{
		Now: e.Now, BestBid: bb, BestAsk: ba, BidQty: bq, AskQty: aq,
		Mid: e.Book.Mid(), Last: e.Book.LastPrice, Spread: e.Book.Spread(),
		Hist: e.hist, Vol: e.vol, NewsAge: -1,
	}
	if e.lastNews != nil {
		base.NewsAge = e.Now - e.lastNewsTime
		base.NewsSize = math.Abs(e.lastNews.Impact)
	}
	// Medium agents see fair value two simulated minutes late, blurred.
	hint := 0.0
	if n := len(e.fvHist); n > 120 {
		hint = e.fvHist[n-121] * (1 + 0.003*e.rng.NormFloat64())
	}
	for _, ag := range e.agentList {
		acct := e.accounts[ag.ID()]
		v := base
		v.Pos = acct.Pos
		v.Open = e.openFor(ag.ID())
		switch ag.Tier() {
		case "pro":
			v.Fair = e.fv.V
		case "medium":
			v.FairHint = hint
		}
		for _, act := range ag.Tick(&v, e.rng) {
			if act.Cancel {
				if o, ok := e.Book.Get(act.ID); ok && o.Owner == ag.ID() {
					e.Book.Cancel(act.ID)
					delete(e.open[ag.ID()], act.ID)
				}
				continue
			}
			if act.Qty <= 0 {
				continue
			}
			o := &book.Order{Owner: ag.ID(), Side: act.Side, Type: act.Type, Price: act.Price, Qty: act.Qty}
			e.place(o)
			// Refresh the live top of book for the next agent in line.
			bb, bq, _ = e.Book.BestBid()
			ba, aq, _ = e.Book.BestAsk()
			base.BestBid, base.BestAsk, base.BidQty, base.AskQty = bb, ba, bq, aq
			base.Mid, base.Last, base.Spread = e.Book.Mid(), e.Book.LastPrice, e.Book.Spread()
		}
	}
}

func (e *Engine) close() {
	e.Closed = true
	// Closing auction: mark at the mid, fall back to the last print.
	e.ClosePrice = e.Book.Mid()
	if e.ClosePrice == 0 {
		e.ClosePrice = e.Book.LastPrice
	}
	if e.cur.Volume > 0 || e.cur.Open != 0 {
		e.Candles = append(e.Candles, *e.cur)
	}
	for _, h := range e.humans {
		a := e.accounts[h]
		eq := a.EquityAt(e.ClosePrice)
		a.Equity = append(a.Equity, eq)
		if eq > a.PeakEquity {
			a.PeakEquity = eq
		}
		if dd := (a.PeakEquity - eq) / a.PeakEquity; dd > a.MaxDD {
			a.MaxDD = dd
		}
	}
	e.append("close", map[string]any{"price": e.ClosePrice})
}

// Describe returns a short roster summary for logs.
func (e *Engine) Describe() string {
	return fmt.Sprintf("%s (%s) seed=%d makers=%d noise=%d medium=%d pros=%d",
		e.Scenario.Ticker, e.Scenario.Company, e.Cfg.Seed, e.Cfg.Makers, e.Cfg.Noise, e.Cfg.Medium, e.Cfg.Pros)
}
