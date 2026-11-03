package scoring

import (
	"testing"

	"github.com/anujkumarsingh-code/tradeforces/internal/book"
	"github.com/anujkumarsingh-code/tradeforces/internal/engine"
)

func fill(t int64, side book.Side, px, qty int64, fair float64, aggr bool) engine.FillRec {
	return engine.FillRec{Time: t, Side: side, Price: px, Qty: qty, Fair: fair, Mid: px, Spread: 2, Aggressor: aggr}
}

func exit(t int64, side book.Side, px, qty int64, fair float64, realized float64) engine.FillRec {
	f := fill(t, side, px, qty, fair, true)
	f.Realized = realized
	return f
}

func TestEpisodesAndMistakes(t *testing.T) {
	a := &engine.Account{Owner: "p", Human: true, StartCash: 100000, Cash: 100000, Limit: 500, PeakEquity: 100000}
	// Episode 1: chase (buy 2% above fair), average down, lose, no stop.
	a.Fills = append(a.Fills,
		fill(100, book.Buy, 10200, 300, 100, true),
		fill(200, book.Buy, 10100, 300, 100, true),
		exit(400, book.Sell, 10000, 600, 100, -900),
	)
	// Episode 2: revenge: within 600s of the loss, sized 2x average.
	a.Fills = append(a.Fills,
		fill(500, book.Buy, 10000, 1200, 100, true),
		exit(900, book.Sell, 10050, 1200, 100, 600),
	)
	a.Realized = -900 + 600
	a.MaxUnprotected = 3000
	a.Equity = []float64{100000, 99000, 99600}
	a.MaxDD = 0.01
	res := Score(a, 10050, nil)
	if res.Episodes != 2 {
		t.Fatalf("episodes %d", res.Episodes)
	}
	want := map[string]bool{"chasing": false, "averaging_down": false, "revenge": false, "no_stop": false, "oversizing": false}
	for _, m := range res.Mistakes {
		if _, ok := want[m.Tag]; ok {
			want[m.Tag] = true
		}
	}
	for k, v := range want {
		if !v {
			t.Errorf("expected mistake %s to be detected: %+v", k, res.Mistakes)
		}
	}
	if res.Score <= 0 || res.Score > 100 {
		t.Fatalf("score out of range: %f", res.Score)
	}
	if res.ProcessScore > 60 {
		t.Fatalf("process score too lenient: %f", res.ProcessScore)
	}
}

func TestCleanSessionHasNoMistakes(t *testing.T) {
	a := &engine.Account{Owner: "p", Human: true, StartCash: 100000, Cash: 100000, Limit: 1000, PeakEquity: 100000}
	a.Fills = append(a.Fills,
		fill(100, book.Buy, 9900, 200, 100, false),
		fill(3000, book.Sell, 10100, 200, 100, false),
	)
	a.Orders = []engine.OrderRec{{Time: 110, ID: 1, Kind: "stop", Side: book.Sell, Price: 9800, Qty: 200}, {Time: 3000, ID: 1, Kind: "stop_cancel"}}
	a.Realized = 400
	res := Score(a, 10100, nil)
	if len(res.Mistakes) != 0 {
		t.Fatalf("unexpected mistakes: %+v", res.Mistakes)
	}
	if res.AvgEntryGap >= 0 {
		t.Fatalf("buying below fair should give a negative gap, got %f", res.AvgEntryGap)
	}
	if len(res.Strengths) < 2 {
		t.Fatalf("expected strengths, got %v", res.Strengths)
	}
}
