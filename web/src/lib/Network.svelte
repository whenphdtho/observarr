<script>
  import Skeleton from './Skeleton.svelte';
  import { untrack } from 'svelte';
  import Kpi from './Kpi.svelte';
  import MirrorChart from './MirrorChart.svelte';
  import Donut from './Donut.svelte';
  import DailyBars from './DailyBars.svelte';
  import Delta from './Delta.svelte';
  import RangeSwitch from './RangeSwitch.svelte';
  import Icon from './icons/Icon.svelte';
  import { api } from './api.js';
  import { snapshot } from './data.js';
  import { serviceList } from './live.js';
  import { streamServices, streamSummary, formatBitrate } from './streams.js';
  import { group, layers, sum, add, advance } from './traffic.js';
  import { rangeOf, integrate, change, coverage, MIN_COVERAGE } from './history.js';
  import { formatMbit, formatRate, formatPart, formatPercent, formatSize, sizeParts, plural } from './format.js';

  // Network shows where the traffic goes: the host's rates and today's totals, the traffic of each
  // app (services with their linked containers, and other containers) over a range from five minutes
  // to a year with each app's share of it, the traffic of the last 30 days against the budget, and the
  // budget. It loads GET /api/network and, for the five minutes, adds each live reading. The key figures
  // compare the last 24 hours with the 24 hours before. The chart's key follows the range picked; the table below
  // says its own frame, now and today; the budget is shown in one place.
  const RELOAD_MS = 60 * 1000;

  let range = $state('5m');
  let view = $state(null);
  let error = $state('');
  // missing: the server has no history for the range asked for (one without the longer ranges answers 400).
  let missing = $state(false);
  // The last 24 hours for the key figures, loaded on its own while the chart shows another range.
  let dayOwn = $state(null);

  async function load() {
    const r = range;
    try {
      const v = await api('GET', `/api/network?range=${r}`);
      if (r === range) {
        v.at = r === '5m' ? (v.times[v.times.length - 1] ?? 0) : 0; // the latest reading it holds
        view = v;
        error = '';
        missing = false;
      }
    } catch (err) {
      if (r !== range) return;
      missing = err.status === 400 && r !== '5m' && r !== '24h';
      error = missing ? '' : err.message;
      if (missing) view = null;
    }
  }

  async function loadDay() {
    try {
      dayOwn = await api('GET', '/api/network?range=24h');
    } catch {
      // The key figures go without their line and change until the next try.
    }
  }

  $effect(() => {
    range;
    untrack(load);
    const id = setInterval(load, RELOAD_MS);
    return () => clearInterval(id);
  });

  $effect(() => {
    if (range === '24h') return;
    untrack(loadDay);
    const id = setInterval(loadDay, RELOAD_MS);
    return () => clearInterval(id);
  });

  // Each live reading updates the rates now; for the five minutes it also becomes the newest point,
  // and the oldest falls off.
  $effect(() => {
    const snap = $snapshot;
    untrack(() => {
      if (snap && view) advance(view, snap);
    });
  });

  const host = $derived($snapshot?.host ?? null);
  const nowRx = $derived(host ? sum(host.net, 'rx') : (view?.host.rx ?? 0));
  const nowTx = $derived(host ? sum(host.net, 'tx') : (view?.host.tx ?? 0));
  const summary = $derived($snapshot?.traffic ?? view);
  const today = $derived({ rx: summary?.todayRx ?? 0, tx: summary?.todayTx ?? 0 });
  const budget = $derived(summary?.budget ?? null);
  const sources = $derived(view ? group(view.containers) : []);
  const chart = $derived(view ? layers(sources, view.host, today) : []);
  const top = $derived(sources.find((s) => s.rx + s.tx > 0) ?? null);
  const todayTotal = $derived(today.rx + today.tx);
  const otherRow = $derived(chart[chart.length - 1]);
  const rows = $derived([
    ...sources.map((s) => ({ ...s, color: chart.find((c) => c.key === s.key)?.color ?? '' })),
    ...(otherRow && otherRow.todayRx + otherRow.todayTx + otherRow.rx + otherRow.tx > 0 ? [otherRow] : []),
  ]);
  const used = $derived(budget ? Math.min(1, budget.usedBytes / budget.limitBytes) : 0);
  const usedTone = $derived(!budget ? '' : budget.usedBytes >= budget.limitBytes ? 'fail' : used >= 0.8 ? 'warn' : '');
  const period = $derived(budget?.period === 'month' ? 'monthly' : 'daily');

  const shownRange = $derived(rangeOf(view?.range ?? range));
  // The last 24 hours: the key figures' lines, and their change against the 24 hours before.
  const day = $derived(range === '24h' ? view : dayOwn);
  const dayIn = $derived(day ? integrate(day.times, day.host.series?.rx) : 0);
  const dayOut = $derived(day ? integrate(day.times, day.host.series?.tx) : 0);
  // A change is shown only for a period mostly on record: just after a start, part of it would be compared
  // with the whole of the one before.
  const dayKnown = $derived(day ? coverage(day.times, day.host.series?.rx, rangeOf('24h').span) >= MIN_COVERAGE : false);
  const mbitLine = (list) => (list ?? []).map((v) => (Number.isFinite(v) ? (v * 8) / 1e6 : null));
  // The range shown: its total, and its change against the period before it.
  const both = (s) => (s ? s.rx.map((v, i) => add(v, s.tx[i])) : []);
  const rangeTotal = $derived(view ? integrate(view.times, both(view.host.series)) : 0);
  const rangeKnown = $derived(view ? coverage(view.times, view.host.series?.rx, shownRange.span) >= MIN_COVERAGE : false);
  const rangeChange = $derived(view?.previous && rangeKnown ? change(rangeTotal, view.previous.in + view.previous.out) : null);
  // Each app's share of the range: its traffic in and out, summed over the range.
  const shares = $derived(view ? chart.map((l) => ({ label: name(l), color: l.color, value: integrate(view.times, both(l.series)) })) : []);
  const shareTotal = $derived(shares.reduce((n, s) => n + s.value, 0));
  // The last 30 days, and the budget of each: the daily limit, or the monthly one spread over its month's days.
  const daily = $derived(view?.daily ?? day?.daily ?? null);
  const hasDaily = $derived(!!daily?.some((d) => (d.in || 0) + (d.out || 0) > 0));
  const limits = $derived(!budget || !daily ? null : daily.map((d) => {
    if (budget.period === 'day') return budget.limitBytes;
    const [yy, mm] = d.day.split('-').map(Number);
    return budget.limitBytes / new Date(yy, mm, 0).getDate();
  }));

  // A media server's streams, by its name: what it plays now and at what bitrate, beside its traffic.
  const streaming = $derived(new Map(streamServices($serviceList).map((svc) => [svc.name, streamSummary([svc])])));

  // playing says what a source streams, e.g. "3 streams, 23 Mbit/s", or '' when it streams nothing. Only a source
  // linked to a service is matched: a container that merely bears a media server's name is not that server.
  function playing(s) {
    const st = s.linked ? streaming.get(s.key) : null;
    if (!(st?.total > 0)) return '';
    const rate = formatBitrate(st.bitrate);
    return `${plural(st.total, 'stream')}${rate ? `, ${rate}` : ''}`;
  }

  // name is what a source is called: the rest of the host's traffic is said in plain words.
  const name = (s) => (s.key ? s.label : 'Other server traffic');

  function sub(s) {
    if (!s.key) return 'What no app accounts for';
    const what = s.members.length === 1 && s.members[0] === s.label ? 'Container' : s.members.join(', ');
    return [what, playing(s)].filter(Boolean).join(' · ');
  }

</script>

<div class="network">
  {#if !host && $snapshot}
    <div class="empty">
      <Icon name="network" size={28} />
      <b>No traffic figures</b>
      <span>Traffic is read from the host's /proc: mount it and set OBSERVARR_HOST_PROC (see the System view).</span>
    </div>
  {:else}
    <div class="kpis">
      <Kpi label="Coming in" value={formatMbit(nowRx)} num={(nowRx * 8) / 1e6} format={(v) => formatMbit((v * 1e6) / 8)} unit="Mbit/s"
        sub={`Peak today ${formatRate(Math.max(summary?.peakRx ?? 0, nowRx))}`}
        spark={mbitLine(day?.host.series?.rx)} sparkColor="var(--down)"
        change={day?.previous && dayKnown ? { ratio: change(dayIn, day.previous.in), against: 'the 24 hours before' } : null} />
      <Kpi label="Going out" value={formatMbit(nowTx)} num={(nowTx * 8) / 1e6} format={(v) => formatMbit((v * 1e6) / 8)} unit="Mbit/s"
        sub={`Peak today ${formatRate(Math.max(summary?.peakTx ?? 0, nowTx))}`}
        spark={mbitLine(day?.host.series?.tx)} sparkColor="var(--up)"
        change={day?.previous && dayKnown ? { ratio: change(dayOut, day.previous.out), against: 'the 24 hours before' } : null} />
      <Kpi label="Today" value={sizeParts(todayTotal)[0]} unit={sizeParts(todayTotal)[1]}
        sub={`${formatSize(today.rx)} in · ${formatSize(today.tx)} out`}
        spark={hasDaily ? daily.slice(-14).map((d) => (d.in || 0) + (d.out || 0)) : null} />
      <Kpi label="Busiest app now" value={top ? formatMbit(top.rx + top.tx) : '—'} unit={top ? 'Mbit/s' : ''}
        sub={top ? top.label : 'No app moves traffic now'} />
    </div>

    <section class="section" aria-labelledby="flow-title">
      <div class="card-head">
        <h2 id="flow-title">Where the traffic goes</h2>
        <RangeSwitch bind:value={range} />
      </div>
      {#if error}
        <p class="error" role="alert">{error}</p>
      {:else if missing}
        <p class="waiting pad">This server keeps no traffic history for {rangeOf(range).text} yet.</p>
      {:else if !view}
        <Skeleton kind="chart" height={280} label="Waiting for the first reading…" />
      {:else}
        <!-- While another range loads, the chart keeps the one it has, dimmed; the new one fades in over it. -->
        <div class="drawn settle" class:busy={view.range !== range} aria-busy={view.range !== range}>
        {#if rangeTotal > 0}
          <p class="span-total">
            <b class="num">{formatSize(rangeTotal)}</b>
            <span class="muted">in {shownRange.text}</span>
            {#if rangeChange !== null}<Delta ratio={rangeChange} against={shownRange.before} />{/if}
          </p>
        {/if}
        <div class="flow">
          <MirrorChart times={view.times} layers={chart} live={view.range === '5m'}
            label={`Traffic by app over ${shownRange.text}, in Mbit/s: incoming above the line, outgoing below; now ${formatMbit(nowRx)} in, ${formatMbit(nowTx)} out`} />
          <!-- The key reads in the frame of the chart: each app's traffic over the range picked, and its share. -->
          <ul class="legend" aria-label={`Apps, ${shownRange.text}`}>
            <li class="legend-head" aria-hidden="true"><span>App</span><span>{shownRange.name}</span></li>
            {#each chart as s, k (s.key)}
              <li>
                <i class="ring" style:color={s.color}></i>
                <span class="who">
                  <span class="name ellipsis" title={name(s)}>{name(s)}</span>
                  <span class="muted ellipsis" title={sub(s)}>{sub(s)}</span>
                </span>
                <span class="fig num">
                  <span>{formatSize(shares[k].value)}</span>
                  <span class="muted">{shareTotal > 0 ? formatPercent(shares[k].value / shareTotal) : '—'}</span>
                </span>
              </li>
            {/each}
          </ul>
        </div>
        <p class="note">
          Mbit/s, stacked: above the line what comes in, below what goes out. Other server traffic is what no app
          accounts for. Apps also count traffic between each other, so a stack can briefly exceed the server's total.
        </p>
        </div>
      {/if}
    </section>

    {#if (view && shareTotal > 0) || hasDaily}
      <div class="pair trend" class:single={!(view && shareTotal > 0) || !hasDaily}>
        {#if view && shareTotal > 0}
          <section class="section" aria-labelledby="share-title">
            <div class="card-head">
              <h2 id="share-title">Share by app</h2>
              <span class="meta">{shownRange.name}</span>
            </div>
            <Donut items={shares} total={formatSize(shareTotal)} caption="in and out" format={formatSize}
              label={`Share of each app in the traffic of ${shownRange.text}: ${shares.filter((x) => x.value > 0).map((x) => `${x.label} ${Math.round((x.value / shareTotal) * 100)}%`).join(', ')}`} />
          </section>
        {/if}
        {#if hasDaily}
          <section class="section" aria-labelledby="daily-title">
            <div class="card-head">
              <h2 id="daily-title">Daily traffic</h2>
              <span class="meta">Last 30 days</span>
            </div>
            <DailyBars days={daily} {limits} height={290}
              limitLabel={budget?.period === 'month' ? 'Monthly budget per day' : 'Daily budget'}
              label={`Traffic per day over the last 30 days${budget ? `, against the ${period} budget` : ''}; today ${formatSize((daily[daily.length - 1].in || 0) + (daily[daily.length - 1].out || 0))} so far`} />
          </section>
        {/if}
      </div>
    {/if}

    <div class="pair">
      <section class="section" aria-labelledby="apps-title">
        <div class="card-head">
          <h2 id="apps-title">By app, now and today</h2>
          <span class="meta">Mbit/s now · totals since midnight</span>
        </div>
        {#if rows.length === 0}
          <p class="waiting">No app traffic yet. Containers with a network of their own are counted.</p>
        {:else}
          <div class="table-wrap">
            <table class="data">
              <caption class="visually-hidden">Traffic by app</caption>
              <thead>
                <tr>
                  <th scope="col">App</th>
                  <th scope="col" class="r">In now</th>
                  <th scope="col" class="r">Out now</th>
                  <th scope="col" class="r">In today</th>
                  <th scope="col" class="r">Out today</th>
                </tr>
              </thead>
              <tbody>
                {#each rows as s (s.key)}
                  {@const share = todayTotal > 0 ? Math.min(1, (s.todayRx + s.todayTx) / todayTotal) : 0}
                  <tr>
                    <td class="lead" title={name(s)}>
                      <span class="app">
                        <!-- A row the chart does not draw keeps an empty swatch, so every name lines up. -->
                        <i class="ring" class:blank={!s.color} style:color={s.color || null}></i>
                        <span class="ellipsis">{name(s)}</span>
                        {#if playing(s)}<span class="plays ellipsis" title={playing(s)}>{playing(s)}</span>{/if}
                      </span>
                      <span class="share" title={`${Math.round(share * 100)}% of today's traffic`}><i style:width={`${share * 100}%`}></i></span>
                    </td>
                    <td class="r" data-label="In now">{formatMbit(s.rx)}</td>
                    <td class="r" data-label="Out now">{formatMbit(s.tx)}</td>
                    <td class="r muted" data-label="In today">{formatSize(s.todayRx)}</td>
                    <td class="r muted" data-label="Out today">{formatSize(s.todayTx)}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}
      </section>

      <section class="section" aria-labelledby="budget-title">
        <div class="card-head">
          <h2 id="budget-title">{budget ? `${period === 'daily' ? 'Daily' : 'Monthly'} budget` : 'Today'}</h2>
          {#if budget}<span class="meta">{budget.period === 'day' ? 'since midnight' : 'this month'}</span>{/if}
        </div>
        {#if budget}
          <p class="pct num {usedTone ? `${usedTone}-text` : ''}">
            {Math.round((budget.usedBytes / budget.limitBytes) * 100)}%<small>of {period} budget</small>
          </p>
          <span class="bar budget-bar {usedTone}" role="img"
            aria-label={`${Math.round(used * 100)}% of the ${period} budget used: ${formatSize(budget.usedBytes)} of ${formatSize(budget.limitBytes)}`}>
            <i style:width={`${used * 100}%`}></i>
          </span>
          <p class="used num">{formatPart(budget.usedBytes, budget.limitBytes)} used</p>
        {:else}
          <dl class="totals">
            <div><dt>In</dt><dd class="num">{formatSize(today.rx)}</dd></div>
            <div><dt>Out</dt><dd class="num">{formatSize(today.tx)}</dd></div>
            <div class="sum"><dt>Total</dt><dd class="num">{formatSize(todayTotal)}</dd></div>
          </dl>
          <p class="waiting hint">Set OBSERVARR_TRAFFIC_BUDGET to follow a daily or monthly budget.</p>
        {/if}
      </section>
    </div>
  {/if}
</div>

<style>
  .network { display: grid; gap: 20px; }
  .card-head { flex-wrap: wrap; }
  .pad { padding: 24px 0; }

  .flow { display: grid; grid-template-columns: minmax(0, 1fr) 260px; gap: 24px; align-items: start; }
  .drawn { transition: opacity var(--dur-base) var(--ease-out); }
  .busy { opacity: 0.45; }
  .legend { list-style: none; margin: 0; padding: 0; display: grid; gap: 2px; min-width: 0; }
  .legend li { display: flex; align-items: flex-start; gap: 10px; padding: 5px 0; min-width: 0; font-size: var(--t-body); }
  .legend .legend-head { font-size: var(--t-small); color: var(--muted); justify-content: space-between; padding-top: 0; }
  .legend .ring { margin-top: 5px; }
  .span-total { display: flex; align-items: center; flex-wrap: wrap; gap: 4px 10px; margin: -6px 0 14px; font-size: var(--t-body); }
  .span-total b { font-size: var(--t-figure); font-weight: 600; letter-spacing: var(--track-figure); }
  .who { display: grid; min-width: 0; flex: 1; line-height: 1.35; }
  .who .name { font-weight: 500; }
  .who .muted, .fig .muted { font-size: var(--t-small); }
  .fig { display: grid; justify-items: end; line-height: 1.35; white-space: nowrap; flex: none; }
  .note { margin-top: 12px; font-size: var(--t-small); color: var(--muted); max-width: 90ch; }

  .pair { display: grid; gap: 20px; grid-template-columns: minmax(0, 2fr) minmax(0, 1fr); align-items: start; }
  .pair.trend { grid-template-columns: minmax(0, 1fr) minmax(0, 1.5fr); align-items: stretch; }
  .pair.single { grid-template-columns: minmax(0, 1fr); }
  .app { display: flex; align-items: center; gap: 8px; min-width: 0; font-weight: 500; }
  .plays { font-size: var(--t-small); font-weight: 400; color: var(--muted); }
  .share { display: block; height: 4px; margin-top: 6px; border-radius: 2px; background: var(--track); overflow: hidden; max-width: 220px; }
  .share i { display: block; height: 100%; border-radius: 2px; background: var(--bar); }

  .pct { font-size: var(--t-figure-lg); font-weight: 600; letter-spacing: var(--track-figure); line-height: 1.2; white-space: nowrap; }
  .pct small { font-size: var(--t-body); font-weight: 400; color: var(--muted); margin-left: 8px; letter-spacing: 0; }
  .budget-bar { display: block; margin: 10px 0 8px; }
  .used { font-size: var(--t-body); color: var(--text2); }

  .totals { margin: 0; display: grid; }
  .totals div { display: flex; justify-content: space-between; gap: 12px; padding: 8px 0; border-bottom: 1px solid var(--border); font-size: var(--t-body); }
  .totals div:first-child { padding-top: 0; }
  .totals dt { color: var(--text2); }
  .totals dd { margin: 0; font-weight: 500; white-space: nowrap; }
  .totals .sum { border-bottom: 0; }
  .totals .sum dd { font-weight: 600; }
  .hint { margin-top: 10px; font-size: var(--t-small); overflow-wrap: anywhere; }

  @container (max-width: 760px) {
    .flow { grid-template-columns: minmax(0, 1fr); }
  }
  @container (max-width: 759px) {
    .pair, .pair.trend { grid-template-columns: minmax(0, 1fr); }
  }
  @container (max-width: 759px) {
    .share { max-width: none; }
  }
</style>
