// Command sim runs a headless session and checks the resulting price series
// against the stylised facts of real markets. It is the Phase 0 gate.
package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/anujkumarsingh-code/tradeforces/internal/engine"
	"github.com/anujkumarsingh-code/tradeforces/internal/stats"
)

func main() {
	seed := flag.Uint64("seed", 42, "session seed")
	runs := flag.Int("runs", 1, "number of seeds to run (seed, seed+1, ...)")
	scenario := flag.Int("scenario", -1, "scenario index, -1 derives from seed")
	noise := flag.Int("noise", 120, "noise traders")
	medium := flag.Int("medium", 30, "medium agents")
	pros := flag.Int("pros", 4, "pro agents")
	makers := flag.Int("makers", 3, "market makers")
	csvPath := flag.String("csv", "", "write per-second mid and fair value to this CSV")
	flag.Parse()

	passes := 0
	for i := 0; i < *runs; i++ {
		cfg := engine.DefaultConfig(*seed + uint64(i))
		cfg.ScenarioIndex = *scenario
		cfg.Noise, cfg.Medium, cfg.Pros, cfg.Makers = *noise, *medium, *pros, *makers
		e := engine.New(cfg)
		t0 := time.Now()
		e.RunHeadless()
		el := time.Since(t0)

		mid := e.MidHistory()
		fv := e.FairHistory()
		var jumps []int64
		for _, ev := range e.Events {
			if ev.Impact != 0 {
				jumps = append(jumps, ev.LeakAt)
			}
		}
		rep := stats.Analyse(mid, fv, 10, jumps)
		fmt.Printf("== %s\n", e.Describe())
		fmt.Printf("ran %d simulated seconds in %s, %d candles, %d shares traded, close %.2f (fair %.2f)\n",
			len(mid), el.Round(time.Millisecond), len(e.Candles), e.Book.Volume, float64(e.ClosePrice)/100, e.FairValue())
		fmt.Print(rep.String())
		if rep.Pass {
			passes++
		}
		if *csvPath != "" && i == 0 {
			writeCSV(*csvPath, mid, fv)
		}
	}
	fmt.Printf("\n%d/%d sessions passed\n", passes, *runs)
	if passes < *runs {
		os.Exit(1)
	}
}

func writeCSV(path string, mid []int64, fv []float64) {
	f, err := os.Create(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	defer f.Close()
	w := csv.NewWriter(f)
	w.Write([]string{"t", "mid", "fair"})
	for i := range mid {
		w.Write([]string{strconv.Itoa(i + 1), strconv.FormatFloat(float64(mid[i])/100, 'f', 2, 64), strconv.FormatFloat(fv[i], 'f', 4, 64)})
	}
	w.Flush()
}
