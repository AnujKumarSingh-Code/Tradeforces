/* Tradeforces client. One WebSocket for state, fetches for actions. The
   server is authoritative for everything; this file only renders. */
(() => {
  const $ = (id) => document.getElementById(id);
  const fmt$ = (c) => (c / 100).toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
  const fmtD = (d, dp = 2) => d.toLocaleString(undefined, { minimumFractionDigits: dp, maximumFractionDigits: dp });
  const fmtN = (n) => n.toLocaleString();
  const money = (v) => `${v < 0 ? '−' : ''}$${fmtN(Math.abs(Math.round(v)))}`;
  const esc = (s) => String(s ?? '').replace(/[&<>"]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));
  const BASE = Date.UTC(2026, 2, 2, 9, 30) / 1000;
  const simTime = (t) => BASE + t;
  const clock = (t) => { const s = 9 * 3600 + 30 * 60 + t; const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), x = s % 60; return `${String(h).padStart(2,'0')}:${String(m).padStart(2,'0')}:${String(x).padStart(2,'0')}`; };
  const mmss = (sec) => { sec = Math.max(0, Math.round(sec)); return `${Math.floor(sec / 60)}:${String(sec % 60).padStart(2, '0')}`; };

  const S = {
    token: localStorage.getItem('tf_token') || '', player: null, state: null, pub: null, me: null,
    type: 'limit', thesis: null, session: null, preDismissed: false, resultShown: false, lastNewsCount: 0,
    candles: [], cur: null, tf: 300, myFills: [], markers: [], ws: null, reviewTimer: null, spectating: false, bannerNews: false,
  };

  // ---------- networking ----------
  const api = async (path, body) => {
    const r = await fetch(path, { method: body === undefined ? 'GET' : 'POST', headers: { 'Content-Type': 'application/json', 'X-Player-Token': S.token }, body: body === undefined ? undefined : JSON.stringify(body) });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(j.error || r.statusText);
    return j;
  };
  function connect() {
    if (S.ws) { S.ws.onclose = null; try { S.ws.close(); } catch (_) {} }
    const ws = new WebSocket(`${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/ws?token=${encodeURIComponent(S.token)}`);
    S.ws = ws;
    ws.onmessage = (ev) => { const m = JSON.parse(ev.data); if (m.type === 'tick') onTick(m); };
    ws.onclose = () => { $('bookLive').classList.add('off'); setTimeout(connect, 1500); };
    ws.onopen = () => $('bookLive').classList.remove('off');
  }
  async function loadState() {
    try { S.state = await api('/api/state'); if (S.state.player) S.player = S.state.player; renderStatic(); resetChart(); }
    catch (e) { toast(e.message, 'err'); }
  }

  // ---------- chart ----------
  let chart, candles, volume;
  function makeChart() {
    chart = LightweightCharts.createChart($('chart'), {
      layout: { background: { type: 'solid', color: 'transparent' }, textColor: '#8a98b4', fontFamily: 'JetBrains Mono' },
      grid: { vertLines: { color: 'rgba(255,255,255,.04)' }, horzLines: { color: 'rgba(255,255,255,.04)' } },
      rightPriceScale: { borderColor: '#1a2740' }, timeScale: { borderColor: '#1a2740', timeVisible: true, secondsVisible: false, rightOffset: 5 },
      crosshair: { mode: 0, vertLine: { color: '#27385a', labelBackgroundColor: '#132038' }, horzLine: { color: '#27385a', labelBackgroundColor: '#132038' } },
      autoSize: true,
    });
    candles = chart.addCandlestickSeries({ upColor: '#22c55e', downColor: '#f43f5e', borderVisible: false, wickUpColor: '#22c55e', wickDownColor: '#f43f5e', priceFormat: { type: 'price', precision: 2, minMove: 0.01 }, priceLineColor: '#22d3ee' });
    volume = chart.addHistogramSeries({ priceFormat: { type: 'volume' }, priceScaleId: 'vol' });
    chart.priceScale('vol').applyOptions({ scaleMargins: { top: 0.8, bottom: 0 } });
  }
  // Aggregate base 5-minute candles into the chosen timeframe.
  function aggregated() {
    const all = S.candles.concat(S.cur && S.cur.o > 0 ? [S.cur] : []);
    if (S.tf === 'day') {
      const prior = (S.state?.prior || []).map(c => ({ ...c }));
      if (all.length) { const d = { t: 0, o: all[0].o, h: Math.max(...all.map(c => c.h)), l: Math.min(...all.map(c => c.l)), c: all[all.length - 1].c, v: all.reduce((s, c) => s + c.v, 0) }; prior.push(d); }
      return prior;
    }
    const out = []; let g = null;
    for (const c of all) {
      const key = Math.floor(c.t / S.tf) * S.tf;
      if (!g || g.t !== key) { g = { t: key, o: c.o, h: c.h, l: c.l, c: c.c, v: c.v }; out.push(g); }
      else { g.h = Math.max(g.h, c.h); g.l = Math.min(g.l, c.l); g.c = c.c; g.v += c.v; }
    }
    return out;
  }
  const bar = (c) => ({ time: simTime(c.t), open: c.o, high: c.h, low: c.l, close: c.c });
  const vbar = (c) => ({ time: simTime(c.t), value: c.v, color: c.c >= c.o ? 'rgba(34,197,94,.35)' : 'rgba(244,63,94,.35)' });
  function fullRedraw() {
    const a = aggregated();
    candles.setData(a.map(bar)); volume.setData(a.map(vbar));
    chart.timeScale().applyOptions({ timeVisible: S.tf !== 'day' });
    applyMarkers(); chart.timeScale().fitContent();
    const last = a[a.length - 1]; if (last) ohlc(last);
  }
  function resetChart() {
    S.candles = (S.state.candles || []).slice(); S.markers = [];
    for (const n of S.state.news || []) S.markers.push(newsMarker(n));
    fullRedraw();
  }
  const newsMarker = (n) => ({ t: n.at, position: 'aboveBar', color: n.kind === 'earnings' ? '#f5b227' : '#5b8cff', shape: 'circle', text: n.kind.toUpperCase().slice(0, 8), size: 1 });
  const fillMarker = (f) => ({ t: f.t, position: f.side === 0 ? 'belowBar' : 'aboveBar', color: f.side === 0 ? '#22c55e' : '#f43f5e', shape: f.side === 0 ? 'arrowUp' : 'arrowDown', text: `${f.side === 0 ? 'B' : 'S'} ${f.qty}`, size: 1 });
  function applyMarkers() {
    if (S.tf === 'day') { candles.setMarkers([]); return; }
    const ms = S.markers.map(m => ({ ...m, time: simTime(Math.floor(m.t / S.tf) * S.tf) })).sort((a, b) => a.time - b.time);
    try { candles.setMarkers(ms); } catch (_) {}
  }
  function ohlc(c) {
    $('sO').textContent = fmtD(c.o); $('sH').textContent = fmtD(c.h); $('sL').textContent = fmtD(c.l); $('sC').textContent = fmtD(c.c);
    const d = c.c - c.o; $('sChg').textContent = `${d >= 0 ? '+' : ''}${fmtD(d)} (${c.o ? (d / c.o * 100).toFixed(2) : '0.00'}%)`; $('sChg').className = 'chgsm ' + (d >= 0 ? 'up' : 'down');
  }
  function updateChart(pub) {
    let redraw = false;
    if (pub.ncandles > S.candles.length && pub.last_candle) { S.candles.push(pub.last_candle); redraw = true; }
    S.cur = pub.candle;
    if (redraw) { fullRedraw(); return; }
    const a = aggregated(); const last = a[a.length - 1];
    if (last && last.o > 0) { candles.update(bar(last)); volume.update(vbar(last)); ohlc(last); }
  }

  // ---------- static ----------
  function renderStatic() {
    const st = S.state, sc = st.scenario;
    $('ticker').textContent = sc.ticker; $('company').textContent = `${sc.company} · ${sc.sector}`;
    $('ohlcTitle').textContent = `${sc.ticker} · ${S.tf === 'day' ? '1D' : S.tf / 60 + 'm'}`;
    $('preTicker').textContent = sc.ticker; $('preCompany').textContent = sc.company; $('preBrief').textContent = sc.brief; $('preCash').textContent = '$' + fmtN(st.start_cash);
    $('speed').textContent = `${st.speed}×`;
    const cal = (el) => { el.innerHTML = (st.calendar || []).map(c => `<div class="cal" data-at="${c.at}"><span class="t">${clock(c.at).slice(0, 5)}</span><span class="k">${c.kind}</span><span>${esc(c.label)}</span></div>`).join(''); };
    cal($('calendar')); cal($('preCalendar')); drawSpark(st.prior || []);
    $('setSession').textContent = st.session; $('setCoach').textContent = st.coach ? 'Claude (two-stage) after the close · rules live' : 'local rules (set ANTHROPIC_API_KEY for Claude)';
    $('replayLinkSet').href = `/api/replay?session=${encodeURIComponent(st.session)}`;
    $('agents').innerHTML = [['noise', 'Noise Traders', `${st.roster.noise} · chase momentum`, '#8a98b4'], ['medium', 'Medium Traders', `${st.roster.medium} · trend & stops`, '#22d3ee'], ['pro', 'Pro Agents', `${st.roster.pros} · find alpha`, '#f5a623'], ['maker', 'Market Makers', `${st.roster.makers} · provide liquidity`, '#5b8cff'], ['human', 'Humans', 'you and the others', '#22c55e']]
      .map(([k, n, d, c]) => `<div class="agent" data-tier="${k}"><span class="d" style="background:${c};box-shadow:0 0 8px ${c}"></span><span class="n">${n}<small>${d}</small></span><svg viewBox="0 0 70 26" preserveAspectRatio="none"><path fill="none" stroke="${c}" stroke-width="1.6" stroke-linejoin="round" id="fl-${k}"/></svg></div>`).join('');
    if (S.player) $('rename').value = S.player.name;
  }
  function drawSpark(prior) {
    const cv = $('spark'); const ctx = cv.getContext('2d'); ctx.clearRect(0, 0, cv.width, cv.height); if (!prior.length) return;
    const xs = prior.map(p => p.c); const lo = Math.min(...xs), hi = Math.max(...xs); const w = cv.width, h = cv.height;
    const grad = ctx.createLinearGradient(0, 0, 0, h); grad.addColorStop(0, 'rgba(34,211,238,.35)'); grad.addColorStop(1, 'rgba(34,211,238,0)');
    ctx.beginPath(); xs.forEach((v, i) => { const x = i / (xs.length - 1) * (w - 4) + 2, y = h - 6 - (v - lo) / (hi - lo || 1) * (h - 14); i ? ctx.lineTo(x, y) : ctx.moveTo(x, y); });
    ctx.strokeStyle = '#22d3ee'; ctx.lineWidth = 2; ctx.stroke(); ctx.lineTo(w - 2, h); ctx.lineTo(2, h); ctx.closePath(); ctx.fillStyle = grad; ctx.fill();
    ctx.fillStyle = '#8a98b4'; ctx.font = '11px JetBrains Mono'; ctx.fillText(fmtD(xs[0]), 2, 11); ctx.textAlign = 'right'; ctx.fillText(fmtD(xs[xs.length - 1]), w - 2, 11); ctx.textAlign = 'left';
  }

  // ---------- tick ----------
  function onTick(m) {
    const pub = typeof m.pub === 'string' ? JSON.parse(m.pub) : m.pub; S.pub = pub; S.me = m.me || null;
    if (pub.session !== S.session) {
      S.session = pub.session; S.preDismissed = false; S.resultShown = false; S.lastNewsCount = 0; S.myFills = []; S.candles = []; S.markers = [];
      $('myFills').innerHTML = ''; $('resultModal').hidden = true; loadState();
    }
    renderHero(pub);
    if ($('app').hidden) return;
    renderPhase(pub); renderQuote(pub); renderBook(pub); renderStrip(pub); renderBoard(pub); renderNews(pub); renderFlow(pub);
    if (S.state) updateChart(pub);
    renderMe(); renderCoach();
    for (const f of (m.fills || [])) onFill(f);
    renderTicket(pub);
  }
  function renderHero(pub) {
    const p = $('heroPhase'); p.className = 'pill ' + pub.phase; $('heroPhaseText').textContent = pub.phase === 'premarket' ? 'pre-market' : pub.phase;
    $('heroLeft').textContent = pub.phase === 'live' ? `${mmss(pub.phase_left)} left in this round · ${pub.players} playing` : pub.phase === 'premarket' ? `opens in ${mmss(pub.phase_left)}` : `next round in ${mmss(pub.phase_left)}`;
    if (!$('hero').hidden && (!S.heroDrawn || (pub.tape?.length && S.heroDrawn !== 'live'))) drawHero(pub);
  }
  function drawHero(pub) {
    S.heroDrawn = pub.tape?.length ? 'live' : true;
    const h = S.state?.candles || []; const prior = S.state?.prior || [];
    const pts = h.length > 4 ? h.map(c => c.c) : prior.length ? prior.map(c => c.c) : Array.from({ length: 40 }, (_, i) => 100 + Math.sin(i / 3) * 4 + i * 0.4);
    const lo = Math.min(...pts), hi = Math.max(...pts);
    const d = pts.map((v, i) => `${i ? 'L' : 'M'}${(i / (pts.length - 1) * 320).toFixed(1)},${(130 - (v - lo) / (hi - lo || 1) * 110).toFixed(1)}`).join(' ');
    $('heroLine').setAttribute('d', d); $('heroArea').setAttribute('d', d + ' L320,140 L0,140 Z');
    $('heroBook').innerHTML = '<b>Bid</b><b>Price</b><b>Ask</b>' + (pub.bids || []).slice(0, 5).map((b, i) => { const a = (pub.asks || [])[i]; return `<span class="b">${fmtN(b.q)}</span><span>${fmt$(b.p)}</span><span class="a">${a ? fmtN(a.q) : ''}</span>`; }).join('');
  }
  function renderPhase(pub) {
    const pill = $('phasePill'); let cls = pub.phase, text = pub.phase, label = 'to open';
    if (pub.phase === 'live') { label = 'to close'; if (pub.phase_left < 120) { cls = 'closing'; text = 'closing'; } }
    if (pub.phase === 'premarket') text = 'pre-market';
    if (pub.phase === 'closed') label = 'to next session';
    pill.className = 'pill phase-pill ' + cls; $('phaseText').textContent = text; $('phaseLeft').textContent = mmss(pub.phase_left); $('phaseLabel').textContent = label;
    const b = $('banner');
    if (pub.phase === 'live' && pub.phase_left < 120) { b.hidden = false; b.className = 'banner closing'; b.textContent = `Closing auction in ${mmss(pub.phase_left)}. Open positions are marked at the close.`; }
    else if (pub.phase === 'premarket') { b.hidden = false; b.className = 'banner'; b.textContent = `Pre-market. The book goes live in ${mmss(pub.phase_left)}. Read the brief and set your limit.`; }
    else if (!S.bannerNews) b.hidden = true;
    if (pub.phase === 'premarket' && !S.preDismissed && S.player && S.state) { $('preModal').hidden = false; $('preLeft').textContent = mmss(pub.phase_left); $('preReady').hidden = false; $('preEyebrow').textContent = 'Pre-market'; }
    else if (!S.briefOpen) $('preModal').hidden = true;
    if (pub.phase === 'closed' && !S.resultShown && S.player) { S.resultShown = true; showResults(pub.session); }
    if (pub.phase === 'closed') $('resLeft').textContent = mmss(pub.phase_left);
    document.querySelectorAll('.cal').forEach(el => { const at = +el.dataset.at; el.classList.toggle('done', pub.now >= at); el.classList.toggle('soon', pub.now < at && at - pub.now < 600); });
  }
  function renderQuote(pub) {
    const px = pub.closed ? pub.close_px : (pub.last || pub.mid);
    $('last').textContent = px ? fmt$(px) : '—';
    const start = S.state?.scenario?.start; const chg = $('chg');
    if (start && px) { const d = px / 100 - start; chg.textContent = `${d >= 0 ? '▲' : '▼'} ${d >= 0 ? '+' : ''}${(d / start * 100).toFixed(2)}%`; chg.className = 'mono chg ' + (d >= 0 ? 'up' : 'down'); }
  }
  function renderBook(pub) {
    const mine = new Set((S.me?.open || []).map(o => o.price));
    const bids = pub.bids || [], asks = pub.asks || []; const n = Math.max(bids.length, asks.length, 1);
    const max = Math.max(...bids.map(b => b.q), ...asks.map(a => a.q), 1);
    let rows = '';
    for (let i = 0; i < Math.min(n, 11); i++) {
      const b = bids[i], a = asks[i];
      rows += `<div class="brow${(b && mine.has(b.p)) || (a && mine.has(a.p)) ? ' mine' : ''}">
        <div class="bq">${b ? `<i style="width:${(b.q / max * 100).toFixed(0)}%"></i><span>${fmtN(b.q)}</span>` : ''}</div>
        <div class="px">${b ? `<b class="bp" data-p="${b.p}">${fmt$(b.p)}</b>` : '<b></b>'}${a ? `<b class="ap" data-p="${a.p}">${fmt$(a.p)}</b>` : '<b></b>'}</div>
        <div class="aq">${a ? `<i style="width:${(a.q / max * 100).toFixed(0)}%"></i><span>${fmtN(a.q)}</span>` : ''}</div></div>`;
    }
    $('book').innerHTML = rows;
    const tb = bids.reduce((s, b) => s + b.q, 0), ta = asks.reduce((s, a) => s + a.q, 0);
    $('spreadBid').style.width = (tb + ta ? tb / (tb + ta) * 100 : 50).toFixed(0) + '%';
    $('spread').textContent = pub.spread ? `${fmt$(pub.spread)} (${(pub.spread / pub.mid * 100).toFixed(2)}%)` : '—';
  }
  function renderStrip(pub) {
    $('stripItems').innerHTML = (pub.tape || []).slice(-12).reverse().map(t => `<div class="sitem ${t.s === 0 ? 'b' : 's'}"><span>${fmt$(t.p)}</span><span class="q">${t.s === 0 ? '▲' : '▼'} ${fmtN(t.q)}</span><span class="t">${clock(t.t)}</span></div>`).join('');
  }
  function renderBoard(pub) {
    const me = S.player?.name;
    $('board').innerHTML = (pub.board || []).map((r, i) => `<div class="lb${r.name === me ? ' me' : ''}"><span class="rk">${i + 1}</span><span>${esc(r.name)}${r.pos ? ` <span class="muted small">${r.pos > 0 ? '+' : ''}${r.pos}</span>` : ''}</span><span class="eq">$${fmtN(Math.round(r.equity))}</span><span class="${r.ret >= 0 ? 'up' : 'down'} mono">${r.ret >= 0 ? '+' : ''}${r.ret.toFixed(2)}%</span></div>`).join('') || '<div class="empty">Nobody has joined this round yet</div>';
  }
  function renderNews(pub) {
    const news = pub.news || [];
    if (news.length === S.lastNewsCount) return;
    $('news').innerHTML = news.slice().reverse().map(n => `<div class="headline ${n.kind}"><span class="t">${clock(n.at).slice(0, 5)}</span>${esc(n.headline)}</div>`).join('') || '<div class="empty">No headlines yet</div>';
    if (news.length) {
      const n = news[news.length - 1];
      if (!S.markers.some(m => m.t === n.at && m.shape === 'circle')) { S.markers.push(newsMarker(n)); applyMarkers(); }
      if (S.lastNewsCount > 0 || S.state) flashBanner(n.headline, n.kind);
    }
    S.lastNewsCount = news.length;
  }
  function flashBanner(text, kind) {
    const b = $('banner'); b.hidden = false; b.className = 'banner news'; b.textContent = `${kind.toUpperCase()}: ${text}`;
    S.bannerNews = true; clearTimeout(S.bannerT); S.bannerT = setTimeout(() => { S.bannerNews = false; }, 9000);
  }
  function renderFlow(pub) {
    const flow = pub.flow || []; if (!flow.length) return;
    for (const k of ['noise', 'medium', 'pro', 'maker', 'human']) {
      const el = $('fl-' + k); if (!el) continue;
      const v = flow.map(f => f[k] || 0); const hi = Math.max(...v, 1);
      el.setAttribute('d', v.map((x, i) => `${i ? 'L' : 'M'}${(i / Math.max(1, v.length - 1) * 70).toFixed(1)},${(24 - x / hi * 22).toFixed(1)}`).join(' '));
    }
  }
  function renderMe() {
    const me = S.me, st = S.state;
    if (!me) { $('cash').textContent = '—'; $('limitHdr').textContent = '—'; $('posKV').innerHTML = `<div class="empty">${S.player ? 'Joining the next round…' : 'Spectating. Join from the lobby to trade.'}</div>`; $('flatten').disabled = true; return; }
    const start = st?.start_cash || 100000; const pnl = me.equity - start;
    $('cash').textContent = '$' + fmtD(me.cash); $('limitHdr').textContent = fmtN(me.limit) + ' sh';
    const kv = [['Equity', '$' + fmtN(Math.round(me.equity))], ['Day P&L', `${money(pnl)} (${(pnl / start * 100).toFixed(2)}%)`, pnl >= 0 ? 'up' : 'down'], ['Position', `${me.pos > 0 ? '+' : ''}${fmtN(me.pos)}`, me.pos > 0 ? 'up' : me.pos < 0 ? 'down' : ''], ['Avg cost', me.pos ? fmtD(me.avg) : '—'], ['Unrealised', money(me.unrealized), me.unrealized >= 0 ? 'up' : 'down'], ['Realised', money(me.realized), me.realized >= 0 ? 'up' : 'down'], ['Max drawdown', me.max_dd.toFixed(2) + '%'], ['Fills', me.fills]];
    $('posKV').innerHTML = kv.map(([k, v, c]) => `<div><span class="k">${k}</span><span class="${c || ''}">${v}</span></div>`).join('');
    $('orders').innerHTML = (me.open || []).map(o => `<div class="ord"><button class="side ${o.side === 'buy' ? 'up' : 'down'}">${o.side.toUpperCase()}</button><span>${fmt$(o.price)}</span><span>${fmtN(o.remaining)}</span><span class="muted">${Math.round(o.age / (S.pub?.speed || 20))}s</span><button class="x" data-cancel="${o.id}">✕</button></div>`).join('') || '<div class="empty">No resting orders</div>';
    $('stops').innerHTML = (me.stops || []).map(s => `<div class="ord"><button class="side ${s.side === 0 ? 'up' : 'down'}">${s.side === 0 ? 'BUY STOP' : 'SELL STOP'}</button><span>${fmt$(s.trigger)}</span><span>${fmtN(s.qty)}</span><span></span><button class="x" data-stopcancel="${s.id}">✕</button></div>`).join('') || `<div class="empty">${me.pos ? 'Unprotected position. The log is counting.' : 'No stops.'}</div>`;
    $('flatten').disabled = !me.pos;
    $('bottomTabs').querySelector('[data-view="orders"]').textContent = `Orders${(me.open?.length || 0) + (me.stops?.length || 0) ? ` (${(me.open?.length || 0) + (me.stops?.length || 0)})` : ''}`;
  }
  function renderCoach() {
    const live = S.me?.live; const card = $('coachCard');
    if (!live) { card.className = 'panel coach-card'; return; }
    card.className = 'panel coach-card ' + live.tone; $('coachText').textContent = live.hint;
    const p = Math.max(0, Math.min(100, live.process)); $('procVal').textContent = p.toFixed(0) + '%';
    const arc = $('procArc'); arc.style.strokeDashoffset = 188.5 - 188.5 * p / 100; arc.style.stroke = p >= 75 ? '#34d399' : p >= 50 ? '#f5b227' : '#f43f5e';
  }
  const pendingFills = new Map(); // order id -> aggregated fill, flushed after a short pause
  function onFill(f) {
    S.myFills.unshift(f);
    $('myFills').innerHTML = S.myFills.map(x => `<div class="tr ${x.side === 0 ? 'b' : 's'}"><span class="t">${clock(x.t)}</span><span class="p">${x.side === 0 ? 'BOT' : 'SLD'} ${fmt$(x.price)}</span><span class="q">${fmtN(x.qty)}</span></div>`).join('');
    const key = f.id || 0; const p = pendingFills.get(key) || { ...f, qty: 0, notional: 0, n: 0 };
    p.qty += f.qty; p.notional += f.qty * f.price; p.n++; p.t = f.t; clearTimeout(p.timer);
    p.timer = setTimeout(() => {
      pendingFills.delete(key);
      const avg = p.notional / p.qty;
      S.markers.push({ ...fillMarker({ ...p, side: f.side }), text: `${f.side === 0 ? 'B' : 'S'} ${p.qty}` }); applyMarkers();
      toast(`${f.side === 0 ? 'Bought' : 'Sold'} ${fmtN(p.qty)} @ ${fmt$(avg)}${p.n > 1 ? ` in ${p.n} fills` : ''}${f.aggr ? '' : ' (passive)'}`, 'fill');
    }, 900);
    pendingFills.set(key, p);
  }
  function renderTicket(pub) {
    const live = pub.phase === 'live' && !!S.player;
    $('btnBuy').disabled = !live; $('btnSell').disabled = !live;
    $('ticketHint').textContent = !S.player ? 'Join to trade' : pub.phase === 'live' ? (pub.phase_left < 120 ? 'Closing soon' : 'Market open') : pub.phase === 'premarket' ? 'Opens soon' : 'Market closed';
    if (document.activeElement !== $('price') && !$('price').value && pub.mid) $('price').placeholder = fmt$(S.type === 'stop' ? pub.last || pub.mid : pub.mid);
    if (S.type === 'stop') { $('btnBuy').textContent = 'Buy stop'; $('btnSell').textContent = 'Sell stop'; $('priceLabel').textContent = 'Trigger'; }
    else { $('btnBuy').textContent = 'Buy'; $('btnSell').textContent = 'Sell'; $('priceLabel').textContent = 'Price'; }
    $('priceField').style.opacity = S.type === 'market' ? .4 : 1; $('price').disabled = S.type === 'market';
  }

  // ---------- results ----------
  async function showResults(session) {
    $('resultModal').hidden = false; $('resSession').textContent = session; $('replayLink').href = `/api/replay?session=${encodeURIComponent(session)}`;
    $('narrative').innerHTML = '<div class="pending">Reconstructing your decisions from the event log…</div>'; $('scoreArc').style.strokeDashoffset = 327; $('scoreVal').textContent = '—';
    clearInterval(S.reviewTimer);
    const poll = async () => {
      try {
        const r = await api(`/api/review?session=${encodeURIComponent(session)}`);
        if (r.status === 'absent' || r.status === 'none') { $('resTitle').textContent = 'You watched this one'; $('resSub').textContent = 'Join before the open to be scored next round.'; $('narrative').innerHTML = '<p class="muted">No trades, no review.</p>'; clearInterval(S.reviewTimer); return; }
        renderResult(r); if (r.status === 'ready') clearInterval(S.reviewTimer);
      } catch (e) { /* retry */ }
    };
    poll(); S.reviewTimer = setInterval(poll, 2000);
  }
  function renderResult(r) {
    const res = r.result; if (!res) return; res.mistakes = res.mistakes || []; res.strengths = res.strengths || []; const sc = S.state?.scenario || {};
    $('resTitle').textContent = `${res.return_pct >= 0 ? 'Finished green' : 'Finished red'} on ${sc.ticker || ''}`;
    $('resSub').textContent = `${res.episodes} round trip${res.episodes === 1 ? '' : 's'} · ${res.fills} fill${res.fills === 1 ? '' : 's'} · max drawdown ${res.max_drawdown_pct.toFixed(2)}%`;
    requestAnimationFrame(() => { $('scoreArc').style.strokeDashoffset = 327 - 327 * res.score / 100; }); $('scoreVal').textContent = res.score.toFixed(0);
    const set = (k, v, txt) => { $('bar' + k).style.width = v + '%'; $('val' + k).textContent = txt; };
    set('Return', res.return_score, `${res.return_pct >= 0 ? '+' : ''}${res.return_pct.toFixed(2)}% · ${res.return_score.toFixed(0)}/100`);
    set('Risk', res.risk_score, `DD ${res.max_drawdown_pct.toFixed(2)}% · ${res.risk_score.toFixed(0)}/100`);
    set('Process', res.process_score, `${res.mistakes.length} pattern${res.mistakes.length === 1 ? '' : 's'} · ${res.process_score.toFixed(0)}/100`);
    const m = [['Final equity', '$' + fmtN(Math.round(res.final_equity))], ['Win rate', res.episodes ? (res.win_rate * 100).toFixed(0) + '%' : '—'], ['Hold (win/loss)', res.episodes ? `${Math.round(res.avg_hold_win_sec / 20)}s / ${Math.round(res.avg_hold_loss_sec / 20)}s` : '—'], ['Entry vs fair', res.episodes ? `${res.avg_entry_gap_pct >= 0 ? '+' : ''}${res.avg_entry_gap_pct.toFixed(2)}%` : '—'], ['Aggressive fills', res.fills ? res.aggressor_pct.toFixed(0) + '%' : '—'], ['Max pos / limit', `${fmtN(res.max_pos)} / ${fmtN(res.limit)}`]];
    $('resMetrics').innerHTML = m.map(([k, v]) => `<div><span class="k">${k}</span>${v}</div>`).join('');
    const mk = res.mistakes.map(x => `<div class="mk"><b>${esc(x.title)}${x.count > 1 ? ` ×${x.count}` : ''}</b>${esc(x.detail)}</div>`);
    const good = (res.strengths || []).map(s => `<div class="mk good"><b>Strength</b>${esc(s)}</div>`);
    $('mistakes').innerHTML = [...mk, ...good].join('') || '<div class="empty">Nothing in the taxonomy fired. Either disciplined or idle; the coach will say which.</div>';
    $('calibration').innerHTML = res.calibration ? `Claimed <b class="mono">${res.calibration.avg_claimed.toFixed(0)}%</b> · right <b class="mono">${res.calibration.hit_rate.toFixed(0)}%</b> over ${res.calibration.theses} ${res.calibration.theses === 1 ? 'thesis' : 'theses'}. ${esc(res.calibration.description)}` : 'No thesis declared this session. Pick one before an entry and your calibration gets scored.';
    $('resBoard').innerHTML = (r.board || []).map((b, i) => `<div class="lb${b.me ? ' me' : ''}"><span class="rk">${i + 1}</span><span>${esc(b.name)}</span><span class="eq">${b.score.toFixed(0)}</span><span class="${b.return_pct >= 0 ? 'up' : 'down'} mono">${b.return_pct >= 0 ? '+' : ''}${b.return_pct.toFixed(2)}%</span></div>`).join('');
    if (r.review) {
      $('coachSrc').textContent = r.review.source === 'claude' ? `· ${r.review.models.join(' → ')} · $${r.review.cost_usd.toFixed(4)}` : '· local rules (set ANTHROPIC_API_KEY for the Claude coach)';
      $('narrative').innerHTML = md(r.review.narrative) + (r.review.error ? `<p class="muted small">${esc(r.review.error)}</p>` : '');
    }
  }
  function md(s) {
    return s.trim().split(/\n{2,}/).map(b => { const lines = b.split('\n'); return lines.every(l => /^\s*[-*] /.test(l)) ? '<ul>' + lines.map(l => `<li>${inline(l.replace(/^\s*[-*] /, ''))}</li>`).join('') + '</ul>' : `<p>${inline(b.replace(/\n/g, ' '))}</p>`; }).join('');
  }
  const inline = (t) => esc(t).replace(/\*\*(.+?)\*\*/g, '<b>$1</b>').replace(/\*(.+?)\*/g, '<i>$1</i>').replace(/`(.+?)`/g, '<code>$1</code>');
  function toast(text, cls = '') { const t = document.createElement('div'); t.className = 'toast ' + cls; t.textContent = text; $('toasts').appendChild(t); setTimeout(() => { t.style.opacity = '0'; t.style.transition = '.4s'; setTimeout(() => t.remove(), 400); }, 3500); }

  // ---------- actions ----------
  function enterApp() { $('hero').hidden = true; $('app').hidden = false; if (S.state) { renderStatic(); resetChart(); } }
  $('joinForm').addEventListener('submit', async (e) => {
    e.preventDefault();
    try { const r = await api('/api/join', { name: $('name').value.trim(), token: S.token }); S.token = r.token; localStorage.setItem('tf_token', r.token); S.player = { id: r.id, name: r.name }; enterApp(); connect(); loadState(); }
    catch (err) { toast(err.message, 'err'); }
  });
  $('spectate').addEventListener('click', () => { S.spectating = true; enterApp(); });
  $('instrumentBtn').addEventListener('click', openBrief);
  function openBrief() { S.briefOpen = true; $('preModal').hidden = false; $('preReady').hidden = S.pub?.phase !== 'premarket'; $('preEyebrow').textContent = S.pub?.phase === 'premarket' ? 'Pre-market' : 'Company brief'; $('preLeft').textContent = S.pub ? mmss(S.pub.phase_left) : ''; $('settings').hidden = true; }
  $('openBrief').addEventListener('click', openBrief);
  $('gear').addEventListener('click', () => { $('settings').hidden = !$('settings').hidden; });
  document.addEventListener('click', (e) => { if (!$('settings').hidden && !e.target.closest('#settings') && !e.target.closest('#gear')) $('settings').hidden = true; });
  $('renameForm').addEventListener('submit', async (e) => { e.preventDefault(); try { const r = await api('/api/join', { name: $('rename').value.trim(), token: S.token }); S.player = { id: r.id, name: r.name }; toast(`You are now ${r.name}`); } catch (err) { toast(err.message, 'err'); } });

  document.querySelectorAll('#ticketTabs button').forEach(b => b.addEventListener('click', () => { S.type = b.dataset.type; document.querySelectorAll('#ticketTabs button').forEach(x => x.classList.toggle('active', x === b)); if (S.type === 'stop' && S.me?.pos) $('qty').value = Math.abs(S.me.pos); if (S.pub) renderTicket(S.pub); }));
  document.querySelectorAll('.stepper button').forEach(b => b.addEventListener('click', () => {
    const d = +b.dataset.d;
    if (b.dataset.step === 'qty') { $('qty').value = Math.max(1, (parseInt($('qty').value, 10) || 0) + d * 50); }
    else { const cur = parseFloat($('price').value || $('price').placeholder) || 0; $('price').value = (cur + d * 0.05).toFixed(2); }
  }));
  document.querySelectorAll('#thesisChips button').forEach(b => b.addEventListener('click', () => { const on = !b.classList.contains('active'); document.querySelectorAll('#thesisChips button').forEach(x => x.classList.remove('active')); b.classList.toggle('active', on); S.thesis = on ? b.dataset.t : null; }));
  $('conf').addEventListener('input', () => $('confVal').textContent = $('conf').value + '%');
  $('book').addEventListener('click', (e) => { const p = e.target.closest('[data-p]'); if (p) { $('price').value = (p.dataset.p / 100).toFixed(2); if (S.type === 'market') { S.type = 'limit'; document.querySelectorAll('#ticketTabs button').forEach(x => x.classList.toggle('active', x.dataset.type === 'limit')); } if (S.pub) renderTicket(S.pub); } });

  async function submit(side) {
    const qty = parseInt($('qty').value, 10);
    const price = S.type === 'market' ? 0 : Math.round(parseFloat($('price').value || $('price').placeholder) * 100);
    if (!qty || (S.type !== 'market' && !price)) { toast('Enter a quantity and a price', 'warn'); return; }
    try {
      if (S.type === 'stop') { await api('/api/stop', { side, price, qty }); toast(`${side === 'buy' ? 'Buy' : 'Sell'} stop placed at ${fmt$(price)}`); $('price').value = ''; return; }
      if (S.thesis) { await api('/api/thesis', { thesis: S.thesis, dir: side === 'buy' ? 1 : -1, confidence: +$('conf').value }); document.querySelectorAll('#thesisChips button').forEach(x => x.classList.remove('active')); S.thesis = null; }
      const r = await api('/api/order', { side, type: S.type, price, qty });
      if (!r.fills || !r.fills.length) toast(`${side === 'buy' ? 'Bid' : 'Offer'} ${fmtN(qty)} @ ${fmt$(price)} resting`);
    } catch (err) { toast(err.message, 'err'); }
  }
  $('btnBuy').addEventListener('click', () => submit('buy'));
  $('btnSell').addEventListener('click', () => submit('sell'));
  $('flatten').addEventListener('click', async () => { const me = S.me; if (!me || !me.pos) return; try { await api('/api/order', { side: me.pos > 0 ? 'sell' : 'buy', type: 'market', price: 0, qty: Math.abs(me.pos) }); } catch (err) { toast(err.message, 'err'); } });
  document.body.addEventListener('click', async (e) => {
    const c = e.target.closest('[data-cancel]'); const sc = e.target.closest('[data-stopcancel]');
    try { if (c) await api('/api/cancel', { id: +c.dataset.cancel }); if (sc) await api('/api/stop/cancel', { id: +sc.dataset.stopcancel }); } catch (err) { toast(err.message, 'err'); }
  });
  $('limitRange').addEventListener('input', () => $('limitVal').textContent = fmtN(+$('limitRange').value) + ' sh');
  $('preReady').addEventListener('click', async () => { try { await api('/api/limit', { limit: +$('limitRange').value }); toast(`Position limit set to ${fmtN(+$('limitRange').value)} shares`); } catch (err) { toast(err.message, 'err'); } S.preDismissed = true; S.briefOpen = false; $('preModal').hidden = true; });
  $('preSkip').addEventListener('click', () => { S.preDismissed = true; S.briefOpen = false; $('preModal').hidden = true; });
  $('resClose').addEventListener('click', () => { $('resultModal').hidden = true; });
  document.querySelectorAll('#tfs button').forEach(b => b.addEventListener('click', () => { document.querySelectorAll('#tfs button').forEach(x => x.classList.toggle('active', x === b)); S.tf = b.dataset.tf === 'day' ? 'day' : +b.dataset.tf; if (S.state) { $('ohlcTitle').textContent = `${S.state.scenario.ticker} · ${S.tf === 'day' ? '1D' : S.tf / 60 + 'm'}`; fullRedraw(); } }));
  document.querySelectorAll('#bottomTabs button').forEach(b => b.addEventListener('click', () => { document.querySelectorAll('#bottomTabs button').forEach(x => x.classList.toggle('active', x === b)); document.querySelectorAll('.bottom-panel .view').forEach(v => v.hidden = v.id !== 'v-' + b.dataset.view); }));

  // ---------- boot ----------
  makeChart();
  connect(); loadState();
  if (S.token) api('/api/join', { token: S.token }).then(r => { S.player = { id: r.id, name: r.name }; enterApp(); connect(); loadState(); }).catch(() => { S.token = ''; localStorage.removeItem('tf_token'); });
})();
