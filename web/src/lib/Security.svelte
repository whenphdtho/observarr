<script>
  import { onMount } from 'svelte';
  import Kpi from './Kpi.svelte';
  import Icon from './icons/Icon.svelte';
  import { api } from './api.js';
  import { connectDocs } from './urls.js';
  import { adapters } from './data.js';
  import { serviceList } from './live.js';
  import { now } from './ticker.js';
  import { formatAge } from './format.js';
  import {
    securityServices, activeBans, formatCount, formatLeft, formatUntil, formatTime,
  } from './security.js';

  // Security shows how Observarr itself is protected (GET /api/security: failed sign-ins, lockouts,
  // sessions, access settings) and, for every service filling the "security" shape, what it blocks.
  const RELOAD_MS = 30 * 1000;
  const RECENT_SHOWN = 10;

  let view = $state(null);
  let error = $state('');
  let dialogEl;
  let opener = null;
  let ending = $state(false);
  let endError = $state('');
  let notice = $state('');
  let showAll = $state({});

  async function load() {
    try {
      view = await api('GET', '/api/security');
      error = '';
    } catch (err) {
      error = err.message;
    }
  }

  onMount(() => {
    load();
    const id = setInterval(load, RELOAD_MS);
    return () => clearInterval(id);
  });

  const blockers = $derived(securityServices($serviceList));
  const bans = $derived(activeBans(blockers));
  // The adapters that can fill the shape name what to connect, so nothing here names an app.
  const sources = $derived($adapters.filter((a) => a.kinds?.includes('security')));
  const failed = $derived(view?.failed ?? null);
  const sessions = $derived(view?.sessions ?? []);
  const others = $derived(Math.max(0, (view?.sessionCount ?? 0) - 1));
  const lastSignIn = $derived(view?.signIns?.[0] ?? null);
  const warnings = $derived((view?.settings ?? []).filter((s) => s.state === 'warn').length);

  function openConfirm() {
    endError = '';
    opener = document.activeElement;
    dialogEl?.showModal();
  }

  function onDialogClose() {
    opener?.focus();
    opener = null;
  }

  function onDialogCancel(event) {
    if (ending) event.preventDefault();
  }

  async function signOutOthers() {
    ending = true;
    endError = '';
    try {
      const res = await api('POST', '/api/security/sign-out-others');
      notice = `Signed out ${res.ended} other session${res.ended === 1 ? '' : 's'}.`;
      dialogEl?.close();
      await load();
    } catch (err) {
      endError = err.message;
    } finally {
      ending = false;
    }
  }

  function settingIcon(state) {
    if (state === 'warn') return 'triangle-alert';
    if (state === 'info') return 'info';
    return 'circle-check';
  }

  function share(count, total) {
    return total > 0 ? Math.max(2, Math.round((count / total) * 100)) : 0;
  }

</script>

<div class="security">
  {#if error}
    <p class="error" role="alert">{error}</p>
  {/if}
  {#if view?.serverLockedSec > 0}
    <div class="banner" role="status">
      <Icon name="triangle-alert" />
      <div class="banner-text">
        <p>
          Many addresses are failing to sign in. For the next {formatLeft(view.serverLockedSec)} only addresses that
          signed in before may try.
        </p>
      </div>
    </div>
  {/if}

  <div class="kpis">
    <Kpi icon="user-x" label="Failed sign-ins · 24 h" value={failed ? formatCount(failed.attempts) : '—'}
      tone={failed?.attempts ? 'warn' : ''}
      sub={!failed ? 'Loading…' : failed.addresses ? `from ${failed.addresses} address${failed.addresses === 1 ? '' : 'es'}` : 'None'} />
    <Kpi icon="lock" label="Locked out" value={view ? view.lockouts.length : '—'}
      unit={view?.lockouts.length === 1 ? 'address' : 'addresses'}
      tone={view?.lockouts.length ? 'warn' : ''}
      sub={!view ? 'Loading…' : view.lockouts.length ? `longest ${formatLeft(view.lockouts[0].seconds)} left` : 'No lockouts'} />
    <Kpi icon="users" label="Active sessions" value={view ? view.sessionCount : '—'}
      sub={!view ? 'Loading…' : others ? `${others} besides yours` : 'Only yours'} />
    {#if blockers.length}
      <Kpi icon="ban" label="Active bans" value={bans.any ? formatCount(bans.bans, bans.capped) : '—'}
        sub={bans.any ? `by ${blockers.length === 1 ? blockers[0].name : `${blockers.length} services`}` : 'Waiting for the first check…'} />
    {:else}
      <Kpi icon="log-in" label="Last sign-in" value={lastSignIn ? lastSignIn.user : '—'}
        sub={lastSignIn ? `${formatAge(lastSignIn.at, $now)}${lastSignIn.address ? ` · ${lastSignIn.address}` : ''}` : 'None yet'} />
    {/if}
  </div>

  <section class="card" aria-labelledby="attempts-title">
    <div class="card-head">
      <h2 id="attempts-title"><Icon name="user-x" size={16} />Sign-in attempts</h2>
      <span class="meta">failed · last 24 h</span>
    </div>
    {#if !failed}
      <p class="waiting">Loading…</p>
    {:else if failed.groups.length === 0}
      <p class="waiting">No failed sign-ins in the last 24 hours.</p>
    {:else}
      <div class="table-wrap">
        <!-- svelte-ignore a11y_no_redundant_roles -->
        <table class="data attempts" role="table">
          <caption class="visually-hidden">Failed sign-ins by address, last 24 hours</caption>
          <!-- svelte-ignore a11y_no_redundant_roles -->
          <thead role="rowgroup">
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <tr role="row">
              <th role="columnheader" scope="col">Address</th>
              <th role="columnheader" scope="col" class="r">Attempts</th>
              <th role="columnheader" scope="col">Last attempt</th>
              <th role="columnheader" scope="col">User names tried</th>
              <th role="columnheader" scope="col">Status</th>
            </tr>
          </thead>
          <!-- svelte-ignore a11y_no_redundant_roles -->
          <tbody role="rowgroup">
            {#each failed.groups as g (g.address)}
              {@const lock = view.lockouts.find((l) => l.address === g.address)}
              {@const users = [...(g.setupToken ? ['setup token'] : []), ...g.users].join(', ') + (g.moreUsers ? ` +${g.moreUsers}` : '')}
              <!-- svelte-ignore a11y_no_redundant_roles -->
              <tr role="row">
                <td role="cell" data-label="Address" class="lead" title={g.address}><span class="ellipsis mono">{g.address}</span></td>
                <td role="cell" data-label="Attempts" class="r">{formatCount(g.attempts)}</td>
                <td role="cell" data-label="Last attempt" title={formatTime(g.last)}>
                  <span class="ellipsis">{formatAge(g.last, $now)}</span>
                </td>
                <td role="cell" data-label="User names" title={users}><span class="ellipsis">{users}</span></td>
                <td role="cell" data-label="Status">
                  {#if lock}
                    <span class="chip warn dotted" title={`Locked out for ${formatLeft(lock.seconds)} more`}>
                      Locked · {formatLeft(lock.seconds)}
                    </span>
                  {:else}
                    <span class="chip">Can retry</span>
                  {/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
      {#if failed.addresses > failed.groups.length || failed.partial}
        <p class="note muted">
          Showing the latest {failed.groups.length} of {failed.addresses} addresses{failed.partial ? ', from the newest records only' : ''}.
        </p>
      {/if}
    {/if}
  </section>

  {#if blockers.length === 0}
    <div class="empty">
      <Icon name="shield" size={28} />
      <b>No intrusion prevention connected</b>
      {#if sources.length}
        <span>
          Add {sources.map((a) => a.name).join(' or ')} on the Apps view to see the addresses it blocks. Observarr only
          reads its decisions, with a key of its own.
        </span>
        <span class="links">
          {#each sources as a (a.type)}
            <a href={connectDocs(a.name)} target="_blank" rel="noopener noreferrer">How to connect {a.name}</a>
          {/each}
          <a href="#/apps">Go to Apps</a>
        </span>
      {:else}
        <span>Connect an intrusion prevention system to see the addresses it blocks.</span>
      {/if}
    </div>
  {:else}
    {#each blockers as svc (svc.id)}
      {@const reading = svc.readings?.security}
      {@const data = reading?.data}
      <section class="card" aria-labelledby={`blocked-${svc.id}`}>
        <div class="card-head">
          <h2 id={`blocked-${svc.id}`}><Icon name="shield" size={16} /><span class="ellipsis" title={`Blocked by ${svc.name}`}>Blocked by {svc.name}</span></h2>
          {#if reading}<span class="meta" title={formatTime(reading.at)}>{formatAge(reading.at, $now)}</span>{/if}
        </div>
        {#if !reading}
          <p class="waiting">Waiting for the first check…</p>
        {:else if reading.error}
          <p class="error" role="alert">{reading.error}</p>
        {:else if data}
          <div class="figures">
            <div class="fig">
              <span class="muted">Active decisions</span>
              <b class="num">{formatCount(data.total, data.capped)}</b>
            </div>
            <div class="fig">
              <span class="muted">Made here</span>
              <b class="num">{formatCount(data.local, data.capped)}</b>
            </div>
            <div class="fig">
              <span class="muted">From blocklists</span>
              <b class="num">{formatCount(data.community, data.capped)}</b>
            </div>
            <div class="fig types">
              <span class="muted">By type</span>
              <span class="chips">
                {#each data.byType as t (t.name)}
                  <span class="chip num" title={`${t.name}: ${formatCount(t.count)}`}>{t.name} {formatCount(t.count)}</span>
                {:else}
                  <span class="muted">—</span>
                {/each}
              </span>
            </div>
          </div>
          {#if data.capped}
            <p class="note muted">The list is longer than Observarr counts; the totals are a lower bound.</p>
          {/if}

          <div class="pair">
            <div>
              <h3>By origin</h3>
              {#if data.byOrigin.length === 0}
                <p class="waiting">No decisions.</p>
              {:else}
                <ul class="bars">
                  {#each data.byOrigin as o (o.name)}
                    <li title={`${o.name}: ${formatCount(o.count)}`}>
                      <span class="ellipsis name">{o.name}</span>
                      <span class="num">{formatCount(o.count)}</span>
                      <span class="bar" aria-hidden="true"><i style:width={`${share(o.count, data.total)}%`}></i></span>
                    </li>
                  {/each}
                </ul>
              {/if}
            </div>
            <div>
              <h3>Top scenarios here</h3>
              {#if data.topScenarios.length === 0}
                <p class="waiting">Nothing detected on this machine.</p>
              {:else}
                <ul class="bars">
                  {#each data.topScenarios as sc (sc.name)}
                    <li title={`${sc.name}: ${formatCount(sc.count)}`}>
                      <span class="ellipsis name">{sc.name}</span>
                      <span class="num">{formatCount(sc.count)}</span>
                      <span class="bar warn" aria-hidden="true"><i style:width={`${share(sc.count, data.local)}%`}></i></span>
                    </li>
                  {/each}
                </ul>
              {/if}
            </div>
          </div>

          <h3>Latest decisions made here</h3>
          {#if data.recent.length === 0}
            <p class="waiting">None: every active decision comes from a blocklist.</p>
          {:else}
            {@const shown = showAll[svc.id] ? data.recent : data.recent.slice(0, RECENT_SHOWN)}
            <div class="table-wrap">
              <!-- svelte-ignore a11y_no_redundant_roles -->
              <table class="data decisions" role="table">
                <caption class="visually-hidden">Latest decisions made by {svc.name}</caption>
                <!-- svelte-ignore a11y_no_redundant_roles -->
                <thead role="rowgroup">
                  <!-- svelte-ignore a11y_no_redundant_roles -->
                  <tr role="row">
                    <th role="columnheader" scope="col">Address</th>
                    <th role="columnheader" scope="col">Type</th>
                    <th role="columnheader" scope="col">Scenario</th>
                    <th role="columnheader" scope="col">Origin</th>
                    <th role="columnheader" scope="col">Ends</th>
                  </tr>
                </thead>
                <!-- svelte-ignore a11y_no_redundant_roles -->
                <tbody role="rowgroup">
                  {#each shown as d, i (i)}
                    <!-- svelte-ignore a11y_no_redundant_roles -->
                    <tr role="row">
                      <td role="cell" data-label="Address" class="lead" title={`${d.value} (${d.scope})`}>
                        <span class="ellipsis mono">{d.value}</span>
                      </td>
                      <td role="cell" data-label="Type"><span class="chip" class:fail={d.type === 'ban'}>{d.type}</span></td>
                      <td role="cell" data-label="Scenario" title={d.scenario}><span class="ellipsis">{d.scenario || '—'}</span></td>
                      <td role="cell" data-label="Origin" title={d.origin}><span class="ellipsis">{d.origin}</span></td>
                      <td role="cell" data-label="Ends" title={formatTime(d.until)}>
                        <span class="ellipsis">{d.until ? formatUntil(d.until, $now) : '—'}</span>
                      </td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            </div>
            {#if data.recent.length > RECENT_SHOWN}
              <button type="button" class="quiet more" aria-expanded={!!showAll[svc.id]}
                onclick={() => { showAll[svc.id] = !showAll[svc.id]; }}>
                {showAll[svc.id] ? 'Show fewer' : `Show all ${data.recent.length}`}
              </button>
            {/if}
          {/if}
        {/if}
      </section>
    {/each}
  {/if}

  <div class="pair">
    <section class="card" aria-labelledby="sessions-title">
      <div class="card-head">
        <h2 id="sessions-title"><Icon name="users" size={16} />Active sessions</h2>
        {#if view}<span class="meta num">{view.sessionCount}</span>{/if}
      </div>
      {#if !view}
        <p class="waiting">Loading…</p>
      {:else}
        <ul class="list">
          {#each sessions as s, i (i)}
            <li>
              <span class="who">
                <span class="ellipsis name" title={s.user}>{s.user}</span>
                {#if s.current}<span class="chip ok">This browser</span>{/if}
              </span>
              <span class="muted when" title={`Signed in ${formatTime(s.created)}, ends ${formatTime(s.expires)}`}>
                signed in {formatAge(s.created, $now)} · ends {formatUntil(s.expires, $now)}
              </span>
            </li>
          {/each}
        </ul>
        {#if view.sessionCount > sessions.length}
          <p class="note muted">Showing the newest {sessions.length}.</p>
        {/if}
        <div class="action">
          <button type="button" class="quiet" disabled={others === 0} onclick={openConfirm}>
            <Icon name="log-out" size={15} />Sign out all other sessions
          </button>
          {#if notice}<p class="muted" role="status">{notice}</p>{/if}
        </div>
      {/if}
    </section>

    <section class="card" aria-labelledby="signins-title">
      <div class="card-head">
        <h2 id="signins-title"><Icon name="log-in" size={16} />Recent sign-ins</h2>
      </div>
      {#if !view}
        <p class="waiting">Loading…</p>
      {:else if view.signIns.length === 0}
        <p class="waiting">No sign-ins recorded.</p>
      {:else}
        <ul class="list">
          {#each view.signIns as s, i (i)}
            <li>
              <span class="who">
                <span class="ellipsis name" title={s.user}>{s.user}</span>
                {#if s.via !== 'password'}<span class="chip">{s.via === 'proxy' ? 'via proxy' : 'setup'}</span>{/if}
              </span>
              <span class="muted when" title={formatTime(s.at)}>
                {formatAge(s.at, $now)}{s.address ? ` · ${s.address}` : ''}
              </span>
            </li>
          {/each}
        </ul>
      {/if}
    </section>
  </div>

  <section class="card" aria-labelledby="access-title">
    <div class="card-head">
      <h2 id="access-title"><Icon name="shield-check" size={16} />Access settings</h2>
      {#if view}
        <span class="chip" class:ok={warnings === 0} class:warn={warnings > 0}>
          {warnings === 0 ? 'No warnings' : `${warnings} warning${warnings === 1 ? '' : 's'}`}
        </span>
      {/if}
    </div>
    {#if !view}
      <p class="waiting">Loading…</p>
    {:else}
      <ul class="checks">
        {#each view.settings as s (s.id)}
          <li class={s.state}>
            <span class="state" aria-hidden="true"><Icon name={settingIcon(s.state)} size={16} /></span>
            <span class="visually-hidden">{s.state === 'warn' ? 'Warning' : s.state === 'info' ? 'Note' : 'OK'}: </span>
            <span class="text">
              <span class="line">
                <span class="label">{s.label}</span>
                <span class="value ellipsis" title={s.value}>{s.value}</span>
              </span>
              <span class="detail">{s.detail}</span>
            </span>
          </li>
        {/each}
      </ul>
    {/if}
  </section>
</div>

<dialog bind:this={dialogEl} aria-labelledby="end-title" onclose={onDialogClose} oncancel={onDialogCancel}>
  <h2 id="end-title">Sign out all other sessions?</h2>
  <p class="muted">
    {others} other session{others === 1 ? '' : 's'} will end, of every user, on every device. This browser stays signed in.
  </p>
  {#if endError}<p class="error" role="alert">{endError}</p>{/if}
  <div class="actions">
    <button type="button" class="quiet" disabled={ending} onclick={() => dialogEl?.close()}>Cancel</button>
    <button type="button" disabled={ending} onclick={signOutOthers}>Sign out others</button>
  </div>
</dialog>

<style>
  .security { display: grid; gap: 16px; }
  .note { font-size: 12.5px; margin-top: 10px; }
  .mono { font-variant-numeric: tabular-nums; }
  h3 { font-size: 12.5px; font-weight: 600; color: var(--text2); margin: 16px 0 8px; }

  .figures { display: grid; gap: 12px; grid-template-columns: repeat(4, minmax(0, 1fr)); }
  .fig {
    display: grid; gap: 2px; align-content: space-between; min-width: 0; padding: 10px 12px; border-radius: var(--r-md); background: var(--surface2);
    font-size: 12.5px;
  }
  .fig b { font-size: 18px; font-weight: 600; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .chips { display: flex; gap: 4px; flex-wrap: wrap; min-width: 0; padding-top: 3px; }

  .pair { display: grid; gap: 16px; grid-template-columns: repeat(2, minmax(0, 1fr)); align-items: start; }
  .card .pair h3 { margin-top: 18px; }
  .bars { list-style: none; margin: 0; padding: 0; display: grid; gap: 9px; }
  .bars li {
    display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 4px 10px; font-size: 13px; min-width: 0;
  }
  .bars .name { font-weight: 500; }
  .bars .num { color: var(--text2); white-space: nowrap; }
  .bars .bar { grid-column: 1 / -1; }

  .more { margin-top: 10px; }

  .list { list-style: none; margin: 0; padding: 0; }
  .list li {
    display: grid; gap: 2px; padding: 9px 0; border-bottom: 1px solid var(--border); min-width: 0; font-size: 13px;
  }
  .list li:last-child { border-bottom: 0; }
  .who { display: flex; align-items: center; gap: 8px; min-width: 0; }
  .who .name { font-weight: 550; flex: 0 1 auto; }
  .who .chip { flex: none; }
  .when { font-size: 12.5px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .action { display: grid; gap: 8px; justify-items: start; margin-top: 14px; font-size: 13px; }

  .checks { list-style: none; margin: 0; padding: 0; display: grid; gap: 4px 24px; grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .checks li { display: flex; gap: 11px; align-items: flex-start; padding: 10px 0; border-bottom: 1px solid var(--border); min-width: 0; }
  .checks li:last-child { border-bottom: 0; }
  .checks .state {
    width: 28px; height: 28px; border-radius: 8px; display: grid; place-items: center; flex: none;
    background: var(--ok-soft); color: var(--ok);
  }
  .checks .info .state { background: var(--surface2); color: var(--text2); }
  .checks .warn .state { background: var(--warn-soft); color: var(--warn); }
  .checks .text { display: grid; gap: 2px; min-width: 0; flex: 1; }
  .checks .line { display: flex; align-items: baseline; gap: 10px; min-width: 0; }
  .checks .label { font-weight: 550; font-size: 13.5px; white-space: nowrap; }
  .checks .value { margin-left: auto; font-size: 12.5px; color: var(--text2); }
  .checks .warn .value { color: var(--warn); }
  .checks .detail { font-size: 12.5px; color: var(--muted); overflow-wrap: anywhere; }

  dialog p { margin: 8px 0 14px; }

  @container (max-width: 820px) {
    .pair, .checks { grid-template-columns: minmax(0, 1fr); }
    .figures { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  }
  /* Narrow: each row is a small card. The address leads, with its state (lockout, decision type) beside it. */
  @container (max-width: 759px) {
    .attempts tbody tr {
      grid-template-columns: minmax(0, 4fr) minmax(0, 5fr);
      grid-template-areas: 'addr status' 'count last' 'users users';
    }
    .decisions tbody tr {
      grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
      grid-template-areas: 'addr type' 'scen scen' 'origin ends';
    }
    .data td[data-label='Address'] { grid-area: addr; grid-column: auto; align-self: center; }
    .attempts td[data-label='Status'], .decisions td[data-label='Type'] { justify-content: flex-end; align-self: center; }
    .attempts td[data-label='Status']::before, .decisions td[data-label='Type']::before { display: none; }
    .attempts td[data-label='Status'] { grid-area: status; }
    .attempts td[data-label='Attempts'] { grid-area: count; }
    .attempts td[data-label='Last attempt'] { grid-area: last; }
    .attempts td[data-label='User names'] { grid-area: users; }
    .decisions td[data-label='Type'] { grid-area: type; }
    .decisions td[data-label='Scenario'] { grid-area: scen; }
    .decisions td[data-label='Origin'] { grid-area: origin; }
    .decisions td[data-label='Ends'] { grid-area: ends; }
    /* Paired figures put their label above the value, so both fit half a phone's width. */
    .attempts td[data-label='Attempts'], .attempts td[data-label='Last attempt'],
    .decisions td[data-label='Origin'], .decisions td[data-label='Ends'] { display: block; }
    .attempts td[data-label='Attempts']::before, .attempts td[data-label='Last attempt']::before,
    .decisions td[data-label='Origin']::before, .decisions td[data-label='Ends']::before { display: block; }
    .data td > .ellipsis { display: block; }
    .attempts td[data-label='User names'] > .ellipsis, .decisions td[data-label='Scenario'] > .ellipsis { flex: 1; }
  }
</style>
