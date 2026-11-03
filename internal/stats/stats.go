// Package stats checks a simulated price series against the stylised facts
// of real financial time series. This is the Phase 0 go/no-go gate.
package stats

import (
	"fmt"
	"math"
	"strings"
)

// Report holds the stylised-fact measurements.
type Report struct {
	Samples        int
	ExcessKurtosis float64
	ReturnAC1      float64
	ReturnAC2      float64
	AbsAC1         float64
	AbsAC5         float64
	AbsAC10        float64
	AbsAC20        float64
	RevertBeta     float64 // regression of next return on (fair - price)/price; positive = mean reversion
	MaxGapPct      float64 // largest |price - fair| / fair
	MeanAbsGapPct  float64
	DailyVolPct    float64
	Pass           bool
	Failures       []string
}

// Returns computes log returns of a cents price series sampled every `every`
// points.
func Returns(prices []int64, every int) []float64 {
	var out []float64
	for i := every; i < len(prices); i += every {
		a, b := prices[i-every], prices[i]
		if a > 0 && b > 0 {
			out = append(out, math.Log(float64(b)/float64(a)))
		}
	}
	return out
}

func meanOf(x []float64) float64 {
	if len(x) == 0 {
		return 0
	}
	s := 0.0
	for _, v := range x {
		s += v
	}
	return s / float64(len(x))
}

func variance(x []float64) float64 {
	m := meanOf(x)
	s := 0.0
	for _, v := range x {
		s += (v - m) * (v - m)
	}
	if len(x) < 2 {
		return 0
	}
	return s / float64(len(x)-1)
}

// Kurtosis returns excess kurtosis.
func Kurtosis(x []float64) float64 {
	m := meanOf(x)
	v := variance(x)
	if v == 0 || len(x) < 4 {
		return 0
	}
	s := 0.0
	for _, r := range x {
		d := r - m
		s += d * d * d * d
	}
	return s/float64(len(x))/(v*v) - 3
}

// Autocorr at a lag.
func Autocorr(x []float64, lag int) float64 {
	if lag <= 0 || lag >= len(x) {
		return 0
	}
	m := meanOf(x)
	num, den := 0.0, 0.0
	for i := range x {
		den += (x[i] - m) * (x[i] - m)
	}
	for i := lag; i < len(x); i++ {
		num += (x[i] - m) * (x[i-lag] - m)
	}
	if den == 0 {
		return 0
	}
	return num / den
}

// Analyse computes the report from per-second mids and fair values.
// Returns are sampled every `every` seconds (10 is a good default: ~2,300
// observations in a 23,400 second day). jumps lists the seconds at which fair
// value jumped on news; the 180 seconds after each are masked out, because
// price adjusting to a 30% readout is not "drifting away from fair value".
func Analyse(mid []int64, fair []float64, every int, jumps []int64) Report {
	masked := make([]bool, len(mid))
	for _, j := range jumps {
		for t := j; t < j+180 && t < int64(len(mid)); t++ {
			if t >= 0 {
				masked[t] = true
			}
		}
	}
	var r []float64
	for i := every; i < len(mid); i += every {
		if masked[i] || masked[i-every] {
			continue
		}
		a, b := mid[i-every], mid[i]
		if a > 0 && b > 0 {
			r = append(r, math.Log(float64(b)/float64(a)))
		}
	}
	abs := make([]float64, len(r))
	for i, v := range r {
		abs[i] = math.Abs(v)
	}
	rep := Report{Samples: len(r)}
	rep.ExcessKurtosis = Kurtosis(r)
	rep.ReturnAC1 = Autocorr(r, 1)
	rep.ReturnAC2 = Autocorr(r, 2)
	rep.AbsAC1 = Autocorr(abs, 1)
	rep.AbsAC5 = Autocorr(abs, 5)
	rep.AbsAC10 = Autocorr(abs, 10)
	rep.AbsAC20 = Autocorr(abs, 20)
	rep.DailyVolPct = math.Sqrt(variance(r)) * math.Sqrt(float64(len(mid))/float64(every)) * 100

	// Mean reversion: regress the next sampled return on the current gap.
	var sxy, sxx float64
	maxGap, sumGap, n := 0.0, 0.0, 0
	for i := every; i+every < len(mid); i += every {
		if fair[i] <= 0 || mid[i] <= 0 || masked[i] || masked[i+every] {
			continue
		}
		gap := (fair[i]*100 - float64(mid[i])) / (fair[i] * 100)
		next := math.Log(float64(mid[i+every]) / float64(mid[i]))
		sxy += gap * next
		sxx += gap * gap
		ag := math.Abs(gap)
		if ag > maxGap {
			maxGap = ag
		}
		sumGap += ag
		n++
	}
	if sxx > 0 {
		rep.RevertBeta = sxy / sxx
	}
	rep.MaxGapPct = maxGap * 100
	if n > 0 {
		rep.MeanAbsGapPct = sumGap / float64(n) * 100
	}

	check := func(ok bool, msg string) {
		if !ok {
			rep.Failures = append(rep.Failures, msg)
		}
	}
	check(rep.ExcessKurtosis > 1.0, fmt.Sprintf("fat tails: excess kurtosis %.2f should exceed 1.0", rep.ExcessKurtosis))
	check(rep.AbsAC1 > 0.05 && rep.AbsAC5 > 0 && rep.AbsAC10 > -0.08, fmt.Sprintf("volatility clustering: |r| autocorr lag1 %.3f lag5 %.3f lag10 %.3f should stay positive", rep.AbsAC1, rep.AbsAC5, rep.AbsAC10))
	check(math.Abs(rep.ReturnAC1) < 0.15, fmt.Sprintf("return autocorr lag1 %.3f should be near zero (|ac| < 0.15)", rep.ReturnAC1))
	check(rep.RevertBeta > 0, fmt.Sprintf("mean reversion to fair value: beta %.4f should be positive", rep.RevertBeta))
	check(rep.MaxGapPct < 8, fmt.Sprintf("price never drifts off: max gap to fair %.2f%% should stay under 8%%", rep.MaxGapPct))
	rep.Pass = len(rep.Failures) == 0
	return rep
}

// String renders a human-readable report.
func (r Report) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "samples           %d\n", r.Samples)
	fmt.Fprintf(&b, "excess kurtosis   %.2f   (fat tails: want > 1)\n", r.ExcessKurtosis)
	fmt.Fprintf(&b, "ret autocorr      lag1 %+.3f  lag2 %+.3f   (want ~0)\n", r.ReturnAC1, r.ReturnAC2)
	fmt.Fprintf(&b, "|ret| autocorr    lag1 %+.3f  lag5 %+.3f  lag10 %+.3f  lag20 %+.3f   (clustering: want > 0, slow decay)\n", r.AbsAC1, r.AbsAC5, r.AbsAC10, r.AbsAC20)
	fmt.Fprintf(&b, "reversion beta    %+.4f   (want > 0)\n", r.RevertBeta)
	fmt.Fprintf(&b, "gap to fair       mean %.2f%%  max %.2f%%\n", r.MeanAbsGapPct, r.MaxGapPct)
	fmt.Fprintf(&b, "realised vol      %.2f%% / day\n", r.DailyVolPct)
	if r.Pass {
		b.WriteString("RESULT            PASS\n")
	} else {
		b.WriteString("RESULT            FAIL\n")
		for _, f := range r.Failures {
			fmt.Fprintf(&b, "  - %s\n", f)
		}
	}
	return b.String()
}
