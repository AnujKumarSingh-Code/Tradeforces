// Package server is the gateway: auth, order validation, the session
// lifecycle (lobby, pre-market, live, close, results) and WebSocket fan-out
// of book deltas, trades, news and account state.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anujkumarsingh-code/tradeforces/internal/book"
	"github.com/anujkumarsingh-code/tradeforces/internal/coach"
	"github.com/anujkumarsingh-code/tradeforces/internal/engine"
	"github.com/anujkumarsingh-code/tradeforces/internal/market"
	"github.com/anujkumarsingh-code/tradeforces/internal/scoring"
	"github.com/anujkumarsingh-code/tradeforces/web"
)

// Config for the gateway.
type Config struct {
	Addr      string
	Speed     int // simulated seconds per real second
	Premarket time.Duration
	Results   time.Duration
	Seed      uint64
	DataDir   string
	Session   int64 // simulated seconds per session
}

// Player is an authenticated human.
type Player struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Token string `json:"-"`
}

type reviewEntry struct {
	Status string          `json:"status"` // pending, ready
	Player string          `json:"player"`
	Result *scoring.Result `json:"result,omitempty"`
	Review *coach.Review   `json:"review,omitempty"`
}

type sessionSummary struct {
	ID       string    `json:"id"`
	Ticker   string    `json:"ticker"`
	Company  string    `json:"company"`
	Seed     uint64    `json:"seed"`
	Start    float64   `json:"start"`
	Close    float64   `json:"close"`
	Fair     float64   `json:"fair"`
	EndedAt  time.Time `json:"ended_at"`
	Players  int       `json:"players"`
	Winner   string    `json:"winner,omitempty"`
	WinnerRt float64   `json:"winner_return,omitempty"`
}

// Server is the gateway.
type Server struct {
	cfg   Config
	mu    sync.Mutex
	hub   *hub
	coach *coach.Coach

	players  map[string]*Player // by token
	byID     map[string]*Player
	eng      *engine.Engine
	sessID   string
	sessNo   int
	phase    string
	phaseEnd time.Time
	seed     uint64

	reviews  map[string]map[string]*reviewEntry
	logs     map[string][]engine.LogEntry
	history  []sessionSummary
	lastSess string
}

// New builds a server; call Run to start the lifecycle and ListenAndServe.
func New(cfg Config) *Server {
	if cfg.Speed <= 0 {
		cfg.Speed = 20
	}
	if cfg.Session <= 0 {
		cfg.Session = 23400
	}
	s := &Server{
		cfg: cfg, hub: newHub(), coach: coach.New(),
		players: map[string]*Player{}, byID: map[string]*Player{},
		reviews: map[string]map[string]*reviewEntry{}, logs: map[string][]engine.LogEntry{},
		seed: cfg.Seed,
	}
	if s.seed == 0 {
		s.seed = uint64(time.Now().UnixNano() % 1_000_000)
	}
	return s
}

// Handler returns the HTTP mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/join", s.handleJoin)
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("POST /api/order", s.handleOrder)
	mux.HandleFunc("POST /api/cancel", s.handleCancel)
	mux.HandleFunc("POST /api/stop", s.handleStop)
	mux.HandleFunc("POST /api/stop/cancel", s.handleStopCancel)
	mux.HandleFunc("POST /api/limit", s.handleLimit)
	mux.HandleFunc("POST /api/thesis", s.handleThesis)
	mux.HandleFunc("GET /api/review", s.handleReview)
	mux.HandleFunc("GET /api/history", s.handleHistory)
	mux.HandleFunc("GET /api/replay", s.handleReplay)
	mux.HandleFunc("GET /ws", s.handleWS)
	sub, _ := fs.Sub(web.FS, ".")
	fileServer := http.FileServer(http.FS(sub))
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Header().Set("Cache-Control", "no-store")
		}
		fileServer.ServeHTTP(w, r)
	})
	return mux
}

// Run drives the session lifecycle forever.
func (s *Server) Run(ctx context.Context) {
	for ctx.Err() == nil {
		s.newSession()
		s.runPhase(ctx, "premarket", s.cfg.Premarket)
		s.runLive(ctx)
		s.closeSession()
		s.runPhase(ctx, "closed", s.cfg.Results)
	}
}

func (s *Server) newSession() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessNo++
	cfg := engine.DefaultConfig(s.seed + uint64(s.sessNo))
	cfg.SessionSeconds = s.cfg.Session
	s.eng = engine.New(cfg)
	s.sessID = fmt.Sprintf("s%d-%d", cfg.Seed, s.sessNo)
	s.phase = "premarket"
	s.phaseEnd = time.Now().Add(s.cfg.Premarket)
	log.Printf("session %s: %s", s.sessID, s.eng.Describe())
	// Everyone connected is in the next round automatically.
	for _, c := range s.hub.clients() {
		if c.player != nil {
			s.eng.Join(c.player.ID, c.player.Name)
		}
	}
}

func (s *Server) runPhase(ctx context.Context, phase string, d time.Duration) {
	s.mu.Lock()
	s.phase = phase
	s.phaseEnd = time.Now().Add(d)
	s.mu.Unlock()
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	end := time.Now().Add(d)
	for time.Now().Before(end) && ctx.Err() == nil {
		<-t.C
		s.broadcast()
	}
}

func (s *Server) runLive(ctx context.Context) {
	s.mu.Lock()
	s.phase = "live"
	s.phaseEnd = time.Now().Add(time.Duration(float64(s.cfg.Session)/float64(s.cfg.Speed)) * time.Second)
	eng := s.eng
	s.mu.Unlock()

	// 20 ticks per real second; each tick advances Speed/20 simulated seconds.
	stepsPerTick := s.cfg.Speed / 20
	if stepsPerTick < 1 {
		stepsPerTick = 1
	}
	t := time.NewTicker(50 * time.Millisecond)
	defer t.Stop()
	n := 0
	for ctx.Err() == nil {
		<-t.C
		for i := 0; i < stepsPerTick; i++ {
			eng.Step()
		}
		n++
		if n%2 == 0 {
			s.broadcast()
		}
		if eng.Closed {
			s.broadcast()
			return
		}
	}
}

func (s *Server) closeSession() {
	s.mu.Lock()
	eng := s.eng
	id := s.sessID
	s.phase = "closed"
	s.lastSess = id
	s.reviews[id] = map[string]*reviewEntry{}
	eng.Lock()
	humans := append([]string(nil), eng.Humans()...)
	var news []scoring.NewsAt
	var headlines []string
	for _, ev := range eng.Published {
		news = append(news, scoring.NewsAt{At: ev.At, Headline: ev.Headline})
		headlines = append(headlines, ev.Headline)
	}
	closePx := eng.ClosePrice
	fair := eng.FairValue()
	sum := sessionSummary{ID: id, Ticker: eng.Scenario.Ticker, Company: eng.Scenario.Company, Seed: eng.Cfg.Seed,
		Start: eng.Scenario.StartPrice, Close: float64(closePx) / 100, Fair: fair, EndedAt: time.Now(), Players: len(humans)}
	type job struct {
		owner string
		in    coach.Input
	}
	var jobs []job
	bestRt := -1e9
	for _, h := range humans {
		a, _ := eng.Account(h)
		res := scoring.Score(a, closePx, news)
		s.reviews[id][h] = &reviewEntry{Status: "pending", Player: a.Name, Result: &res}
		if res.ReturnPct > bestRt && res.Fills > 0 {
			bestRt = res.ReturnPct
			sum.Winner = a.Name
			sum.WinnerRt = res.ReturnPct
		}
		jobs = append(jobs, job{owner: h, in: coach.Input{
			Player: a.Name, Ticker: eng.Scenario.Ticker, Company: eng.Scenario.Company, Brief: eng.Scenario.Brief,
			Result: res, News: headlines, ClosePx: float64(closePx) / 100, StartPx: eng.Scenario.StartPrice, FairEnd: fair,
		}})
	}
	s.logs[id] = append([]engine.LogEntry(nil), eng.Log...)
	eng.Unlock()
	s.history = append([]sessionSummary{sum}, s.history...)
	if len(s.history) > 30 {
		s.history = s.history[:30]
	}
	if len(s.logs) > 10 {
		oldest := ""
		for k := range s.logs {
			if oldest == "" || k < oldest {
				oldest = k
			}
		}
		delete(s.logs, oldest)
	}
	s.mu.Unlock()

	s.persistLog(id)
	for _, j := range jobs {
		go func(j job) {
			rev := s.coach.Review(context.Background(), j.in)
			s.mu.Lock()
			if e := s.reviews[id][j.owner]; e != nil {
				e.Review = &rev
				e.Status = "ready"
			}
			s.mu.Unlock()
			s.broadcast()
		}(j)
	}
}

func (s *Server) persistLog(id string) {
	if s.cfg.DataDir == "" {
		return
	}
	s.mu.Lock()
	entries := s.logs[id]
	s.mu.Unlock()
	if err := os.MkdirAll(s.cfg.DataDir, 0o755); err != nil {
		log.Printf("data dir: %v", err)
		return
	}
	f, err := os.Create(filepath.Join(s.cfg.DataDir, id+".jsonl"))
	if err != nil {
		log.Printf("persist: %v", err)
		return
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, e := range entries {
		enc.Encode(e)
	}
}

// ---- HTTP ----

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func (s *Server) playerFrom(r *http.Request) *Player {
	tok := r.Header.Get("X-Player-Token")
	if tok == "" {
		tok = r.URL.Query().Get("token")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.players[tok]
}

func (s *Server) handleJoin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"name"`
		Token string `json:"token"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	req.Name = strings.TrimSpace(req.Name)
	if len(req.Name) > 24 {
		req.Name = req.Name[:24]
	}
	s.mu.Lock()
	p := s.players[req.Token]
	if p == nil {
		if req.Name == "" {
			s.mu.Unlock()
			writeErr(w, 400, errors.New("name required"))
			return
		}
		b := make([]byte, 16)
		rand.Read(b)
		p = &Player{ID: "p-" + hex.EncodeToString(b[:6]), Name: req.Name, Token: hex.EncodeToString(b)}
		s.players[p.Token] = p
		s.byID[p.ID] = p
	} else if req.Name != "" {
		p.Name = req.Name
	}
	eng, phase := s.eng, s.phase
	s.mu.Unlock()
	if eng != nil && phase != "closed" {
		eng.Join(p.ID, p.Name)
	}
	writeJSON(w, 200, map[string]any{"token": p.Token, "id": p.ID, "name": p.Name})
}

type orderReq struct {
	Side  string `json:"side"`
	Type  string `json:"type"`
	Price int64  `json:"price"` // cents
	Qty   int64  `json:"qty"`
	ID    uint64 `json:"id"`
}

func parseSide(s string) (book.Side, error) {
	switch s {
	case "buy":
		return book.Buy, nil
	case "sell":
		return book.Sell, nil
	}
	return 0, errors.New("side must be buy or sell")
}

func (s *Server) handleOrder(w http.ResponseWriter, r *http.Request) {
	p := s.playerFrom(r)
	if p == nil {
		writeErr(w, 401, errors.New("join first"))
		return
	}
	var req orderReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	side, err := parseSide(req.Side)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	typ := book.Limit
	if req.Type == "market" {
		typ = book.Market
	}
	s.mu.Lock()
	eng, phase := s.eng, s.phase
	s.mu.Unlock()
	if phase != "live" {
		writeErr(w, 409, errors.New("the market is not open"))
		return
	}
	eng.Join(p.ID, p.Name)
	id, fills, err := eng.Submit(p.ID, side, typ, req.Price, req.Qty)
	if err != nil {
		writeErr(w, 422, err)
		return
	}
	writeJSON(w, 200, map[string]any{"id": id, "fills": fills})
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	p := s.playerFrom(r)
	if p == nil {
		writeErr(w, 401, errors.New("join first"))
		return
	}
	var req orderReq
	json.NewDecoder(r.Body).Decode(&req)
	s.mu.Lock()
	eng := s.eng
	s.mu.Unlock()
	if err := eng.Cancel(p.ID, req.ID); err != nil {
		writeErr(w, 422, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	p := s.playerFrom(r)
	if p == nil {
		writeErr(w, 401, errors.New("join first"))
		return
	}
	var req orderReq
	json.NewDecoder(r.Body).Decode(&req)
	side, err := parseSide(req.Side)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	s.mu.Lock()
	eng, phase := s.eng, s.phase
	s.mu.Unlock()
	if phase != "live" {
		writeErr(w, 409, errors.New("the market is not open"))
		return
	}
	id, err := eng.PlaceStop(p.ID, side, req.Price, req.Qty)
	if err != nil {
		writeErr(w, 422, err)
		return
	}
	writeJSON(w, 200, map[string]any{"id": id})
}

func (s *Server) handleStopCancel(w http.ResponseWriter, r *http.Request) {
	p := s.playerFrom(r)
	if p == nil {
		writeErr(w, 401, errors.New("join first"))
		return
	}
	var req orderReq
	json.NewDecoder(r.Body).Decode(&req)
	s.mu.Lock()
	eng := s.eng
	s.mu.Unlock()
	if err := eng.CancelStop(p.ID, req.ID); err != nil {
		writeErr(w, 422, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleLimit(w http.ResponseWriter, r *http.Request) {
	p := s.playerFrom(r)
	if p == nil {
		writeErr(w, 401, errors.New("join first"))
		return
	}
	var req struct {
		Limit int64 `json:"limit"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	s.mu.Lock()
	eng, phase := s.eng, s.phase
	s.mu.Unlock()
	if phase != "premarket" {
		writeErr(w, 409, errors.New("the position limit can only be set in pre-market"))
		return
	}
	eng.Join(p.ID, p.Name)
	if err := eng.SetLimit(p.ID, req.Limit); err != nil {
		writeErr(w, 422, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleThesis(w http.ResponseWriter, r *http.Request) {
	p := s.playerFrom(r)
	if p == nil {
		writeErr(w, 401, errors.New("join first"))
		return
	}
	var req struct {
		Thesis     string `json:"thesis"`
		Direction  int    `json:"dir"`
		Confidence int    `json:"confidence"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if len(req.Thesis) > 120 {
		req.Thesis = req.Thesis[:120]
	}
	s.mu.Lock()
	eng := s.eng
	s.mu.Unlock()
	eng.Join(p.ID, p.Name)
	if err := eng.SetThesis(p.ID, req.Thesis, req.Direction, req.Confidence); err != nil {
		writeErr(w, 422, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

type calendarItem struct {
	At    int64  `json:"at"`
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	p := s.playerFrom(r)
	s.mu.Lock()
	eng, phase, end, id := s.eng, s.phase, s.phaseEnd, s.sessID
	s.mu.Unlock()
	if eng == nil {
		writeErr(w, 503, errors.New("starting"))
		return
	}
	if p != nil && phase != "closed" {
		eng.Join(p.ID, p.Name)
	}
	eng.Lock()
	defer eng.Unlock()
	var cal []calendarItem
	for _, ev := range eng.Events {
		if ev.Scheduled {
			cal = append(cal, calendarItem{At: ev.At, Kind: string(ev.Kind), Label: ev.Label})
		}
	}
	out := map[string]any{
		"session": id, "phase": phase, "phase_left": time.Until(end).Seconds(),
		"speed": s.cfg.Speed, "session_seconds": eng.Cfg.SessionSeconds, "candle_seconds": eng.Cfg.CandleSeconds,
		"premarket_seconds": s.cfg.Premarket.Seconds(), "results_seconds": s.cfg.Results.Seconds(),
		"scenario": map[string]any{
			"ticker": eng.Scenario.Ticker, "company": eng.Scenario.Company, "sector": eng.Scenario.Sector,
			"brief": eng.Scenario.Brief, "start": eng.Scenario.StartPrice,
		},
		"calendar": cal, "news": eng.Published, "prior": eng.Prior, "candles": eng.Candles,
		"roster": map[string]int{"makers": eng.Cfg.Makers, "noise": eng.Cfg.Noise, "medium": eng.Cfg.Medium, "pros": eng.Cfg.Pros},
		"coach":  s.coach.Enabled(), "start_cash": eng.Cfg.StartCash,
	}
	out["pub"] = s.publicFrame(eng, phase, end, id)
	if p != nil {
		out["me"] = s.privateFrame(eng, p.ID)
		out["player"] = p
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleReview(w http.ResponseWriter, r *http.Request) {
	p := s.playerFrom(r)
	if p == nil {
		writeErr(w, 401, errors.New("join first"))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.URL.Query().Get("session")
	if id == "" {
		id = s.lastSess
	}
	m := s.reviews[id]
	if m == nil {
		writeJSON(w, 200, map[string]any{"status": "none"})
		return
	}
	e := m[p.ID]
	if e == nil {
		writeJSON(w, 200, map[string]any{"status": "absent", "session": id})
		return
	}
	// Leaderboard for that session.
	type row struct {
		Name   string  `json:"name"`
		Score  float64 `json:"score"`
		Return float64 `json:"return_pct"`
		Me     bool    `json:"me"`
	}
	var rows []row
	for owner, x := range m {
		rows = append(rows, row{Name: x.Player, Score: x.Result.Score, Return: x.Result.ReturnPct, Me: owner == p.ID})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Score > rows[j].Score })
	writeJSON(w, 200, map[string]any{"status": e.Status, "session": id, "result": e.Result, "review": e.Review, "board": rows})
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, 200, s.history)
}

func (s *Server) handleReplay(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("session")
	s.mu.Lock()
	entries := s.logs[id]
	s.mu.Unlock()
	if entries == nil {
		writeErr(w, 404, errors.New("no such session in memory"))
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s.json", id))
	writeJSON(w, 200, entries)
}

// ---- frames ----

type lbRow struct {
	Name   string  `json:"name"`
	Equity float64 `json:"equity"`
	Return float64 `json:"ret"`
	Pos    int64   `json:"pos"`
}

type publicFrame struct {
	Session   string              `json:"session"`
	Phase     string              `json:"phase"`
	PhaseLeft float64             `json:"phase_left"`
	Now       int64               `json:"now"`
	Mid       int64               `json:"mid"`
	Last      int64               `json:"last"`
	Bid       int64               `json:"bid"`
	Ask       int64               `json:"ask"`
	BidQty    int64               `json:"bid_qty"`
	AskQty    int64               `json:"ask_qty"`
	Spread    int64               `json:"spread"`
	Bids      []book.LevelView    `json:"bids"`
	Asks      []book.LevelView    `json:"asks"`
	Candle    market.Candle       `json:"candle"`
	NCandles  int                 `json:"ncandles"`
	LastDone  *market.Candle      `json:"last_candle,omitempty"`
	Tape      []engine.Trade      `json:"tape"`
	Vol       float64             `json:"vol"`
	Volume    int64               `json:"volume"`
	News      []market.Event      `json:"news"`
	Board     []lbRow             `json:"board"`
	Players   int                 `json:"players"`
	Closed    bool                `json:"closed"`
	ClosePx   int64               `json:"close_px"`
	Flow      []engine.FlowBucket `json:"flow"` // delayed two simulated minutes
	Speed     int                 `json:"speed"`
}

func (s *Server) publicFrame(eng *engine.Engine, phase string, end time.Time, id string) publicFrame {
	bb, bq, _ := eng.Book.BestBid()
	ba, aq, _ := eng.Book.BestAsk()
	bids, asks := eng.Book.Depth(12)
	f := publicFrame{
		Session: id, Phase: phase, PhaseLeft: time.Until(end).Seconds(), Now: eng.Now,
		Mid: eng.Book.Mid(), Last: eng.Book.LastPrice, Bid: bb, Ask: ba, BidQty: bq, AskQty: aq, Spread: eng.Book.Spread(),
		Bids: bids, Asks: asks, Candle: eng.Current(), NCandles: len(eng.Candles), Tape: eng.Tape(30), Vol: eng.Vol(),
		Volume: eng.Book.Volume, News: eng.Published, Closed: eng.Closed, ClosePx: eng.ClosePrice,
		Flow: eng.Flow(40, 120), Speed: s.cfg.Speed,
	}
	if n := len(eng.Candles); n > 0 {
		c := eng.Candles[n-1]
		f.LastDone = &c
	}
	mark := eng.Book.Mid()
	for _, h := range eng.Humans() {
		a, _ := eng.Account(h)
		eq := a.EquityAt(mark)
		f.Board = append(f.Board, lbRow{Name: a.Name, Equity: eq, Return: (eq - a.StartCash) / a.StartCash * 100, Pos: a.Pos})
	}
	sort.Slice(f.Board, func(i, j int) bool { return f.Board[i].Equity > f.Board[j].Equity })
	f.Players = len(f.Board)
	if len(f.Board) > 10 {
		f.Board = f.Board[:10]
	}
	return f
}

type privateFrame struct {
	Cash       float64            `json:"cash"`
	Pos        int64              `json:"pos"`
	AvgCost    float64            `json:"avg"`
	Realized   float64            `json:"realized"`
	Unrealized float64            `json:"unrealized"`
	Equity     float64            `json:"equity"`
	Limit      int64              `json:"limit"`
	MaxDD      float64            `json:"max_dd"`
	Fills      int                `json:"fills"`
	Open       []openOrder        `json:"open"`
	Stops      []engine.StopOrder `json:"stops"`
	Theses     int                `json:"theses"`
	Live       *liveCoach         `json:"live,omitempty"`
}

// liveCoach is the rule-based nudge shown during the session. It costs
// nothing: the same scoring code that runs at the close, run on the account
// as it stands now.
type liveCoach struct {
	Process  float64 `json:"process"`
	Score    float64 `json:"score"`
	Hint     string  `json:"hint"`
	Tone     string  `json:"tone"` // good, warn, bad, info
	Patterns int     `json:"patterns"`
}

func (s *Server) liveCoachFor(eng *engine.Engine, a *engine.Account, mark int64) *liveCoach {
	var news []scoring.NewsAt
	for _, ev := range eng.Published {
		news = append(news, scoring.NewsAt{At: ev.At, Headline: ev.Headline})
	}
	res := scoring.Score(a, mark, news)
	lc := &liveCoach{Process: res.ProcessScore, Score: res.Score, Patterns: len(res.Mistakes), Tone: "info"}
	realSec := func(sim int64) int64 { return sim / int64(s.cfg.Speed) }
	// Next scheduled event.
	var next *market.Event
	for i := range eng.Events {
		ev := &eng.Events[i]
		if ev.Scheduled && !ev.Fired {
			next = ev
			break
		}
	}
	unprot := int64(0)
	if a.Pos != 0 {
		protected := false
		for _, st := range eng.Stops(a.Owner) {
			if (a.Pos > 0 && st.Side == book.Sell) || (a.Pos < 0 && st.Side == book.Buy) {
				protected = true
			}
		}
		if !protected {
			unprot = a.MaxUnprotected
		}
	}
	switch {
	case a.Pos != 0 && abs64(a.Pos) > a.Limit:
		lc.Hint = fmt.Sprintf("You are %d shares over the %d limit you set yourself. Size is the one variable you control.", abs64(a.Pos)-a.Limit, a.Limit)
		lc.Tone = "bad"
	case a.Pos != 0 && next != nil && realSec(next.At-eng.Now) <= 45 && unprot > 0:
		lc.Hint = fmt.Sprintf("%s in %ds and you are unprotected. Makers pull quotes on headlines; the first prints are the worst of the day.", next.Label, realSec(next.At-eng.Now))
		lc.Tone = "bad"
	case a.Pos != 0 && unprot >= 60*int64(s.cfg.Speed):
		lc.Hint = "Position open for over a minute with no stop. The log is counting every second of it."
		lc.Tone = "warn"
	case len(res.Mistakes) > 0 && res.Mistakes[0].Tag == "revenge":
		lc.Hint = "That re-entry came fast and big after a loss. Step back for a minute; the tape will still be here."
		lc.Tone = "bad"
	case len(res.Mistakes) > 0 && res.Mistakes[0].Tag == "paying_spread":
		lc.Hint = "You keep crossing a wide spread with market orders. A limit one tick inside the touch usually fills in seconds here."
		lc.Tone = "warn"
	case len(res.Mistakes) > 0 && res.Mistakes[0].Tag == "chasing":
		lc.Hint = "Your entries are chasing extended prices. Wait for the pullback; the crowd overshoots, then the pros fade it."
		lc.Tone = "warn"
	case len(res.Mistakes) > 0:
		lc.Hint = res.Mistakes[0].Detail
		lc.Tone = "warn"
	case a.Pos != 0 && next != nil && realSec(next.At-eng.Now) <= 60:
		lc.Hint = fmt.Sprintf("%s drops in %ds. Pros already know; watch for size sweeping the book.", next.Label, realSec(next.At-eng.Now))
		lc.Tone = "info"
	case res.Episodes > 0 && res.AvgEntryGap < -0.2:
		lc.Hint = fmt.Sprintf("Entries are averaging %.2f%% inside fair value: you are fading the crowd, not joining it. Keep that up.", res.AvgEntryGap)
		lc.Tone = "good"
	case res.Episodes > 0 && res.Wins > res.Losses:
		lc.Hint = fmt.Sprintf("%d of %d round trips green with a %.2f%% max drawdown. Protect it: tighten stops rather than add size.", res.Wins, res.Episodes, res.MaxDrawdown)
		lc.Tone = "good"
	case a.Pos != 0:
		lc.Hint = "Stop resting, size inside your limit. Now read the tape: who is moving it, the crowd or the pros?"
		lc.Tone = "good"
	default:
		lc.Hint = "Flat. Pick a thesis before the next entry; calibration is scored."
		lc.Tone = "info"
	}
	return lc
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

type openOrder struct {
	ID        uint64 `json:"id"`
	Side      string `json:"side"`
	Price     int64  `json:"price"`
	Remaining int64  `json:"remaining"`
	Age       int64  `json:"age"`
}

func (s *Server) privateFrame(eng *engine.Engine, owner string) *privateFrame {
	a, ok := eng.Account(owner)
	if !ok {
		return nil
	}
	mark := eng.Book.Mid()
	pf := &privateFrame{Cash: a.Cash, Pos: a.Pos, AvgCost: a.AvgCost, Realized: a.Realized, Limit: a.Limit, MaxDD: a.MaxDD * 100, Fills: a.FillCount, Theses: len(a.Theses)}
	if a.Pos != 0 {
		px := float64(mark) / 100
		if a.Pos > 0 {
			pf.Unrealized = (px - a.AvgCost) * float64(a.Pos)
		} else {
			pf.Unrealized = (a.AvgCost - px) * float64(-a.Pos)
		}
	}
	pf.Equity = a.EquityAt(mark)
	for _, o := range eng.OpenOrders(owner) {
		pf.Open = append(pf.Open, openOrder{ID: o.ID, Side: o.Side.String(), Price: o.Price, Remaining: o.Remaining, Age: o.Age})
	}
	pf.Stops = eng.Stops(owner)
	if !eng.Closed {
		pf.Live = s.liveCoachFor(eng, a, mark)
	}
	return pf
}

// broadcast sends one frame to every connected client.
func (s *Server) broadcast() {
	s.mu.Lock()
	eng, phase, end, id := s.eng, s.phase, s.phaseEnd, s.sessID
	s.mu.Unlock()
	if eng == nil {
		return
	}
	clients := s.hub.clients()
	if len(clients) == 0 {
		eng.Lock()
		eng.DrainHumanFills()
		eng.Unlock()
		return
	}
	eng.Lock()
	pub := s.publicFrame(eng, phase, end, id)
	fills := eng.DrainHumanFills()
	byOwner := map[string][]engine.FillRec{}
	for _, f := range fills {
		byOwner[ownerOfFill(eng, f)] = append(byOwner[ownerOfFill(eng, f)], f)
	}
	pubJSON, _ := json.Marshal(pub)
	type msg struct {
		Type  string           `json:"type"`
		Pub   json.RawMessage  `json:"pub"`
		Me    *privateFrame    `json:"me,omitempty"`
		Fills []engine.FillRec `json:"fills,omitempty"`
	}
	for _, c := range clients {
		m := msg{Type: "tick", Pub: pubJSON}
		if c.player != nil {
			m.Me = s.privateFrame(eng, c.player.ID)
			m.Fills = byOwner[c.player.ID]
		}
		b, _ := json.Marshal(m)
		c.send(b)
	}
	eng.Unlock()
}

// ownerOfFill recovers the owner of a human fill by matching the order ID
// against the humans' recorded fills (the FillRec itself carries no owner).
func ownerOfFill(eng *engine.Engine, f engine.FillRec) string {
	for _, h := range eng.Humans() {
		a, _ := eng.Account(h)
		for i := len(a.Fills) - 1; i >= 0 && i >= len(a.Fills)-50; i-- {
			x := a.Fills[i]
			if x.OrderID == f.OrderID && x.Time == f.Time && x.Price == f.Price && x.Qty == f.Qty && x.Side == f.Side {
				return h
			}
		}
	}
	return ""
}
