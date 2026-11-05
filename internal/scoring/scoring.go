// Package scoring turns a player's event record into the three-part score
// (return, risk discipline, process) and the fixed taxonomy of detectable
// mistakes. Everything here is computed from the log with no model involved;
// Claude's job is explaining it, not finding it.
package scoring

import (
	"fmt"
	"math"
	"sort"

	"github.com/anujkumarsingh-code/tradeforces/internal/book"
	"github.com/anujkumarsingh-code/tradeforces/internal/engine"
)

// Episode is one round trip: flat -> position -> flat.
type Episode struct {
	N          int     `json:"n"`
	Start      int64   `json:"start"`
	End        int64   `json:"end"`
	Long       bool    `json:"long"`
	EntryPx    float64 `json:"entry_px"`
	ExitPx     float64 `json:"exit_px"`
	FairEntry  float64 `json:"fair_entry"`
	EntryGap   float64 `json:"entry_gap_pct"` // positive = paid above fair (long) or sold below fair (short): chased
	MaxSize    int64   `json:"max_size"`
	Adds       int     `json:"adds"`
	AddsUnder  int     `json:"adds_underwater"`
	PnL        float64 `json:"pnl"`
	HoldSec    int64   `json:"hold_sec"`
	Aggressive int     `json:"aggressive_fills"`
	Fills      int     `json:"fills"`
	HadStop    bool    `json:"had_stop"`
	Stopped    bool    `json:"stopped_out"`
	NewsNearby string  `json:"news_nearby,omitempty"`
	Open       bool    `json:"open_at_close"`
}

// Mistake is one detected pattern.
type Mistake struct {
	Tag      string  `json:"tag"`
	Title    string  `json:"title"`
	Detail   string  `json:"detail"`
	Count    int     `json:"count"`
	Severity float64 `json:"severity"` // 0..1
}

// Calibration compares claimed confidence to realised hit rate.
type Calibration struct {
	Theses      int     `json:"theses"`
	AvgClaimed  float64 `json:"avg_claimed"`
	HitRate     float64 `json:"hit_rate"`
	Gap         float64 `json:"gap"` // claimed - actual; positive = overconfident
	Description string  `json:"description"`
}

// Result is the full scorecard.
type Result struct {
	Score        float64 `json:"score"`
	ReturnScore  float64 `json:"return_score"`
	RiskScore    float64 `json:"risk_score"`
	ProcessScore float64 `json:"process_score"`

	StartCash    float64 `json:"start_cash"`
	FinalEquity  float64 `json:"final_equity"`
	ReturnPct    float64 `json:"return_pct"`
	MaxDrawdown  float64 `json:"max_drawdown_pct"`
	Realized     float64 `json:"realized"`
	Fills        int     `json:"fills"`
	Episodes     int     `json:"episodes"`
	Wins         int     `json:"wins"`
	Losses       int     `json:"losses"`
	WinRate      float64 `json:"win_rate"`
	AvgHoldWin   float64 `json:"avg_hold_win_sec"`
	AvgHoldLoss  float64 `json:"avg_hold_loss_sec"`
	AvgEntryGap  float64 `json:"avg_entry_gap_pct"` // negative is good: bought below fair
	AggressorPct float64 `json:"aggressor_pct"`
	Limit        int64   `json:"limit"`
	MaxPos       int64   `json:"max_pos"`
	UnprotectedS int64   `json:"max_unprotected_sec"`

	Mistakes    []Mistake    `json:"mistakes"`
	Strengths   []string     `json:"strengths"`
	Calibration *Calibration `json:"calibration,omitempty"`
	EpisodeList []Episode    `json:"episodes_list"`
}

const simMinute = 60

// Score computes the scorecard for one account.
func Score(a *engine.Account, closePx int64, news []NewsAt) Result {
	r := Result{StartCash: a.StartCash, Limit: a.Limit, Fills: len(a.Fills), Realized: a.Realized, UnprotectedS: a.MaxUnprotected}
	r.FinalEquity = a.EquityAt(closePx)
	if a.StartCash > 0 {
		r.ReturnPct = (r.FinalEquity - a.StartCash) / a.StartCash * 100
	}
	r.MaxDrawdown = a.MaxDD * 100

	eps := episodes(a, closePx, news)
	r.EpisodeList = eps
	r.Episodes = len(eps)
	var holdW, holdL float64
	var gapSum float64
	var aggr, fills int
	for _, e := range eps {
		if e.PnL > 0 {
			r.Wins++
			holdW += float64(e.HoldSec)
		} else if e.PnL < 0 {
			r.Losses++
			holdL += float64(e.HoldSec)
		}
		gapSum += e.EntryGap
		aggr += e.Aggressive
		fills += e.Fills
		if e.MaxSize > r.MaxPos {
			r.MaxPos = e.MaxSize
		}
	}
	if r.Wins > 0 {
		r.AvgHoldWin = holdW / float64(r.Wins)
	}
	if r.Losses > 0 {
		r.AvgHoldLoss = holdL / float64(r.Losses)
	}
	if r.Wins+r.Losses > 0 {
		r.WinRate = float64(r.Wins) / float64(r.Wins+r.Losses)
	}
	if len(eps) > 0 {
		r.AvgEntryGap = gapSum / float64(len(eps))
	}
	if fills > 0 {
		r.AggressorPct = float64(aggr) / float64(fills) * 100
	}

	r.Mistakes = detect(a, eps, r)
	r.Calibration = calibrate(a, eps)
	r.Strengths = strengths(r, eps)

	// Return: +5% on the day is a perfect 100, -5% is 0.
	r.ReturnScore = clamp(50+r.ReturnPct*10, 0, 100)

	// Risk discipline: how much drawdown was taken (5% is a full penalty)
	// and what return it bought, penalised for oversizing.
	if len(eps) == 0 {
		r.RiskScore = 50
	} else {
		ddPenalty := math.Min(1, r.MaxDrawdown/5)
		base := 100 - 60*ddPenalty
		ratio := 0.0
		if r.MaxDrawdown > 0.05 {
			ratio = clamp(r.ReturnPct/r.MaxDrawdown, -1, 1)
		} else if r.ReturnPct > 0 {
			ratio = 1
		}
		r.RiskScore = clamp(base+20*ratio, 0, 100)
	}
	for _, m := range r.Mistakes {
		if m.Tag == "oversizing" {
			r.RiskScore = clamp(r.RiskScore-15*math.Min(3, float64(m.Count)), 0, 100)
		}
	}

	// Process: start from 100 and subtract by severity; a detected pattern
	// costs at least half its weight, and several distinct patterns compound.
	p := 100.0
	for _, m := range r.Mistakes {
		p -= math.Max(0.5, m.Severity) * weightFor(m.Tag)
	}
	if n := len(r.Mistakes); n > 2 {
		p -= 3 * float64(n-2)
	}
	if len(eps) > 0 && r.AvgEntryGap < -0.1 {
		p += 8
	}
	// A session with a detected pattern never reads as perfect.
	if len(r.Mistakes) > 0 && p > 92 {
		p = 92
	}
	if len(eps) == 0 {
		p = 50
	}
	r.ProcessScore = clamp(p, 0, 100)

	r.Score = math.Round((0.4*r.ReturnScore+0.3*r.RiskScore+0.3*r.ProcessScore)*10) / 10
	return r
}

// NewsAt is a published headline with its time, for episode context.
type NewsAt struct {
	At       int64
	Headline string
}

func weightFor(tag string) float64 {
	switch tag {
	case "revenge":
		return 18
	case "no_stop":
		return 16
	case "overtrading":
		return 12
	case "chasing":
		return 12
	case "averaging_down":
		return 12
	case "cut_winners":
		return 10
	case "oversizing":
		return 10
	case "paying_spread":
		return 6
	case "held_through_news":
		return 6
	}
	return 8
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

func episodes(a *engine.Account, closePx int64, news []NewsAt) []Episode {
	var eps []Episode
	var cur *Episode
	var pos int64
	var avg float64
	stops := stopIntervals(a)
	for _, f := range a.Fills {
		signed := f.Qty
		if f.Side == book.Sell {
			signed = -f.Qty
		}
		px := float64(f.Price) / 100
		if pos == 0 {
			cur = &Episode{N: len(eps) + 1, Start: f.Time, Long: signed > 0, EntryPx: px, FairEntry: f.Fair}
			if f.Fair > 0 {
				gap := (px - f.Fair) / f.Fair * 100
				if !cur.Long {
					gap = -gap
				}
				cur.EntryGap = math.Round(gap*100) / 100
			}
			avg = px
		} else if (pos > 0) == (signed > 0) {
			// Adding to the position.
			cur.Adds++
			underwater := (pos > 0 && px < avg) || (pos < 0 && px > avg)
			if underwater {
				cur.AddsUnder++
			}
			avg = (avg*float64(abs(pos)) + px*float64(f.Qty)) / float64(abs(pos)+f.Qty)
		}
		if cur == nil {
			continue
		}
		cur.Fills++
		if f.Aggressor {
			cur.Aggressive++
		}
		cur.PnL += f.Realized
		pos += signed
		if abs(pos) > cur.MaxSize {
			cur.MaxSize = abs(pos)
		}
		if pos == 0 {
			cur.End = f.Time
			cur.ExitPx = px
			cur.HoldSec = cur.End - cur.Start
			cur.HadStop = hadStop(stops, cur.Start, cur.End)
			cur.Stopped = firedStop(a, cur.Start, cur.End)
			cur.NewsNearby = nearNews(news, cur.Start, cur.End)
			cur.PnL = math.Round(cur.PnL*100) / 100
			eps = append(eps, *cur)
			cur = nil
		} else if (pos > 0) != cur.Long {
			// Flipped through zero: close this episode and open the reverse.
			cur.End = f.Time
			cur.ExitPx = px
			cur.HoldSec = cur.End - cur.Start
			cur.HadStop = hadStop(stops, cur.Start, cur.End)
			cur.NewsNearby = nearNews(news, cur.Start, cur.End)
			cur.PnL = math.Round(cur.PnL*100) / 100
			eps = append(eps, *cur)
			cur = &Episode{N: len(eps) + 1, Start: f.Time, Long: pos > 0, EntryPx: px, FairEntry: f.Fair, Fills: 1, MaxSize: abs(pos)}
			avg = px
		}
	}
	if cur != nil {
		// Still open at the close: mark it at the closing auction.
		px := float64(closePx) / 100
		if cur.Long {
			cur.PnL += (px - avg) * float64(abs(pos))
		} else {
			cur.PnL += (avg - px) * float64(abs(pos))
		}
		cur.End = a.Fills[len(a.Fills)-1].Time
		cur.ExitPx = px
		cur.HoldSec = cur.End - cur.Start
		cur.Open = true
		cur.HadStop = hadStop(stops, cur.Start, cur.End)
		cur.NewsNearby = nearNews(news, cur.Start, cur.End)
		cur.PnL = math.Round(cur.PnL*100) / 100
		eps = append(eps, *cur)
	}
	return eps
}

type interval struct{ from, to int64 }

func stopIntervals(a *engine.Account) []interval {
	open := map[uint64]int64{}
	var out []interval
	for _, o := range a.Orders {
		switch o.Kind {
		case "stop":
			open[o.ID] = o.Time
		case "stop_cancel", "stop_fire":
			if t, ok := open[o.ID]; ok {
				out = append(out, interval{t, o.Time})
				delete(open, o.ID)
			}
		}
	}
	for _, t := range open {
		out = append(out, interval{t, math.MaxInt64})
	}
	return out
}

func hadStop(iv []interval, from, to int64) bool {
	for _, i := range iv {
		if i.from <= to && i.to >= from {
			return true
		}
	}
	return false
}

func firedStop(a *engine.Account, from, to int64) bool {
	for _, o := range a.Orders {
		if o.Kind == "stop_fire" && o.Time >= from && o.Time <= to {
			return true
		}
	}
	return false
}

func nearNews(news []NewsAt, from, to int64) string {
	for _, n := range news {
		if n.At >= from-120 && n.At <= to {
			return n.Headline
		}
	}
	return ""
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func detect(a *engine.Account, eps []Episode, r Result) []Mistake {
	var out []Mistake
	if len(eps) == 0 {
		return out
	}
	totalSize := 0.0
	for _, e := range eps {
		totalSize += float64(e.MaxSize)
	}

	// Revenge trading: new position within 30 real seconds (600 sim s) of a
	// realised loss, sized above 1.5x the average of your other positions.
	rev := 0
	for i := 1; i < len(eps); i++ {
		prev, e := eps[i-1], eps[i]
		others := (totalSize - float64(e.MaxSize)) / float64(len(eps)-1)
		if prev.PnL < 0 && e.Start-prev.End <= 600 && float64(e.MaxSize) > 1.5*others {
			rev++
		}
	}
	if rev > 0 {
		out = append(out, Mistake{Tag: "revenge", Title: "Revenge trading", Count: rev, Severity: math.Min(1, float64(rev)/2),
			Detail: fmt.Sprintf("%d time(s) you re-entered within 30 seconds of a losing exit, sized above 1.5x your usual.", rev)})
	}

	// Overtrading: many round trips with nothing to show for it.
	if len(eps) >= 12 && math.Abs(r.ReturnPct) < 0.5 {
		out = append(out, Mistake{Tag: "overtrading", Title: "Overtrading", Count: len(eps), Severity: math.Min(1, float64(len(eps))/25),
			Detail: fmt.Sprintf("%d round trips for a net %.2f%%: a lot of activity, no edge, and every crossing paid the spread.", len(eps), r.ReturnPct)})
	}

	// No stop: a position held more than two real minutes (2,400 sim s)
	// with no resting protective order.
	if a.MaxUnprotected > 2400 {
		mins := float64(a.MaxUnprotected) / 20 / 60
		out = append(out, Mistake{Tag: "no_stop", Title: "No stop", Count: 1, Severity: math.Min(1, mins/6),
			Detail: fmt.Sprintf("Your longest stretch in a position with no protective stop was %.1f real minutes.", mins)})
	}

	// Chasing: entering with price already extended past fair value.
	chase := 0
	for _, e := range eps {
		if e.EntryGap > 1.0 {
			chase++
		}
	}
	if chase > 0 {
		out = append(out, Mistake{Tag: "chasing", Title: "Chasing", Count: chase, Severity: math.Min(1, float64(chase)/3),
			Detail: fmt.Sprintf("%d entry/entries were made with price already more than 1%% through fair value in your direction.", chase)})
	}

	// Averaging down.
	avgDown := 0
	for _, e := range eps {
		avgDown += e.AddsUnder
	}
	if avgDown > 0 {
		out = append(out, Mistake{Tag: "averaging_down", Title: "Averaging down", Count: avgDown, Severity: math.Min(1, float64(avgDown)/3),
			Detail: fmt.Sprintf("%d time(s) you added size to a position that was already underwater.", avgDown)})
	}

	// Cutting winners, holding losers.
	if r.Wins >= 2 && r.Losses >= 2 && r.AvgHoldWin < 0.6*r.AvgHoldLoss {
		out = append(out, Mistake{Tag: "cut_winners", Title: "Cutting winners, holding losers", Count: 1, Severity: math.Min(1, (r.AvgHoldLoss/math.Max(1, r.AvgHoldWin)-1)/2),
			Detail: fmt.Sprintf("Winners were held %.0fs on average, losers %.0fs. The asymmetry is backwards.", r.AvgHoldWin/20, r.AvgHoldLoss/20)})
	}

	// Oversizing against the self-declared limit.
	over := 0
	for _, e := range eps {
		if e.MaxSize > a.Limit {
			over++
		}
	}
	if over > 0 {
		out = append(out, Mistake{Tag: "oversizing", Title: "Oversizing", Count: over, Severity: math.Min(1, float64(over)/2),
			Detail: fmt.Sprintf("%d position(s) exceeded the %d-share limit you set yourself before the open (largest: %d).", over, a.Limit, r.MaxPos)})
	}

	// Paying the spread: crossing wide spreads with market orders.
	wide := 0
	for _, f := range a.Fills {
		if f.Aggressor && f.Mid > 0 && float64(f.Spread)/float64(f.Mid) > 0.002 {
			wide++
		}
	}
	if wide >= 3 && float64(wide)/float64(len(a.Fills)) > 0.3 {
		out = append(out, Mistake{Tag: "paying_spread", Title: "Paying the spread", Count: wide, Severity: math.Min(1, float64(wide)/10),
			Detail: fmt.Sprintf("%d fills crossed a spread wider than 0.2%% with a market order when a limit would likely have filled.", wide)})
	}

	// Holding an unprotected position through a scheduled release.
	through := 0
	for _, e := range eps {
		if e.NewsNearby != "" && !e.HadStop && e.HoldSec > 600 {
			through++
		}
	}
	if through > 0 {
		out = append(out, Mistake{Tag: "held_through_news", Title: "Held through news without protection", Count: through, Severity: math.Min(1, float64(through)/2),
			Detail: fmt.Sprintf("%d position(s) were carried through a headline with no stop in place.", through)})
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].Severity*weightFor(out[i].Tag) > out[j].Severity*weightFor(out[j].Tag)
	})
	return out
}

func calibrate(a *engine.Account, eps []Episode) *Calibration {
	if len(a.Theses) == 0 {
		return nil
	}
	c := &Calibration{}
	hits, used := 0, 0
	var claimed float64
	for _, t := range a.Theses {
		// The next episode opened after the thesis is the one it applies to.
		for _, e := range eps {
			if e.Start >= t.Time {
				used++
				claimed += float64(t.Confidence)
				if e.PnL > 0 {
					hits++
				}
				break
			}
		}
	}
	if used == 0 {
		return nil
	}
	c.Theses = used
	c.AvgClaimed = claimed / float64(used)
	c.HitRate = float64(hits) / float64(used) * 100
	c.Gap = math.Round((c.AvgClaimed-c.HitRate)*10) / 10
	switch {
	case c.Gap > 20:
		c.Description = "Overconfident: you claimed far more certainty than your results delivered."
	case c.Gap > 8:
		c.Description = "Mildly overconfident."
	case c.Gap < -15:
		c.Description = "Underconfident: your calls were better than you believed."
	default:
		c.Description = "Well calibrated: your stated confidence matched your hit rate."
	}
	return c
}

func strengths(r Result, eps []Episode) []string {
	var s []string
	if len(eps) == 0 {
		return s
	}
	if r.AvgEntryGap < -0.2 {
		s = append(s, fmt.Sprintf("You bought below fair value on average (entries %.2f%% inside fair): you were fading the crowd, not joining it.", r.AvgEntryGap))
	}
	stopped := 0
	for _, e := range eps {
		if e.HadStop {
			stopped++
		}
	}
	if float64(stopped)/float64(len(eps)) >= 0.7 {
		s = append(s, fmt.Sprintf("%d of %d positions had a protective stop resting while open.", stopped, len(eps)))
	}
	if r.Wins >= 2 && r.Losses >= 1 && r.AvgHoldWin > 1.2*r.AvgHoldLoss {
		s = append(s, "Winners were held longer than losers. That asymmetry is the whole game.")
	}
	if r.MaxPos <= r.Limit && r.MaxPos > 0 {
		s = append(s, "Every position stayed inside the limit you set yourself pre-market.")
	}
	if r.AggressorPct < 40 {
		s = append(s, "Most of your fills were passive limit orders: you were paid the spread instead of paying it.")
	}
	return s
}
