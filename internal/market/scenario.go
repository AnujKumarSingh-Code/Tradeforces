package market

import (
	"fmt"
	"math/rand/v2"
)

// EventKind classifies a news event.
type EventKind string

const (
	Earnings EventKind = "earnings"
	Macro    EventKind = "macro"
	Analyst  EventKind = "analyst"
	Rumour   EventKind = "rumour"
)

// EventTemplate is a scenario-authored event whose direction and size are
// resolved per session from the seed.
type EventTemplate struct {
	At        int64     // simulated seconds after the open
	Kind      EventKind // category shown on the economic calendar
	Label     string    // calendar label (what, not which way)
	Good      string    // headline if the resolved impact is positive
	Bad       string    // headline if the resolved impact is negative
	MinImpact float64   // absolute fraction, e.g. 0.02
	MaxImpact float64
	LeakLead  int64   // seconds before At that pro agents learn the outcome
	Scheduled bool    // false for rumours: not on the calendar
	Bias      float64 // probability the outcome is positive
}

// Event is a resolved, session-specific news event.
type Event struct {
	ID        int       `json:"id"`
	At        int64     `json:"at"`
	LeakAt    int64     `json:"-"`
	Kind      EventKind `json:"kind"`
	Label     string    `json:"label"`
	Headline  string    `json:"headline"`
	Impact    float64   `json:"-"`
	Scheduled bool      `json:"scheduled"`
	Fired     bool      `json:"-"`
	Leaked    bool      `json:"-"`
}

// Scenario describes one fictional instrument and its session script.
type Scenario struct {
	Ticker     string
	Company    string
	Sector     string
	Brief      string
	StartPrice float64
	DailyVol   float64
	Events     []EventTemplate
}

// Library is the hand-authored scenario set. Fictional companies only.
var Library = []Scenario{
	{
		Ticker: "NVLX", Company: "Novalux Photonics", Sector: "Semiconductors",
		Brief:      "Novalux designs optical interconnect chips for data centres. Revenue has doubled two years running, the stock trades at 40x sales, and a quarterly report lands mid-session. Half the street thinks the next guide is a beat; the other half thinks hyperscaler capex is about to pause.",
		StartPrice: 142.50, DailyVol: 0.035,
		Events: []EventTemplate{
			{At: 3600, Kind: Macro, Label: "Factory orders print", Good: "Factory orders beat: tech capex running hotter than expected", Bad: "Factory orders miss: capex cycle shows first signs of cooling", MinImpact: 0.006, MaxImpact: 0.015, LeakLead: 90, Scheduled: true, Bias: 0.5},
			{At: 9000, Kind: Rumour, Label: "", Good: "Chatter: Novalux rumoured to be in supply talks with a top-three hyperscaler", Bad: "Chatter: Novalux rumoured to have lost a key qualification at a hyperscaler", MinImpact: 0.0, MaxImpact: 0.012, LeakLead: 60, Scheduled: false, Bias: 0.5},
			{At: 13200, Kind: Earnings, Label: "Q3 earnings release", Good: "Novalux crushes Q3: revenue +118% y/y, raises full-year guide", Bad: "Novalux misses Q3 and guides below consensus on a hyperscaler digestion pause", MinImpact: 0.04, MaxImpact: 0.09, LeakLead: 150, Scheduled: true, Bias: 0.5},
			{At: 18600, Kind: Analyst, Label: "Analyst day commentary", Good: "Harbour Street upgrades Novalux to Buy after earnings call", Bad: "Harbour Street cuts Novalux to Hold: 'valuation leaves no room for error'", MinImpact: 0.01, MaxImpact: 0.025, LeakLead: 120, Scheduled: true, Bias: 0.5},
		},
	},
	{
		Ticker: "BRKW", Company: "Brookwater Foods", Sector: "Consumer staples",
		Brief:      "Brookwater makes private-label cereal and snacks for grocery chains. It is a slow, dividend-paying stock that rarely moves more than a percent a day. Today a macro inflation print and an antitrust ruling on its biggest customer's merger are both due.",
		StartPrice: 58.20, DailyVol: 0.014,
		Events: []EventTemplate{
			{At: 2700, Kind: Macro, Label: "CPI inflation print", Good: "CPI cooler than forecast; staples bid as rate-cut odds rise", Bad: "CPI runs hot; input-cost worries hit packaged food", MinImpact: 0.005, MaxImpact: 0.012, LeakLead: 90, Scheduled: true, Bias: 0.5},
			{At: 10800, Kind: Macro, Label: "Merger ruling on largest customer", Good: "Regulator clears the grocery merger; Brookwater seen gaining shelf space", Bad: "Regulator blocks the grocery merger; Brookwater's volume outlook clouded", MinImpact: 0.015, MaxImpact: 0.035, LeakLead: 120, Scheduled: true, Bias: 0.5},
			{At: 16800, Kind: Rumour, Label: "", Good: "Chatter: activist fund said to be building a Brookwater stake", Bad: "Chatter: Brookwater said to be facing a product recall", MinImpact: 0.0, MaxImpact: 0.01, LeakLead: 60, Scheduled: false, Bias: 0.5},
			{At: 20400, Kind: Analyst, Label: "Sector note", Good: "Castle Point initiates Brookwater at Overweight: 'the safest yield in staples'", Bad: "Castle Point initiates Brookwater at Underweight on private-label margin pressure", MinImpact: 0.008, MaxImpact: 0.018, LeakLead: 100, Scheduled: true, Bias: 0.5},
		},
	},
	{
		Ticker: "HELX", Company: "Helixar Therapeutics", Sector: "Biotech",
		Brief:      "Helixar is a clinical-stage biotech with one lead drug for a rare liver disease. Phase 3 topline data is due during the session. The company has no revenue, a cash runway of 18 months, and a short interest above 30%. This will not be a quiet day.",
		StartPrice: 24.80, DailyVol: 0.06,
		Events: []EventTemplate{
			{At: 3000, Kind: Analyst, Label: "Pre-data analyst preview", Good: "Meridian sees 70% odds of a clean Phase 3 readout for Helixar", Bad: "Meridian warns Helixar's Phase 3 is 'a coin flip at best'", MinImpact: 0.01, MaxImpact: 0.03, LeakLead: 90, Scheduled: true, Bias: 0.5},
			{At: 7800, Kind: Rumour, Label: "", Good: "Chatter: Helixar trial site staff said to be 'in good spirits'", Bad: "Chatter: Helixar trial said to have an unexpected safety review", MinImpact: 0.0, MaxImpact: 0.02, LeakLead: 60, Scheduled: false, Bias: 0.5},
			{At: 12600, Kind: Earnings, Label: "Phase 3 topline data", Good: "HELIXAR PHASE 3 MEETS PRIMARY ENDPOINT; filing planned this year", Bad: "HELIXAR PHASE 3 FAILS PRIMARY ENDPOINT; company to 'evaluate options'", MinImpact: 0.12, MaxImpact: 0.30, LeakLead: 180, Scheduled: true, Bias: 0.5},
			{At: 15600, Kind: Macro, Label: "Biotech sector flow", Good: "Sector rotation into biotech after a soft rates print", Bad: "Biotech sells off as yields spike", MinImpact: 0.008, MaxImpact: 0.02, LeakLead: 90, Scheduled: true, Bias: 0.5},
			{At: 19800, Kind: Analyst, Label: "Post-data rating changes", Good: "Three brokers raise Helixar targets after the readout", Bad: "Three brokers slash Helixar targets after the readout", MinImpact: 0.02, MaxImpact: 0.05, LeakLead: 120, Scheduled: true, Bias: 0.5},
		},
	},
	{
		Ticker: "ARDN", Company: "Ardent Mobility", Sector: "Autos / EV",
		Brief:      "Ardent builds electric delivery vans and just started its second factory. It is a crowded, volatile name that retail traders love. Monthly delivery numbers come out this morning and a central-bank rate decision hits in the afternoon.",
		StartPrice: 31.40, DailyVol: 0.045,
		Events: []EventTemplate{
			{At: 2400, Kind: Earnings, Label: "Monthly deliveries", Good: "Ardent deliveries +34% m/m, well ahead of the whisper number", Bad: "Ardent deliveries flat m/m as the second factory ramp stalls", MinImpact: 0.03, MaxImpact: 0.07, LeakLead: 120, Scheduled: true, Bias: 0.5},
			{At: 8400, Kind: Rumour, Label: "", Good: "Chatter: a large logistics firm said to be close to a fleet order with Ardent", Bad: "Chatter: Ardent said to be preparing a dilutive capital raise", MinImpact: 0.0, MaxImpact: 0.025, LeakLead: 60, Scheduled: false, Bias: 0.5},
			{At: 14400, Kind: Macro, Label: "Rate decision", Good: "Central bank cuts 25bp and signals more to come; growth names rally", Bad: "Central bank holds and talks tough; growth names sold", MinImpact: 0.012, MaxImpact: 0.03, LeakLead: 90, Scheduled: true, Bias: 0.5},
			{At: 20100, Kind: Analyst, Label: "Broker note", Good: "Oakline reiterates Ardent as a top pick into year end", Bad: "Oakline downgrades Ardent: 'the easy part of the ramp is over'", MinImpact: 0.01, MaxImpact: 0.025, LeakLead: 120, Scheduled: true, Bias: 0.5},
		},
	},
}

// Pick returns a scenario by index (wrapping) so a seed selects it.
func Pick(i int) Scenario {
	return Library[((i%len(Library))+len(Library))%len(Library)]
}

// Resolve turns the templates into session events using the seed.
func (s Scenario) Resolve(rng *rand.Rand) []Event {
	out := make([]Event, 0, len(s.Events))
	for i, t := range s.Events {
		size := t.MinImpact + rng.Float64()*(t.MaxImpact-t.MinImpact)
		head := t.Good
		if rng.Float64() >= t.Bias {
			size = -size
			head = t.Bad
		}
		if t.Kind == Rumour && rng.Float64() < 0.4 {
			// Rumours are sometimes just noise: a headline with no fair-value change.
			size = 0
		}
		lead := t.LeakLead
		if lead > t.At {
			lead = t.At / 2
		}
		label := t.Label
		if label == "" {
			label = fmt.Sprintf("Unscheduled %s", t.Kind)
		}
		out = append(out, Event{
			ID: i + 1, At: t.At, LeakAt: t.At - lead, Kind: t.Kind, Label: label,
			Headline: head, Impact: size, Scheduled: t.Scheduled,
		})
	}
	return out
}
