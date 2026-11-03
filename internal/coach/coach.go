// Package coach produces the post-session review. It is two Claude calls:
// a cheap tagging pass (one call per player) and a narrative pass with the
// rubric cached as a stable prefix. Without an API key it falls back to a
// deterministic local narrative so the game still works offline.
package coach

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"

	"github.com/anujkumarsingh-code/tradeforces/internal/scoring"
)

// Input is everything the coach may look at.
type Input struct {
	Player  string         `json:"player"`
	Ticker  string         `json:"ticker"`
	Company string         `json:"company"`
	Brief   string         `json:"brief"`
	Result  scoring.Result `json:"result"`
	News    []string       `json:"news"`
	ClosePx float64        `json:"close_px"`
	StartPx float64        `json:"start_px"`
	FairEnd float64        `json:"fair_end"`
}

// TradeTag is the tagging pass output for one episode.
type TradeTag struct {
	Episode int      `json:"episode"`
	Tags    []string `json:"tags"`
	Note    string   `json:"note"`
}

// Review is the finished coaching output.
type Review struct {
	Narrative string     `json:"narrative"`
	Tags      []TradeTag `json:"tags"`
	Source    string     `json:"source"` // "claude" or "local"
	Models    []string   `json:"models,omitempty"`
	CostUSD   float64    `json:"cost_usd"`
	Elapsed   string     `json:"elapsed"`
	Error     string     `json:"error,omitempty"`
}

// Coach holds the client and model choice.
type Coach struct {
	client   *anthropic.Client
	enabled  bool
	tagModel string
	narModel string
}

// New reads ANTHROPIC_API_KEY (and optional COACH_TAG_MODEL / COACH_MODEL).
// The build plan budgets Haiku for tagging and Sonnet with caching for the
// narrative; both are overridable.
func New() *Coach {
	c := &Coach{
		tagModel: envOr("COACH_TAG_MODEL", "claude-haiku-5-5"),
		narModel: envOr("COACH_MODEL", "claude-sonnet-5-5"),
	}
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		cl := anthropic.NewClient(option.WithAPIKey(key), option.WithMaxRetries(2))
		c.client = &cl
		c.enabled = true
	}
	return c
}

// Enabled reports whether Claude calls will be made.
func (c *Coach) Enabled() bool { return c.enabled }

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// Review runs both stages, falling back to the local narrative on any error.
func (c *Coach) Review(ctx context.Context, in Input) Review {
	t0 := time.Now()
	if !c.enabled {
		r := local(in)
		r.Elapsed = time.Since(t0).Round(time.Millisecond).String()
		return r
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	rev := Review{Source: "claude", Models: []string{c.tagModel, c.narModel}}
	tags, cost1, err := c.tag(ctx, in)
	if err != nil {
		r := local(in)
		r.Error = "tagging stage failed: " + err.Error()
		r.Elapsed = time.Since(t0).Round(time.Millisecond).String()
		return r
	}
	rev.Tags = tags
	narrative, cost2, err := c.narrate(ctx, in, tags)
	if err != nil {
		r := local(in)
		r.Tags = tags
		r.Error = "narrative stage failed: " + err.Error()
		r.Elapsed = time.Since(t0).Round(time.Millisecond).String()
		return r
	}
	rev.Narrative = narrative
	rev.CostUSD = cost1 + cost2
	rev.Elapsed = time.Since(t0).Round(time.Millisecond).String()
	return rev
}

const tagSystem = `You tag trades from a simulated trading session for a coaching product. You receive a JSON list of round-trip episodes with hidden-state fields the trader could not see (fair value at entry, entry gap to fair in percent, whether a stop rested, nearby news). Return ONLY a JSON array, no prose, one object per episode: {"episode": n, "tags": [...], "note": "one short factual sentence"}.
Allowed tags: chased_breakout, faded_panic, bought_below_fair, sold_above_fair, no_stop, oversized, held_through_news, revenge_trade, averaged_down, scalped_spread, cut_winner_early, let_loser_run, stopped_out, disciplined_exit, open_at_close.
Be mechanical. Use only what the data supports.`

const narrativeRubric = `You are the post-session coach for an order-flow trading simulator. The market in the game is a real limit order book moved by bot agents: market makers, noise traders who chase momentum, medium agents with stops at round numbers, and informed pros who trade toward a hidden fair value and see news a few seconds early. The player's score is 40% return, 30% risk discipline (return over max drawdown, penalised for oversizing), 30% process.

Your job is to critique PROCESS, never to recommend positions. You never say "buy" or "sell" about anything real; this is a game with fictional companies. Never make performance claims about real markets.

Teaching style:
- Speak directly to the player in second person, plain language, no jargon without a gloss.
- Lead with the one thing that mattered most this session (good or bad), with the specific trade and numbers that prove it.
- Then at most three further observations, each anchored to an episode number or a metric.
- A player can finish green and still have traded badly; say so when true. A player can lose money and have traded well; say so when true.
- Where a mistake is in the taxonomy (revenge trading, overtrading, no stop, chasing, averaging down, cutting winners, oversizing, paying the spread), name it and explain the mechanism in the simulated market that punished it or would have.
- Close with one concrete drill for the next session: a single rule to follow, measurable from the event log.
- 180 to 280 words. Markdown with short paragraphs and at most one bullet list. No headings. No praise padding.
- Mention that the entry-gap and fair-value numbers come from hidden simulator state no real brokerage could show them, when that is relevant.`

func (c *Coach) tag(ctx context.Context, in Input) ([]TradeTag, float64, error) {
	if len(in.Result.EpisodeList) == 0 {
		return nil, 0, nil
	}
	payload, _ := json.Marshal(in.Result.EpisodeList)
	resp, err := c.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(c.tagModel),
		MaxTokens: 4000,
		System:    []anthropic.TextBlockParam{{Text: tagSystem}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("Episodes (times are simulated seconds; 20 simulated seconds is one real second):\n" + string(payload))),
		},
	})
	if err != nil {
		return nil, 0, err
	}
	if resp.StopReason == anthropic.StopReasonRefusal {
		return nil, 0, errors.New("tagging call was refused")
	}
	text := textOf(resp)
	var tags []TradeTag
	if err := json.Unmarshal([]byte(stripFence(text)), &tags); err != nil {
		return nil, 0, fmt.Errorf("tag JSON: %w", err)
	}
	return tags, costOf(c.tagModel, resp.Usage), nil
}

func (c *Coach) narrate(ctx context.Context, in Input, tags []TradeTag) (string, float64, error) {
	summary := map[string]any{
		"player":      in.Player,
		"instrument":  fmt.Sprintf("%s (%s)", in.Ticker, in.Company),
		"brief":       in.Brief,
		"news":        in.News,
		"start_price": in.StartPx,
		"close_price": in.ClosePx,
		"fair_at_end": in.FairEnd,
		"score":       in.Result.Score,
		"components":  map[string]float64{"return": in.Result.ReturnScore, "risk": in.Result.RiskScore, "process": in.Result.ProcessScore},
		"metrics": map[string]any{
			"return_pct": in.Result.ReturnPct, "max_drawdown_pct": in.Result.MaxDrawdown,
			"fills": in.Result.Fills, "round_trips": in.Result.Episodes, "win_rate": in.Result.WinRate,
			"avg_hold_win_real_sec": in.Result.AvgHoldWin / 20, "avg_hold_loss_real_sec": in.Result.AvgHoldLoss / 20,
			"avg_entry_gap_to_fair_pct": in.Result.AvgEntryGap, "aggressor_fill_pct": in.Result.AggressorPct,
			"declared_limit": in.Result.Limit, "max_position": in.Result.MaxPos,
			"max_unprotected_real_sec": in.Result.UnprotectedS / 20,
		},
		"detected_mistakes": in.Result.Mistakes,
		"strengths":         in.Result.Strengths,
		"calibration":       in.Result.Calibration,
		"episodes":          in.Result.EpisodeList,
		"trade_tags":        tags,
	}
	payload, _ := json.MarshalIndent(summary, "", " ")
	resp, err := c.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(c.narModel),
		MaxTokens: 2000,
		// The rubric is the stable prefix: cached after the first review of
		// the day, so each later review pays cache-read prices for it.
		System: []anthropic.TextBlockParam{{
			Text:         narrativeRubric,
			CacheControl: anthropic.NewCacheControlEphemeralParam(),
		}},
		OutputConfig: anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffortLow},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("Write the review for this session.\n\n" + string(payload))),
		},
	})
	if err != nil {
		return "", 0, err
	}
	if resp.StopReason == anthropic.StopReasonRefusal {
		return "", 0, errors.New("narrative call was refused")
	}
	return strings.TrimSpace(textOf(resp)), costOf(c.narModel, resp.Usage), nil
}

func textOf(m *anthropic.Message) string {
	var b strings.Builder
	for _, block := range m.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

func stripFence(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
	}
	return strings.TrimSpace(s)
}

// costOf estimates spend from list prices per million tokens.
func costOf(model string, u anthropic.Usage) float64 {
	in, out, cacheRead, cacheWrite := 0.0, 0.0, 0.0, 0.0
	switch {
	case strings.Contains(model, "haiku"):
		in, out, cacheRead, cacheWrite = 0.10, 0.50, 0.01, 0.125
	case strings.Contains(model, "sonnet"):
		in, out, cacheRead, cacheWrite = 2.0, 10.0, 0.20, 2.5
	case strings.Contains(model, "opus"):
		in, out, cacheRead, cacheWrite = 4.0, 20.0, 0.20, 5.0
	default:
		in, out, cacheRead, cacheWrite = 10.0, 50.0, 0.25, 12.5
	}
	return (float64(u.InputTokens)*in + float64(u.OutputTokens)*out +
		float64(u.CacheReadInputTokens)*cacheRead + float64(u.CacheCreationInputTokens)*cacheWrite) / 1e6
}

// local writes a deterministic review from the computed metrics.
func local(in Input) Review {
	r := in.Result
	var b strings.Builder
	if r.Episodes == 0 {
		b.WriteString("You did not complete a round trip this session, so there is nothing to score beyond a flat return. ")
		b.WriteString("Next session: place one small position with a stop before the first scheduled headline, and let the review tell you what it did.")
		return Review{Narrative: b.String(), Source: "local"}
	}
	fmt.Fprintf(&b, "You finished %+.2f%% with a max drawdown of %.2f%% across %d round trip%s (%d fills). ", r.ReturnPct, r.MaxDrawdown, r.Episodes, plural(r.Episodes), r.Fills)
	if r.ReturnPct > 0 && r.ProcessScore < 60 {
		b.WriteString("You were green, but the process score says the result was not earned: the habits below lose money over many sessions even when one session pays. ")
	} else if r.ReturnPct <= 0 && r.ProcessScore >= 75 {
		b.WriteString("You lost money while trading well. That happens; the process is what compounds. ")
	}
	if len(r.Mistakes) > 0 {
		m := r.Mistakes[0]
		fmt.Fprintf(&b, "\n\nThe thing that mattered most: **%s**. %s ", m.Title, m.Detail)
		b.WriteString(mechanism(m.Tag))
		if len(r.Mistakes) > 1 {
			b.WriteString("\n\nAlso detected:\n")
			for _, x := range r.Mistakes[1:] {
				if x.Tag == m.Tag {
					continue
				}
				fmt.Fprintf(&b, "- **%s**: %s\n", x.Title, x.Detail)
			}
		}
	} else {
		b.WriteString("\n\nNo entry in the mistake taxonomy fired this session. ")
	}
	if r.AvgEntryGap != 0 {
		fmt.Fprintf(&b, "\n\nYour average entry was %.2f%% relative to the hidden fair value (negative means you bought below it or sold above it). Only the simulator can show you that number; no brokerage can. ", r.AvgEntryGap)
	}
	for _, s := range r.Strengths {
		fmt.Fprintf(&b, "%s ", s)
	}
	if r.Calibration != nil {
		fmt.Fprintf(&b, "\n\nCalibration: you claimed %.0f%% confidence on average and were right %.0f%% of the time. %s ", r.Calibration.AvgClaimed, r.Calibration.HitRate, r.Calibration.Description)
	}
	b.WriteString("\n\nDrill for next session: ")
	b.WriteString(drill(r))
	return Review{Narrative: b.String(), Source: "local"}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func mechanism(tag string) string {
	switch tag {
	case "revenge":
		return "In this market the move that just stopped you out was usually the crowd overshooting; re-entering bigger and faster meant you joined the overshoot at its worst price."
	case "no_stop":
		return "Stops from the medium agents cluster at round numbers, and when a cascade fires the book thins in seconds. Without a resting stop you were exposed to exactly that air pocket."
	case "overtrading":
		return "Every market order paid the makers' spread, which widens the moment volatility rises. Churn hands your edge to the market makers."
	case "chasing":
		return "By the time price was that far through fair value, the pro agents were already working the other side. You were providing their exit."
	case "averaging_down":
		return "Adding to a loser bets that fair value is where you think it is; the informed flow that was pushing against you knew otherwise."
	case "cut_winners":
		return "Trends in this market self-reinforce while the crowd chases; the winners you cut were usually still being pushed your way."
	case "oversizing":
		return "The limit you set pre-market was your own risk budget. Breaking it is the habit that turns one bad session into a blown account."
	case "paying_spread":
		return "A limit order one tick inside the touch would usually have filled within seconds in this book."
	case "held_through_news":
		return "Makers pull their quotes on a headline and pros sweep the book; the first prints after news are the worst prices of the day."
	}
	return ""
}

func drill(r scoring.Result) string {
	if len(r.Mistakes) == 0 {
		return "Halve your usual size and see whether your entry gap to fair value improves; smaller size makes patience cheaper."
	}
	switch r.Mistakes[0].Tag {
	case "no_stop", "held_through_news":
		return "Every entry gets a stop within 10 real seconds, before anything else. The log will show whether you kept to it."
	case "revenge":
		return "After any losing exit, no new order for 60 real seconds. Count the number of times you break it."
	case "overtrading":
		return "A maximum of five round trips. Make each one carry a thesis you would say out loud."
	case "chasing":
		return "Only enter with a limit order that rests at least two ticks inside the current price; if it does not fill, you were chasing."
	case "averaging_down":
		return "Never add to a position that is underwater. Add only to winners, and only once."
	case "cut_winners":
		return "Set a target at twice your stop distance and do not touch a winner before it reaches the target or the stop."
	case "oversizing":
		return "Declare your limit, then trade at half of it. Size is the one variable you fully control."
	case "paying_spread":
		return "No market orders for the whole session. Limit orders only, and watch how many fill anyway."
	}
	return "Pick one rule from your review and track it across three sessions."
}
