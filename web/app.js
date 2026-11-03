/* Tradeforces client: one WebSocket for state, plain fetches for actions.
   The server is authoritative for everything; this file only renders. */
(() => {
  const $ = (id) => document.getElementById(id);
  const fmt$ = (c) => (c / 100).toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
  const fmtD = (d, dp = 2) => d.toLocaleString(undefined, { minimumFractionDigits: dp, maximumFractionDigits: dp });
  const fmtN = (n) => n.toLocaleString();
  const BASE = Date.UTC(2026, 2, 2, 9, 30) / 1000; // simulated open, 09:30
  const simTime = (t) => BASE + t;
  const clock = (t) => { const s = 9 * 3600 + 30 * 60 + t; const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), x = s % 60; return `${String(h).padStart(2,'0')}:${String(m).padStart(2,'0')}:${String(x).padStart(2,'0')}`; };
  const mmss = (sec) => { sec = Math.max(0, Math.round(sec)); return `${Math.floor(sec / 60)}:${String(sec % 60).padStart(2, '0')}`; };

  const S = {
    token: localStorage.getItem('tf_token') || '',
    player: null, state: null, pub: null, me: null,
    side: 'buy', type: 'limit', thesis: null, thesisSent: false,
    session: null, preDismissed: false, resultShown: false, lastNewsCount: 0,
    knownCandles: 0, markers: [], myFills: [], ws: null, reviewTimer: null,
  };

  // ---------- networking ----------
  const api = async (path, body) => {
    const r = await fetch(path, {
      method: body === undefined ? 'GET' : 'POST',
      headers: { 'Content-Type': 'application/json', 'X-Player-Token': S.token },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(j.error || r.statusText);
    return j;
  };

  function connect() {
    if (S.ws) { try { S.ws.close(); } catch (_) {} }
    const proto = location.protocol === 'https:' ? 'wss' : 'ws';
    const ws = new WebSocket(`${proto}://${location.host}/ws?token=${encodeURIComponent(S.token)}`);
    S.ws = ws;
    ws.onmessage = (ev) => { const m = JSON.parse(ev.data); if (m.type === 'tick') onTick(m); };
    ws.onclose = () => setTimeout(connect, 1500);
  }

  async function loadState() {
    try {
      S.state = await api('/api/state');
      if (S.state.player) S.player = S.state.player;
      renderStatic();
      resetChart();
    } catch (e) { toast(e.message, 'err'); }
  }

  // ---------- chart ----------
  let chart, candles, volume, hist, histSeries;
  function makeCharts() {
    const opts = {
      layout: { background: { type: 'solid', color: 'transparent' }, textColor: '#8b97ad', fontFamily: 'JetBrains Mono' },
      grid: { vertLines: { color: 'rgba(255,255,255,.04)' }, horzLines: { color: 'rgba(255,255,255,.04)' } },
      rightPriceScale: { borderColor: '#1f2a3d' },
      timeScale: { borderColor: '#1f2a3d', timeVisible: true, secondsVisible: false, rightOffset: 4 },
      crosshair: { mode: 0, vertLine: { color: '#2a3850', labelBackgroundColor: '#172133' }, horzLine: { color: '#2a3850', labelBackgroundColor: '#172133' } },
      handleScroll: true, handleScale: true, autoSize: true,
    };
    chart = LightweightCharts.createChart($('chart'), opts);
    candles = chart.addCandlestickSeries({ upColor: '#34d399', downColor: '#fb7185', borderVisible: false, wickUpColor: '#34d399', wickDownColor: '#fb7185', priceFormat: { type: 'price', precision: 2, minMove: 0.01 } });
    volume = chart.addHistogramSeries({ priceFormat: { type: 'volume' }, priceScaleId: 'vol', color: 'rgba(124,156,255,.35)' });
    chart.priceScale('vol').applyOptions({ scaleMargins: { top: 0.82, bottom: 0 } });
    hist = LightweightCharts.createChart($('chartHist'), { ...opts, timeScale: { ...opts.timeScale, timeVisible: false } });
    histSeries = hist.addCandlestickSeries({ upColor: '#34d399', downColor: '#fb7185', borderVisible: false, wickUpColor: '#34d399', wickDownColor: '#fb7185' });
  }
  const bar = (c) => ({ time: simTime(c.t), open: c.o, high: c.h, low: c.l, close: c.c });
  const vbar = (c) => ({ time: simTime(c.t), value: c.v, color: c.c >= c.o ? 'rgba(52,211,153,.35)' : 'rgba(251,113,133,.35)' });

  function resetChart() {
    const st = S.state; if (!st) return;
    const done = st.candles || [];
    candles.setData(done.map(bar));
    volume.setData(done.map(vbar));
    S.knownCandles = done.length;
    S.markers = [];
    for (const n of st.news || []) addNewsMarker(n);
    histSeries.setData((st.prior || []).map(c => ({ time: simTime(c.t), open: c.o, high: c.h, low: c.l, close: c.c })));
    hist.timeScale().fitContent();
    chart.timeScale().fitContent();
  }
  function addNewsMarker(n) {
    S.markers.push({ time: simTime(n.at), position: 'aboveBar', color: n.kind === 'earnings' ? '#fbbf24' : '#7c9cff', shape: 'circle', text: n.kind.toUpperCase().slice(0, 8), size: 1 });
    applyMarkers();
  }
  function addFillMarker(f) {
    const ct = Math.floor(f.t / (S.state?.candle_seconds || 300)) * (S.state?.candle_seconds || 300);
    S.markers.push({ time: simTime(ct), position: f.side === 0 ? 'belowBar' : 'aboveBar', color: f.side === 0 ? '#34d399' : '#fb7185', shape: f.side === 0 ? 'arrowUp' : 'arrowDown', text: `${f.side === 0 ? 'B' : 'S'} ${f.qty}`, size: 1 });
    applyMarkers();
  }
  function applyMarkers() {
    S.markers.sort((a, b) => a.time - b.time);
    try { candles.setMarkers(S.markers); } catch (_) {}
  }
  function updateChart(pub) {
    if (pub.ncandles > S.knownCandles && pub.last_candle) {
      candles.update(bar(pub.last_candle)); volume.update(vbar(pub.last_candle));
      S.knownCandles = pub.ncandles;
    }
    const c = pub.candle;
    if (c && c.o > 0) { candles.update(bar(c)); volume.update(vbar(c)); }
    $('sO').textContent = c.o ? fmtD(c.o) : '—'; $('sH').textContent = c.h ? fmtD(c.h) : '—';
    $('sL').textContent = c.l ? fmtD(c.l) : '—'; $('sC').textContent = c.c ? fmtD(c.c) : '—';
    $('sV').textContent = fmtN(c.v || 0);
    $('sVol').textContent = fmtN(pub.volume || 0);
  }

  // ---------- render ----------
  function renderStatic() {
    const st = S.state, sc = st.scenario;
    $('ticker').textContent = sc.ticker; $('company').textContent = sc.company; $('sector').textContent = sc.sector;
    $('preTicker').textContent = sc.ticker; $('preCompany').textContent = sc.company; $('preBrief').textContent = sc.brief;
    $('preCash').textContent = '$' + fmtN(st.start_cash);
    const cal = (el) => {
      el.innerHTML = '';
      for (const c of st.calendar || []) {
        const d = document.createElement('div'); d.className = 'cal'; d.dataset.at = c.at;
        d.innerHTML = `<span class="t">${clock(c.at).slice(0, 5)}</span><span class="k">${c.kind}</span><span>${c.label}</span>`;
        el.appendChild(d);
      }
    };
    cal($('calendar')); cal($('preCalendar'));
    drawSpark(st.prior || []);
    if (S.player) { $('who').innerHTML = `<div class="avatar">${S.player.name[0].toUpperCase()}</div><span>${S.player.name}</span>`; }
    $('limitText').textContent = `coach: ${st.coach ? 'Claude' : 'local'}`;
  }

  function drawSpark(prior) {
    const cv = $('spark'); const ctx = cv.getContext('2d'); ctx.clearRect(0, 0, cv.width, cv.height);
    if (!prior.length) return;
    const xs = prior.map(p => p.c); const lo = Math.min(...xs), hi = Math.max(...xs); const w = cv.width, h = cv.height;
    const grad = ctx.createLinearGradient(0, 0, 0, h); grad.addColorStop(0, 'rgba(34,211,238,.35)'); grad.addColorStop(1, 'rgba(34,211,238,0)');
    ctx.beginPath();
    xs.forEach((v, i) => { const x = i / (xs.length - 1) * (w - 4) + 2, y = h - 6 - (v - lo) / (hi - lo || 1) * (h - 14); i ? ctx.lineTo(x, y) : ctx.moveTo(x, y); });
    ctx.strokeStyle = '#22d3ee'; ctx.lineWidth = 2; ctx.stroke();
    ctx.lineTo(w - 2, h); ctx.lineTo(2, h); ctx.closePath(); ctx.fillStyle = grad; ctx.fill();
    ctx.fillStyle = '#8b97ad'; ctx.font = '11px JetBrains Mono'; ctx.fillText(fmtD(xs[0]), 2, 11); ctx.textAlign = 'right'; ctx.fillText(fmtD(xs[xs.length - 1]), w - 2, 11); ctx.textAlign = 'left';
  }

  function onTick(m) {
    const pub = typeof m.pub === 'string' ? JSON.parse(m.pub) : m.pub; S.pub = pub; S.me = m.me || null;
    if (pub.session !== S.session) {
      S.session = pub.session; S.preDismissed = false; S.resultShown = false; S.lastNewsCount = 0; S.myFills = []; S.thesisSent = false;
      $('myFills').innerHTML = ''; $('resultModal').hidden = true;
      loadState();
    }
    renderPhase(pub); renderQuote(pub); renderLadder(pub); renderTape(pub); renderBoard(pub); renderNews(pub);
    if (S.state) updateChart(pub);
    renderMe();
    for (const f of (m.fills || [])) onFill(f);
    renderTicketHint(pub);
  }

  function renderPhase(pub) {
    const pill = $('phasePill'); let cls = pub.phase, text = pub.phase;
    if (pub.phase === 'live' && pub.phase_left < 120) { cls = 'closing'; text = 'closing'; }
    if (pub.phase === 'premarket') text = 'pre-market';
    pill.className = 'pill ' + cls; $('phaseText').textContent = text;
    $('simClock').textContent = clock(pub.now); $('phaseLeft').textContent = mmss(pub.phase_left);
    const b = $('banner');
    if (pub.phase === 'live' && pub.phase_left < 120) { b.hidden = false; b.className = 'banner closing'; b.textContent = `Closing auction in ${mmss(pub.phase_left)}. Open positions are marked at the close.`; }
    else if (pub.phase === 'premarket') { b.hidden = false; b.className = 'banner'; b.textContent = `Pre-market. The book goes live in ${mmss(pub.phase_left)}. Read the brief, set your limit.`; }
    else if (!S.bannerNews) b.hidden = true;
    // pre-market modal
    if (pub.phase === 'premarket' && !S.preDismissed && S.player && S.state) { $('preModal').hidden = false; $('preLeft').textContent = mmss(pub.phase_left); }
    else $('preModal').hidden = true;
    if (pub.phase === 'closed' && !S.resultShown && S.player) { S.resultShown = true; showResults(pub.session); }
    if (pub.phase === 'closed') $('resLeft').textContent = mmss(pub.phase_left);
    // calendar progress
    document.querySelectorAll('.cal').forEach(el => { const at = +el.dataset.at; el.classList.toggle('done', pub.now >= at); el.classList.toggle('soon', pub.now < at && at - pub.now < 600); });
  }

  function renderQuote(pub) {
    const px = pub.closed ? pub.close_px : (pub.last || pub.mid);
    $('last').textContent = px ? fmt$(px) : '—';
    const start = S.state?.scenario?.start; const chg = $('chg');
    if (start && px) { const d = px / 100 - start; chg.textContent = `${d >= 0 ? '+' : ''}${fmtD(d)} (${(d / start * 100).toFixed(2)}%)`; chg.className = 'chg mono ' + (d >= 0 ? 'up' : 'down'); }
    $('spread').textContent = pub.spread ? `spread ${fmt$(pub.spread)} (${(pub.spread / pub.mid * 100).toFixed(2)}%)` : 'spread —';
  }

  function renderLadder(pub) {
    const mine = new Set((S.me?.open || []).map(o => o.price));
    const cum = (lv) => { let s = 0; return lv.map(l => (s += l.q)); };
    const ca = cum(pub.asks || []), cb = cum(pub.bids || []);
    const max = Math.max(ca[ca.length - 1] || 0, cb[cb.length - 1] || 0, 1);
    const row = (l, i, cls, c) => `<div class="lvl ${cls}${mine.has(l.p) ? ' mine' : ''}" data-p="${l.p}"><i style="width:${(c[i] / max * 100).toFixed(1)}%"></i><span class="p">${fmt$(l.p)}</span><span class="q">${fmtN(l.q)}</span><span class="n">${l.n}</span></div>`;
    $('asks').innerHTML = (pub.asks || []).map((l, i) => row(l, i, 'ask', ca)).reverse().join('');
    $('bids').innerHTML = (pub.bids || []).map((l, i) => row(l, i, 'bid', cb)).join('');
    $('midRow').innerHTML = `<span class="muted">mid</span><span>${pub.mid ? fmt$(pub.mid) : '—'}</span><span class="muted">last</span><span>${pub.last ? fmt$(pub.last) : '—'}</span>`;
  }

  function renderTape(pub) {
    $('tape').innerHTML = (pub.tape || []).slice().reverse().map(t => `<div class="tr ${t.s === 0 ? 'b' : 's'}"><span class="t">${clock(t.t)}</span><span class="p">${fmt$(t.p)}</span><span class="q">${fmtN(t.q)}</span></div>`).join('');
  }

  function renderBoard(pub) {
    const me = S.player?.name;
    $('board').innerHTML = (pub.board || []).map((r, i) => `<div class="lb${r.name === me ? ' me' : ''}"><span class="rk">${i + 1}</span><span>${esc(r.name)}${r.pos ? ` <span class="muted small">${r.pos > 0 ? '+' : ''}${r.pos}</span>` : ''}</span><span class="eq">$${fmtN(Math.round(r.equity))}</span><span class="${r.ret >= 0 ? 'up' : 'down'} mono">${r.ret >= 0 ? '+' : ''}${r.ret.toFixed(2)}%</span></div>`).join('') || '<div class="empty">Nobody has joined this round yet</div>';
  }

  function renderNews(pub) {
    const news = pub.news || [];
    if (news.length !== S.lastNewsCount) {
      $('news').innerHTML = news.slice().reverse().map(n => `<div class="headline ${n.kind}"><span class="t">${clock(n.at).slice(0, 5)}</span>${esc(n.headline)}</div>`).join('');
      if (S.lastNewsCount > 0 || (news.length && S.state)) {
        const n = news[news.length - 1];
        if (S.state && !(S.state.news || []).some(x => x.id === n.id)) addNewsMarker(n);
        flashBanner(n.headline, n.kind);
      }
      S.lastNewsCount = news.length;
    }
  }
  function flashBanner(text, kind) {
    const b = $('banner'); b.hidden = false; b.className = 'banner news'; b.textContent = `${kind.toUpperCase()}: ${text}`;
    S.bannerNews = true; clearTimeout(S.bannerT); S.bannerT = setTimeout(() => { S.bannerNews = false; }, 9000);
  }

  function renderMe() {
    const me = S.me; const st = S.state;
    if (!me) { $('equity').textContent = '—'; $('pnlDay').textContent = S.player ? 'joining…' : 'spectating'; $('posKV').innerHTML = '<div class="empty">Join to trade</div>'; return; }
    const start = st?.start_cash || 100000; const pnl = me.equity - start;
    $('equity').textContent = '$' + fmtN(Math.round(me.equity));
    $('pnlDay').textContent = `${pnl >= 0 ? '+' : '−'}$${fmtN(Math.abs(Math.round(pnl)))} (${(pnl / start * 100).toFixed(2)}%)`; $('pnlDay').className = 'mono small ' + (pnl >= 0 ? 'up' : 'down');
    const kv = [['Position', `${me.pos > 0 ? '+' : ''}${fmtN(me.pos)}`, me.pos > 0 ? 'up' : me.pos < 0 ? 'down' : ''], ['Avg cost', me.pos ? fmtD(me.avg) : '—'], ['Unrealised', money(me.unrealized), me.unrealized >= 0 ? 'up' : 'down'], ['Realised', money(me.realized), me.realized >= 0 ? 'up' : 'down'], ['Cash', '$' + fmtN(Math.round(me.cash))], ['Max drawdown', me.max_dd.toFixed(2) + '%'], ['Fills', me.fills], ['Limit', fmtN(me.limit) + ' sh', Math.abs(me.pos) > me.limit ? 'warn' : '']];
    $('posKV').innerHTML = kv.map(([k, v, c]) => `<div><span class="k">${k}</span><span class="${c || ''}">${v}</span></div>`).join('');
    $('limitText').textContent = `limit ${fmtN(me.limit)} sh`;
    $('orders').innerHTML = (me.open || []).map(o => `<div class="ord"><button class="side ${o.side === 'buy' ? 'up' : 'down'}">${o.side.toUpperCase()}</button><span>${fmt$(o.price)}</span><span>${fmtN(o.remaining)}</span><span class="muted">${Math.round(o.age / 20)}s</span><button class="x" data-cancel="${o.id}">✕</button></div>`).join('') || '<div class="empty">No resting orders</div>';
    $('stops').innerHTML = (me.stops || []).map(s => `<div class="ord"><button class="side ${s.side === 0 ? 'up' : 'down'}">${s.side === 0 ? 'BUY STOP' : 'SELL STOP'}</button><span>${fmt$(s.trigger)}</span><span>${fmtN(s.qty)}</span><span></span><button class="x" data-stopcancel="${s.id}">✕</button></div>`).join('') || `<div class="empty">${me.pos ? 'Unprotected position. The log is counting.' : 'No stops.'}</div>`;
    if (!$('stopQty').value && me.pos) $('stopQty').placeholder = String(Math.abs(me.pos));
    $('flatten').disabled = !me.pos;
  }
  const money = (v) => `${v < 0 ? '−' : ''}$${fmtN(Math.abs(Math.round(v)))}`;

  function onFill(f) {
    S.myFills.unshift(f); addFillMarker(f);
    $('myFills').innerHTML = S.myFills.map(x => `<div class="tr ${x.side === 0 ? 'b' : 's'}"><span class="t">${clock(x.t)}</span><span class="p">${x.side === 0 ? 'BOT' : 'SLD'} ${fmt$(x.price)}</span><span class="q">${fmtN(x.qty)}</span></div>`).join('');
    toast(`${x(f.side) } ${fmtN(f.qty)} @ ${fmt$(f.price)}${f.aggr ? '' : ' (passive)'}`, 'fill');
  }
  const x = (s) => s === 0 ? 'Bought' : 'Sold';

  function renderTicketHint(pub) {
    const live = pub.phase === 'live' && !!S.player;
    $('submit').disabled = !live; $('placeStop').disabled = !live;
    $('ticketHint').textContent = !S.player ? 'Join to trade' : pub.phase === 'live' ? (pub.phase_left < 120 ? 'Closing soon' : 'Market open') : pub.phase === 'premarket' ? 'Opens soon' : 'Market closed';
    const t = S.type === 'market' ? 'market' : ($('price').value ? fmtD(+$('price').value) : 'limit');
    $('submit').textContent = `${S.side === 'buy' ? 'Buy' : 'Sell'} ${$('qty').value || 0} @ ${t}`;
    if (S.type === 'limit' && !$('price').value && pub.mid && document.activeElement !== $('price')) {
      $('price').placeholder = fmt$(S.side === 'buy' ? (pub.bid || pub.mid) : (pub.ask || pub.mid));
    }
  }

  // ---------- results ----------
  async function showResults(session) {
    $('resultModal').hidden = false; $('resSession').textContent = session; $('replayLink').href = `/api/replay?session=${encodeURIComponent(session)}`;
    $('narrative').innerHTML = '<div class="pending">Reconstructing your decisions from the event log…</div>';
    $('scoreArc').style.strokeDashoffset = 327; $('scoreVal').textContent = '—';
    clearInterval(S.reviewTimer);
    const poll = async () => {
      try {
        const r = await api(`/api/review?session=${encodeURIComponent(session)}`);
        if (r.status === 'absent' || r.status === 'none') { $('resTitle').textContent = 'You watched this one'; $('resSub').textContent = 'Join before the open to be scored next round.'; $('narrative').innerHTML = '<p class="muted">No trades, no review.</p>'; clearInterval(S.reviewTimer); return; }
        renderResult(r);
        if (r.status === 'ready') clearInterval(S.reviewTimer);
      } catch (e) { /* retry */ }
    };
    poll(); S.reviewTimer = setInterval(poll, 2000);
  }

  function renderResult(r) {
    const res = r.result; if (!res) return;
    const sc = S.state?.scenario || {};
    $('resTitle').textContent = `${res.return_pct >= 0 ? 'Finished green' : 'Finished red'} on ${sc.ticker || ''}`;
    $('resSub').textContent = `${res.episodes} round trip${res.episodes === 1 ? '' : 's'} · ${res.fills} fill${res.fills === 1 ? '' : 's'} · max drawdown ${res.max_drawdown_pct.toFixed(2)}%`;
    requestAnimationFrame(() => { $('scoreArc').style.strokeDashoffset = 327 - 327 * res.score / 100; });
    $('scoreVal').textContent = res.score.toFixed(0);
    const set = (k, v, txt) => { $('bar' + k).style.width = v + '%'; $('val' + k).textContent = txt; };
    set('Return', res.return_score, `${res.return_pct >= 0 ? '+' : ''}${res.return_pct.toFixed(2)}% · ${res.return_score.toFixed(0)}/100`);
    set('Risk', res.risk_score, `DD ${res.max_drawdown_pct.toFixed(2)}% · ${res.risk_score.toFixed(0)}/100`);
    set('Process', res.process_score, `${res.mistakes.length} pattern${res.mistakes.length === 1 ? '' : 's'} · ${res.process_score.toFixed(0)}/100`);
    const m = [['Final equity', '$' + fmtN(Math.round(res.final_equity))], ['Win rate', res.episodes ? (res.win_rate * 100).toFixed(0) + '%' : '—'], ['Hold (win/loss)', res.episodes ? `${Math.round(res.avg_hold_win_sec / 20)}s / ${Math.round(res.avg_hold_loss_sec / 20)}s` : '—'], ['Entry vs fair', res.episodes ? `${res.avg_entry_gap_pct >= 0 ? '+' : ''}${res.avg_entry_gap_pct.toFixed(2)}%` : '—'], ['Aggressive fills', res.fills ? res.aggressor_pct.toFixed(0) + '%' : '—'], ['Max pos / limit', `${fmtN(res.max_pos)} / ${fmtN(res.limit)}`]];
    $('resMetrics').innerHTML = m.map(([k, v]) => `<div><span class="k">${k}</span>${v}</div>`).join('');
    const mk = res.mistakes.map(x => `<div class="mk"><b>${esc(x.title)}${x.count > 1 ? ` ×${x.count}` : ''}</b>${esc(x.detail)}</div>`);
    const good = (res.strengths || []).map(s => `<div class="mk good"><b>Strength</b>${esc(s)}</div>`);
    $('mistakes').innerHTML = [...mk, ...good].join('') || '<div class="empty">Nothing in the taxonomy fired. Either disciplined or idle; the coach will say which.</div>';
    if (res.calibration) { const c = res.calibration; $('calibration').innerHTML = `Claimed <b class="mono">${c.avg_claimed.toFixed(0)}%</b> · right <b class="mono">${c.hit_rate.toFixed(0)}%</b> over ${c.theses} ${c.theses === 1 ? 'thesis' : 'theses'}. ${esc(c.description)}`; }
    else $('calibration').textContent = 'No thesis declared this session. Pick one before an entry and your calibration gets scored.';
    $('resBoard').innerHTML = (r.board || []).map((b, i) => `<div class="lb${b.me ? ' me' : ''}"><span class="rk">${i + 1}</span><span>${esc(b.name)}</span><span class="eq">${b.score.toFixed(0)}</span><span class="${b.return_pct >= 0 ? 'up' : 'down'} mono">${b.return_pct >= 0 ? '+' : ''}${b.return_pct.toFixed(2)}%</span></div>`).join('');
    if (r.review) {
      $('coachSrc').textContent = r.review.source === 'claude' ? `· ${r.review.models.join(' → ')} · $${r.review.cost_usd.toFixed(4)}` : '· local rules (set ANTHROPIC_API_KEY for the Claude coach)';
      $('narrative').innerHTML = md(r.review.narrative) + (r.review.error ? `<p class="muted small">${esc(r.review.error)}</p>` : '');
    }
  }

  function md(s) {
    const blocks = s.trim().split(/\n{2,}/); let out = '';
    for (const b of blocks) {
      const lines = b.split('\n');
      if (lines.every(l => /^\s*[-*] /.test(l))) out += '<ul>' + lines.map(l => `<li>${inline(l.replace(/^\s*[-*] /, ''))}</li>`).join('') + '</ul>';
      else out += `<p>${inline(b.replace(/\n/g, ' '))}</p>`;
    }
    return out;
  }
  const inline = (t) => esc(t).replace(/\*\*(.+?)\*\*/g, '<b>$1</b>').replace(/\*(.+?)\*/g, '<i>$1</i>').replace(/`(.+?)`/g, '<code>$1</code>');
  const esc = (s) => String(s ?? '').replace(/[&<>"]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));

  function toast(text, cls = '') {
    const t = document.createElement('div'); t.className = 'toast ' + cls; t.textContent = text; $('toasts').appendChild(t);
    setTimeout(() => { t.style.opacity = '0'; t.style.transition = '.4s'; setTimeout(() => t.remove(), 400); }, 3500);
  }

  // ---------- actions ----------
  $('joinForm').addEventListener('submit', async (e) => {
    e.preventDefault();
    try {
      const r = await api('/api/join', { name: $('name').value.trim(), token: S.token });
      S.token = r.token; localStorage.setItem('tf_token', r.token); S.player = { id: r.id, name: r.name };
      $('joinModal').hidden = true; connect(); loadState();
    } catch (err) { toast(err.message, 'err'); }
  });
  $('btnJoin').addEventListener('click', () => { $('joinModal').hidden = false; $('name').focus(); });

  document.querySelectorAll('.side-toggle .side').forEach(b => b.addEventListener('click', () => {
    S.side = b.dataset.side; document.querySelectorAll('.side-toggle .side').forEach(x => x.classList.toggle('active', x === b));
    $('submit').className = 'submit ' + S.side; if (S.pub) renderTicketHint(S.pub);
  }));
  document.querySelectorAll('#typeSeg button').forEach(b => b.addEventListener('click', () => {
    S.type = b.dataset.type; document.querySelectorAll('#typeSeg button').forEach(x => x.classList.toggle('active', x === b));
    $('price').disabled = S.type === 'market'; if (S.pub) renderTicketHint(S.pub);
  }));
  document.querySelectorAll('.quick button').forEach(b => b.addEventListener('click', () => {
    $('qty').value = b.dataset.q === 'limit' ? (S.me?.limit || 1000) : b.dataset.q; if (S.pub) renderTicketHint(S.pub);
  }));
  ['qty', 'price'].forEach(id => $(id).addEventListener('input', () => S.pub && renderTicketHint(S.pub)));
  document.querySelectorAll('#thesisChips button').forEach(b => b.addEventListener('click', () => {
    const on = !b.classList.contains('active');
    document.querySelectorAll('#thesisChips button').forEach(x => x.classList.remove('active'));
    b.classList.toggle('active', on); S.thesis = on ? b.dataset.t : null;
  }));
  $('conf').addEventListener('input', () => $('confVal').textContent = $('conf').value + '%');
  $('ladder').addEventListener('click', (e) => { const l = e.target.closest('.lvl'); if (l) { $('price').value = (l.dataset.p / 100).toFixed(2); if (S.pub) renderTicketHint(S.pub); } });

  $('submit').addEventListener('click', async () => {
    const qty = parseInt($('qty').value, 10);
    const price = S.type === 'market' ? 0 : Math.round(parseFloat($('price').value || $('price').placeholder) * 100);
    if (!qty || (S.type === 'limit' && !price)) { toast('Enter a quantity and a price', 'warn'); return; }
    try {
      if (S.thesis) { await api('/api/thesis', { thesis: S.thesis, dir: S.side === 'buy' ? 1 : -1, confidence: +$('conf').value }); document.querySelectorAll('#thesisChips button').forEach(x => x.classList.remove('active')); S.thesis = null; }
      const r = await api('/api/order', { side: S.side, type: S.type, price, qty });
      if (!r.fills || !r.fills.length) toast(`${S.side === 'buy' ? 'Bid' : 'Offer'} ${fmtN(qty)} @ ${fmt$(price)} resting`);
    } catch (err) { toast(err.message, 'err'); }
  });
  $('placeStop').addEventListener('click', async () => {
    const me = S.me; if (!me || !me.pos) { toast('No position to protect', 'warn'); return; }
    const trigger = Math.round(parseFloat($('stopPx').value) * 100); const qty = parseInt($('stopQty').value || Math.abs(me.pos), 10);
    if (!trigger) { toast('Enter a trigger price', 'warn'); return; }
    try { await api('/api/stop', { side: me.pos > 0 ? 'sell' : 'buy', price: trigger, qty }); toast(`Stop placed at ${fmt$(trigger)}`); $('stopPx').value = ''; $('stopQty').value = ''; } catch (err) { toast(err.message, 'err'); }
  });
  $('flatten').addEventListener('click', async () => {
    const me = S.me; if (!me || !me.pos) return;
    try { await api('/api/order', { side: me.pos > 0 ? 'sell' : 'buy', type: 'market', price: 0, qty: Math.abs(me.pos) }); } catch (err) { toast(err.message, 'err'); }
  });
  document.body.addEventListener('click', async (e) => {
    const c = e.target.closest('[data-cancel]'); const sc = e.target.closest('[data-stopcancel]');
    try {
      if (c) await api('/api/cancel', { id: +c.dataset.cancel });
      if (sc) await api('/api/stop/cancel', { id: +sc.dataset.stopcancel });
    } catch (err) { toast(err.message, 'err'); }
  });
  $('limitRange').addEventListener('input', () => $('limitVal').textContent = fmtN(+$('limitRange').value) + ' sh');
  $('preReady').addEventListener('click', async () => {
    try { await api('/api/limit', { limit: +$('limitRange').value }); toast(`Position limit set to ${fmtN(+$('limitRange').value)} shares`); } catch (err) { toast(err.message, 'err'); }
    S.preDismissed = true; $('preModal').hidden = true;
  });
  $('preSkip').addEventListener('click', () => { S.preDismissed = true; $('preModal').hidden = true; });
  $('resClose').addEventListener('click', () => { $('resultModal').hidden = true; });
  document.querySelectorAll('#chartTabs .tab').forEach(b => b.addEventListener('click', () => {
    document.querySelectorAll('#chartTabs .tab').forEach(x => x.classList.toggle('active', x === b));
    const h = b.dataset.view === 'history'; $('chart').hidden = h; $('chartHist').hidden = !h; if (h) hist.timeScale().fitContent();
  }));
  document.querySelectorAll('#tapeTabs .tab').forEach(b => b.addEventListener('click', () => {
    document.querySelectorAll('#tapeTabs .tab').forEach(x => x.classList.toggle('active', x === b));
    $('tape').hidden = b.dataset.view !== 'tape'; $('board').hidden = b.dataset.view !== 'board'; $('myFills').hidden = b.dataset.view !== 'fills';
  }));

  // ---------- boot ----------
  makeCharts();
  if (S.token) {
    api('/api/join', { token: S.token }).then(r => { S.player = { id: r.id, name: r.name }; $('joinModal').hidden = true; connect(); loadState(); })
      .catch(() => { S.token = ''; localStorage.removeItem('tf_token'); connect(); loadState(); });
  } else { connect(); loadState(); }
})();
