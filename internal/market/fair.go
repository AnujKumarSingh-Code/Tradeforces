// Package market holds the hidden fair-value process, the scenario library
// and the synthetic prior-history generator.
package market

import (
	"math"
	"math/rand/v2"
)

// FairValue is the one number the player never sees. It is a slow-drifting
// anchor plus a mean-reverting value around it, with discrete jumps on news.
type FairValue struct {
	V      float64 // current fair value, dollars
	anchor float64
	sigma  float64 // per-second vol of V around the anchor
	sigmaA float64 // per-second vol of the anchor random walk
	theta  float64 // reversion speed of V toward the anchor
	rng    *rand.Rand
	// logVol is a slowly mean-reverting volatility regime: information
	// arrives in bursts, which is what gives real series their clustering.
	logVol float64
}

// VolRegime returns the current volatility multiplier (1 = average).
func (f *FairValue) VolRegime() float64 { return math.Exp(f.logVol) }

// NewFairValue builds a fair-value process with a daily volatility expressed
// as a fraction (0.02 means 2% per simulated trading day of 23,400 seconds).
func NewFairValue(start float64, dailyVol float64, rng *rand.Rand) *FairValue {
	perSec := dailyVol / math.Sqrt(23400)
	return &FairValue{
		V:      start,
		anchor: start,
		sigma:  perSec * 0.6,
		sigmaA: perSec * 0.8,
		theta:  1.0 / 600, // ten-minute half-life-ish
		rng:    rng,
	}
}

// Step advances the process by dt simulated seconds.
func (f *FairValue) Step(dt float64) {
	sq := math.Sqrt(dt)
	const kappa, eta = 1.0 / 900, 0.035
	f.logVol += -kappa*f.logVol*dt + eta*sq*f.rng.NormFloat64()
	m := math.Exp(f.logVol)
	sA, sV := f.sigmaA*m, f.sigma*m
	f.anchor *= math.Exp(sA*sq*f.rng.NormFloat64() - 0.5*sA*sA*dt)
	f.V += f.theta*(f.anchor-f.V)*dt + f.V*sV*sq*f.rng.NormFloat64()
	if f.V < 0.5 {
		f.V = 0.5
	}
}

// Jump applies a proportional shock (0.03 is +3%) to both the anchor and V.
func (f *FairValue) Jump(pct float64) {
	f.anchor *= 1 + pct
	f.V *= 1 + pct
}
