package engine

import (
	"testing"

	"github.com/anujkumarsingh-code/tradeforces/internal/book"
)

func short(seed uint64) Config {
	cfg := DefaultConfig(seed)
	cfg.SessionSeconds = 1800
	return cfg
}

func TestDeterministicReplay(t *testing.T) {
	a := New(short(7))
	b := New(short(7))
	a.RunHeadless()
	b.RunHeadless()
	ha, hb := a.MidHistory(), b.MidHistory()
	if len(ha) != len(hb) || len(ha) != 1800 {
		t.Fatalf("lengths %d %d", len(ha), len(hb))
	}
	for i := range ha {
		if ha[i] != hb[i] {
			t.Fatalf("diverged at t=%d: %d vs %d", i, ha[i], hb[i])
		}
	}
	if a.ClosePrice != b.ClosePrice || a.Book.Volume != b.Book.Volume {
		t.Fatalf("close/volume differ")
	}
}

func TestDifferentSeedsDiffer(t *testing.T) {
	a := New(short(1))
	b := New(short(2))
	a.RunHeadless()
	b.RunHeadless()
	if a.ClosePrice == b.ClosePrice && a.Book.Volume == b.Book.Volume {
		t.Fatal("different seeds produced identical sessions")
	}
}

func TestHumanAccountingAndStops(t *testing.T) {
	e := New(short(11))
	for i := 0; i < 200; i++ {
		e.Step()
	}
	acct := e.Join("h1", "Human")
	if acct.Cash != e.Cfg.StartCash {
		t.Fatal("start cash")
	}
	_, fills, err := e.Submit("h1", book.Buy, book.Market, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(fills) == 0 || acct.Pos != 100 {
		t.Fatalf("expected fills, pos=%d fills=%d", acct.Pos, len(fills))
	}
	spent := 0.0
	for _, f := range fills {
		spent += float64(f.Price) / 100 * float64(f.Qty)
	}
	if diff := (e.Cfg.StartCash - spent) - acct.Cash; diff > 1e-6 || diff < -1e-6 {
		t.Fatalf("cash accounting off by %f", diff)
	}
	// A stop well above the market on the sell side is rejected.
	if _, err := e.PlaceStop("h1", book.Sell, e.Book.LastPrice+500, 100); err == nil {
		t.Fatal("sell stop above last must be rejected")
	}
	// A stop far below never fires in a few ticks; a stop just below does.
	id, err := e.PlaceStop("h1", book.Sell, e.Book.LastPrice-1, 100)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 600 && acct.Pos != 0; i++ {
		e.Step()
	}
	if acct.Pos != 0 {
		t.Fatalf("stop %d did not fire, pos=%d", id, acct.Pos)
	}
	if len(e.Stops("h1")) != 0 {
		t.Fatal("fired stop still listed")
	}
	fired := false
	for _, o := range acct.Orders {
		if o.Kind == "stop_fire" {
			fired = true
		}
	}
	if !fired {
		t.Fatal("stop_fire not recorded")
	}
}

func TestExposureLimits(t *testing.T) {
	e := New(short(3))
	for i := 0; i < 50; i++ {
		e.Step()
	}
	e.Join("h1", "Human")
	if _, _, err := e.Submit("h1", book.Buy, book.Market, 0, 9000); err != ErrHardLimit {
		t.Fatalf("want hard limit error, got %v", err)
	}
	if _, _, err := e.Submit("h1", book.Buy, book.Limit, 1, 10); err != ErrPrice {
		t.Fatalf("want price error, got %v", err)
	}
}
