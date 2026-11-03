package book

import "testing"

func TestPriceTimePriority(t *testing.T) {
	b := New()
	b.Submit(&Order{Owner: "a", Side: Sell, Type: Limit, Price: 10100, Qty: 100}, 0)
	b.Submit(&Order{Owner: "b", Side: Sell, Type: Limit, Price: 10100, Qty: 100}, 1)
	b.Submit(&Order{Owner: "c", Side: Sell, Type: Limit, Price: 10050, Qty: 50}, 2)

	fills, err := b.Submit(&Order{Owner: "t", Side: Buy, Type: Limit, Price: 10100, Qty: 200}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(fills) != 3 {
		t.Fatalf("want 3 fills, got %d", len(fills))
	}
	if fills[0].Maker != "c" || fills[0].Price != 10050 || fills[0].Qty != 50 {
		t.Errorf("best price first: %+v", fills[0])
	}
	if fills[1].Maker != "a" || fills[1].Qty != 100 {
		t.Errorf("time priority: %+v", fills[1])
	}
	if fills[2].Maker != "b" || fills[2].Qty != 50 {
		t.Errorf("partial on later order: %+v", fills[2])
	}
	ap, aq, ok := b.BestAsk()
	if !ok || ap != 10100 || aq != 50 {
		t.Errorf("remaining ask: %d %d %v", ap, aq, ok)
	}
	if b.LastPrice != 10100 {
		t.Errorf("last price %d", b.LastPrice)
	}
}

func TestMarketOrderSweepsAndDoesNotRest(t *testing.T) {
	b := New()
	b.Submit(&Order{Owner: "m", Side: Buy, Type: Limit, Price: 9900, Qty: 30}, 0)
	fills, _ := b.Submit(&Order{Owner: "t", Side: Sell, Type: Market, Qty: 100}, 1)
	if len(fills) != 1 || fills[0].Qty != 30 {
		t.Fatalf("fills %+v", fills)
	}
	if b.Resting() != 0 {
		t.Errorf("market remainder must not rest")
	}
}

func TestCancel(t *testing.T) {
	b := New()
	o := &Order{Owner: "a", Side: Buy, Type: Limit, Price: 100, Qty: 10}
	b.Submit(o, 0)
	if _, ok := b.Cancel(o.ID); !ok {
		t.Fatal("cancel failed")
	}
	if _, _, ok := b.BestBid(); ok {
		t.Error("level should be gone")
	}
	if _, ok := b.Cancel(o.ID); ok {
		t.Error("double cancel")
	}
}

func TestLimitRestsWhenNotCrossing(t *testing.T) {
	b := New()
	b.Submit(&Order{Owner: "a", Side: Sell, Type: Limit, Price: 10100, Qty: 10}, 0)
	fills, _ := b.Submit(&Order{Owner: "b", Side: Buy, Type: Limit, Price: 10000, Qty: 10}, 1)
	if len(fills) != 0 {
		t.Fatal("should not cross")
	}
	if b.Spread() != 100 || b.Mid() != 10050 {
		t.Errorf("spread %d mid %d", b.Spread(), b.Mid())
	}
}
