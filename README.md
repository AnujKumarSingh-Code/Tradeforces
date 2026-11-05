# Tradeforces

An order-flow trading game. Price is not scripted and not replayed: it emerges
from a real limit order book driven by four tiers of bot agents, and the
player's edge comes from learning to read that flow. After every 20-minute
session a coach scores your *process*, not just your P&L, and tells you what
you did wrong in your own trades.

Backend is Go, end to end: matching engine, agent runtime, session
orchestrator, server-authoritative accounts, event log, scoring, Claude coach
and the WebSocket gateway. The client is a single embedded page with
TradingView's Lightweight Charts. One binary, no database, no build step.

```
go run ./cmd/server          # open http://localhost:8080
go run ./cmd/sim -runs 12    # Phase 0 realism gate, headless
go test ./...
```

Set `ANTHROPIC_API_KEY` to turn on the Claude coach. Without it the game still
runs and the review is written by local rules.

## How price forms

One hidden number drives everything: **fair value**, a slow-drifting process
with a volatility regime that jumps on news. Nobody sees it directly.

| Tier | Count | Knows | Trades |
|---|---|---|---|
| Market makers | 3 | Nothing directional | Five quote levels each side around a smoothed trade price; widen with volatility, skew to unwind inventory, pull quotes on shock news |
| Noise traders | 120 | The last few prices | Chase green, panic on red, market orders of 1-20 shares every 1-5 s, more active when the tape moves, never set a stop |
| Medium agents | 30 | A 2-minute-delayed, blurred fair value plus moving averages | Trend-follow and mean-revert with limit orders; stops cluster at half-dollar round numbers and fire as market orders |
| Pro agents | 4 | Fair value, and news 1-3 minutes (sim) before the headline | Work 5k-50k parent orders in 100-1k child slices at the touch; sweep the book when the gap is a real news gap |

Pros learn an event's outcome at `LeakAt`; the public headline prints at `At`.
The flow in between is the tell players learn to read.

Everything is deterministic: a session is a seed plus the ordered human
actions, and `engine.New(cfg).RunHeadless()` reproduces it tick for tick
(`internal/engine/engine_test.go`). The event log is written to
`data/sessions/<id>.jsonl` and served at `/api/replay?session=<id>`.

## Phase 0 gate: does it behave like a market?

`cmd/sim` runs a headless day (23,400 simulated seconds, ~1 s wall clock) and
checks the mid series against the stylised facts, with the 3 minutes after
each fair-value jump masked out (price adjusting to a 30% readout is not
"drifting away"):

* fat tails: excess kurtosis of 10-second returns > 1
* volatility clustering: |return| autocorrelation positive at lags 1 and 5
* no return autocorrelation: |lag-1| < 0.15
* mean reversion to fair value: positive regression of next return on the gap
* bounded gap: price never more than 8% from fair outside news windows

Current calibration passes 8 of 12 seeds; the misses are marginal (lag-1
autocorrelation of ±0.15-0.22, or |r| autocorrelation that decays slightly
below zero by lag 10). Tuning levers are in `internal/agents`: noise
`chase`/`activity`, pro `threshold`/`patience`, maker `volMult`/`invScale`,
and the fair-value volatility regime in `internal/market/fair.go`. Run
`make gate` after any change.

## Session format

Sessions run back to back. One real second is 20 simulated seconds; a candle
prints every 5 simulated minutes, so a day builds 78 candles.

1. **Pre-market, 60 s.** Instrument, one-paragraph brief, 30 prior days,
   starting cash ($100,000), the economic calendar (when, not what), and your
   self-declared position limit. The score penalises positions above it.
2. **Open, ~19.5 min.** Limit, market and stop orders. Market orders carry a
   0.5% price collar. Pre-trade thesis and confidence are optional and scored
   for calibration.
3. **Close.** A visible 2-minute countdown; open positions are marked at the
   closing mid.
4. **Review.** Score, leaderboard, detected patterns, coach narrative, replay
   log download. The next round starts 90 s later.

Flags: `-speed`, `-session`, `-premarket`, `-results`, `-seed`, `-data`,
`-addr`. For a quick 2-minute round: `./bin/server -session 2400 -premarket 10s`.

## Scoring

| Component | Weight | What it measures |
|---|---|---|
| Return | 40% | Net P&L against starting capital (+5% on the day scores 100) |
| Risk discipline | 30% | Drawdown taken and the return it bought, minus 15 per oversized position |
| Process | 30% | The mistake taxonomy below, plus credit for entering on the right side of fair value |

Every mistake is computed from the event log in `internal/scoring`, no model
involved: revenge trading, overtrading, no stop, chasing, averaging down,
cutting winners and holding losers, oversizing, paying the spread, holding
through news unprotected. The process component is only possible because the
engine owns the fair-value series: it knows whether you bought below fair or
chased above it, which no real brokerage can tell you.

## Where Claude fits

The LLM is the director, the code is the actor. Agents never call a model.
The coach is two calls per player per session (`internal/coach`):

1. **Tagging** (`claude-haiku-5-5`): all of the player's round trips in one
   call, structured tags back.
2. **Narrative** (`claude-sonnet-5-5`): the rubric and teaching style sit in a
   cached system prefix; the tags, metrics and episodes go in the user turn.

If the key is injected by an outbound proxy instead (for example a Claude
Code cloud environment network secret with header `x-api-key` for
`api.anthropic.com`), set `COACH=on` so the coach sends requests without a
key of its own. `COACH_BASE_URL` overrides the API host.
Override models with `COACH_TAG_MODEL` and `COACH_MODEL`. To exercise the
real calls without playing a round, `go run ./cmd/coachtest` runs a headless
session with a scripted trader who commits most of the taxonomy, then prints
the tags, the narrative and the estimated cost (it exits non-zero if a key is
set and the Claude path fails). The review screen
shows the estimated cost of each review (about $0.01-0.02). On the $1,000
credit plan this is roughly 50,000 player-sessions of coaching. Set a spend
limit in the Console before exposing it to the public.

## Layout

```
cmd/server          gateway binary (HTTP + WebSocket + embedded client)
cmd/sim             headless harness and realism gate
internal/book       limit order book, price-time priority, integer cents
internal/market     fair-value process, scenario library, prior history
internal/agents     the four agent tiers
internal/engine     session engine: clock, news, accounts, stops, event log
internal/stats      stylised-fact tests
internal/scoring    episodes, mistake taxonomy, three-part score, calibration
internal/coach      two-stage Claude coach with local fallback
internal/server     lobby lifecycle, auth, order validation, fan-out
web/                index.html, app.js, style.css, vendored Lightweight Charts
```

## API

All actions are `POST` JSON with `X-Player-Token`. Prices are integer cents.

| Route | Body |
|---|---|
| `POST /api/join` | `{name}` or `{token}` |
| `POST /api/limit` | `{limit}` pre-market only |
| `POST /api/thesis` | `{thesis, dir, confidence}` |
| `POST /api/order` | `{side, type, price, qty}` |
| `POST /api/cancel` | `{id}` |
| `POST /api/stop` / `POST /api/stop/cancel` | `{side, price, qty}` / `{id}` |
| `GET /api/state` | full snapshot for the client |
| `GET /api/review?session=` | score, review, leaderboard |
| `GET /api/history`, `GET /api/replay?session=` | past sessions, event log |
| `GET /ws?token=` | 10 Hz frames: book depth, tape, candle, news, board, your account, your fills |

## What is deliberately not here yet

This is the "playable sooner" cut from the build plan: one instrument, one
format, in-memory accounts and leaderboard, no login, no persistence beyond
the event-log files. Next in order: Postgres for the event log and accounts,
classroom rooms with a join code, deterministic replay in the client, the
share card, and batch-generated scenario packs.

## Honesty note

Fictional companies only. The coach critiques process and never recommends
positions. Real fills are worse at size, fees and taxes are real, markets gap
overnight, and nothing here reproduces how it feels to lose money you need.

Lightweight Charts is Apache-2.0, TradingView, Inc.; see
`web/LICENSE.lightweight-charts`.
