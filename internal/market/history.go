package market

import (
	"math"
	"math/rand/v2"
)

// Candle is an OHLCV bar. Time is simulated seconds (negative for prior days).
type Candle struct {
	Time   int64   `json:"t"`
	Open   float64 `json:"o"`
	High   float64 `json:"h"`
	Low    float64 `json:"l"`
	Close  float64 `json:"c"`
	Volume int64   `json:"v"`
}

// PriorHistory synthesises `days` daily candles ending at the start price so
// the chart has context before the open. Times are placed one simulated day
// (86,400 s) apart before t=0.
func PriorHistory(start float64, dailyVol float64, days int, rng *rand.Rand) []Candle {
	closes := make([]float64, days)
	p := start
	// Walk backwards so the series ends exactly at the start price.
	for i := days - 1; i >= 0; i-- {
		closes[i] = p
		p *= math.Exp(-dailyVol*rng.NormFloat64() + 0.5*dailyVol*dailyVol)
	}
	out := make([]Candle, days)
	for i := range closes {
		open := closes[i] * math.Exp(dailyVol*0.3*rng.NormFloat64())
		if i > 0 {
			open = closes[i-1] * math.Exp(dailyVol*0.2*rng.NormFloat64())
		}
		hi := math.Max(open, closes[i]) * (1 + math.Abs(rng.NormFloat64())*dailyVol*0.5)
		lo := math.Min(open, closes[i]) * (1 - math.Abs(rng.NormFloat64())*dailyVol*0.5)
		out[i] = Candle{
			Time: int64(-(days - i)) * 86400,
			Open: round2(open), High: round2(hi), Low: round2(lo), Close: round2(closes[i]),
			Volume: 500000 + rng.Int64N(1500000),
		}
	}
	return out
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
