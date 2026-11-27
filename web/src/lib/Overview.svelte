<script>
  import { flip } from 'svelte/animate';
  import { untrack } from 'svelte';
  import AppIcon from './AppIcon.svelte';
  import Fold from './Fold.svelte';
  import Rail from './Rail.svelte';
  import Traffic from './Traffic.svelte';
  import Icon from './icons/Icon.svelte';
  import Skeleton from './Skeleton.svelte';
  import { arrive, leave, smoothHeight, FLIP } from './motion.js';
  import { adapters, adapterFor } from './data.js';
  import { synced, attention } from './live.js';
  import { matches } from './status.js';
  import { now } from './ticker.js';
  import { issues, explain, actions, downloads, playing } from './overview.js';

  // Overview is the landing view. Its title, in the shell, states the situation; here follow what needs attention
  // (each row opens in place to say what it means and where to go), the traffic over time, and what plays and
  // downloads now. services is the (search-filtered) list, total how many apps there are in all, query the
  // search; onjump(id) shows an app's card, onadd opens the Add apps page.
  let { services, total, query = '', onjump, onadd } = $props();

  // A few rows are enough here; the rest are a click away.
  const MAX_ROWS = 5;

  // The rows are the server's list, the one the alerts and the bell count from, narrowed by the search. The clock
  // only moves a row's "12 min" on, so it is read by the minute.
  const minute = $derived(Math.floor($now / 60000) * 60000);
  const found = $derived(issues(($attention ?? []).filter((it) => matches(query, it.title, it.message, it.name)), minute));
  const rows = $derived(found.rows);
  const fails = $derived(rows.filter((r) => r.tone === 'fail').length);
  const byId = $derived(new Map(services.map((s) => [s.id, s])));
  const queue = $derived(downloads(services));
  const streams = $derived(playing(services));

  // One row is open at a time.
  let open = $state(null);

  // Rows that appear after the first reading are lit for a moment, so a new problem is noticed; the rows already
  // there when the view opens are not.
  let known = null;
  let fresh = $state({});
  $effect(() => {
    if (!$synced) return;
    const keys = rows.map((r) => r.key);
    if (known === null) {
      known = new Set(keys);
      return;
    }
    const added = keys.filter((k) => !known.has(k));
    known = new Set(keys);
    if (added.length === 0) return;
    fresh = { ...untrack(() => fresh), ...Object.fromEntries(added.map((k) => [k, true])) };
    setTimeout(() => {
      fresh = Object.fromEntries(Object.entries(fresh).filter(([k]) => !added.includes(k)));
    }, 1600);
  });

  // The icon of a row about no app: the view it leads to.
  const VIEW_ICONS = { '#/storage': 'hard-drive', '#/security': 'shield', '#/network': 'network', '#/system': 'server' };
</script>

{#snippet lines(list, more, what)}
  <div use:smoothHeight>
    <ul class="lines">
      {#each list.slice(0, MAX_ROWS) as r (r.key)}
        <li in:arrive out:leave animate:flip={FLIP}>
          <span class="l-title ellipsis" title={r.raw}>{r.title}</span>
          <span class="l-note {r.tone}" title={r.hint || null}>{r.note}</span>
          <span class="bar" class:idle={r.idle} class:warn={r.tone === 'warn' && what === 'downloads'} role="progressbar"
            aria-valuemin="0" aria-valuemax="100" aria-valuenow={parseInt(r.pct, 10)} aria-label={`${r.title}: ${r.pct}`}>
            <i style:width={r.pct}></i>
          </span>
        </li>
      {/each}
    </ul>
  </div>
  {#if more > 0}<a class="go more" href="#/apps">{more} more in Apps<Icon name="chevron-right" size={14} /></a>{/if}
{/snippet}

<div class="overview">
  {#if !$synced || $attention === null}
    <Skeleton count={2} label="Waiting for the first reading…" />
  {:else}
    {#if total === 0}
      <section class="section first settle" aria-labelledby="first-title">
        <div>
          <h2 id="first-title">No apps watched yet</h2>
          <p>Observarr looks for your apps on this server; you pick the ones it should watch. Nothing is added without your click.</p>
        </div>
        <button type="button" onclick={onadd}><Icon name="plus" size={16} />Add your apps</button>
      </section>
    {/if}
    {#if rows.length > 0}
      <section class="attention" class:has-fail={fails > 0} aria-labelledby="attention-title">
        <h2 id="attention-title" class="visually-hidden">Needs attention</h2>
        <div use:smoothHeight>
          <ul class="issues">
            {#each rows as row (row.key)}
              {@const svc = row.service === null ? null : byId.get(row.service) ?? null}
              {@const isOpen = open === row.key}
              <li class:open={isOpen} class:fresh={fresh[row.key]} class={row.tone} in:arrive out:leave animate:flip={FLIP}>
                <button type="button" class="issue" aria-expanded={isOpen} aria-controls={`more-${row.key}`}
                  onclick={() => { open = isOpen ? null : row.key; }}>
                  <span class="marker {row.tone}" aria-hidden="true"></span>
                  <span class="tile-icon" aria-hidden="true">
                    {#if svc}<AppIcon adapter={adapterFor($adapters, svc.type)} size={19} />{:else}<Icon name={VIEW_ICONS[row.view] ?? 'server'} size={19} />{/if}
                  </span>
                  <span class="what">
                    <span class="visually-hidden">{row.tone === 'fail' ? 'Error' : 'Warning'}: </span>
                    <span class="title">{row.title}</span>
                    {#if row.detail}<span class="detail ellipsis">{row.detail}</span>{/if}
                  </span>
                  {#if row.value}<span class="value num {row.tone}">{row.value}</span>{/if}
                  <span class="turn" class:up={isOpen} aria-hidden="true"><Icon name="chevron-down" size={16} /></span>
                </button>
                <div class="fold" class:open={isOpen} id={`more-${row.key}`} inert={!isOpen}>
                  <div>
                    <div class="more-body">
                      <div class="said">{#each explain(row) as text, i (i)}<p>{text}</p>{/each}</div>
                      <div class="acts">
                        {#each actions(row, svc) as act, i (act.label)}
                          {#if act.jump !== undefined}
                            <button type="button" class:quiet={i > 0} onclick={() => onjump(act.jump)}>{act.label}</button>
                          {:else}
                            <a class="button" class:quiet={i > 0} href={act.href} target={act.external ? '_blank' : null}
                              rel={act.external ? 'noopener noreferrer' : null}>
                              {act.label}{#if act.external}<Icon name="external-link" size={14} />{/if}
                            </a>
                          {/if}
                        {/each}
                      </div>
                    </div>
                  </div>
                </div>
              </li>
            {/each}
          </ul>
        </div>
      </section>
    {/if}
    {#if found.notes.length > 0}
      <p class="notes settle">
        <b>Good to know</b>
        {#each found.notes as n (n.key)}<a href={n.view}>{n.text}</a>{/each}
      </p>
    {/if}
  {/if}

  <Traffic />

  {#if streams.total > 0 || queue.any}
    <div class="pair" class:single={!(streams.total > 0 && queue.any)}>
      {#if streams.total > 0}
        <Fold id="playing" title="Now playing" summary={streams.summary}>
          {@render lines(streams.rows, streams.total - Math.min(MAX_ROWS, streams.rows.length), 'streams')}
        </Fold>
      {/if}
      {#if queue.any}
        <Fold id="downloads" title="Downloads" summary={queue.summary}>
          {#if queue.rows.length === 0}
            <p class="waiting">Nothing is downloading.</p>
          {:else}
            {@render lines(queue.rows, queue.rows.length - MAX_ROWS, 'downloads')}
          {/if}
        </Fold>
      {/if}
    </div>
  {/if}

  <div class="inline-rail">
    <Rail {services} {onjump} />
  </div>
</div>

<style>
  .overview { display: grid; gap: 20px; }

  /* The first start: one thing to do. */
  .first { display: flex; align-items: center; justify-content: space-between; gap: 16px 24px; flex-wrap: wrap; padding: 22px 24px; }
  .first div { flex: 1 1 320px; display: grid; gap: 4px; }
  .first p { color: var(--text2); max-width: 62ch; }
  .first button { padding: 8px 16px; }

  /* What needs attention: the one panel whose outline leans towards its worst state. Its rows carry the state
     on a marker and on their figure, nowhere else. */
  .attention {
    background: var(--surface); border: 1px solid var(--warn-edge); border-radius: var(--r-lg);
    padding: 8px; min-width: 0; box-shadow: var(--lift);
  }
  .attention.has-fail { border-color: var(--fail-edge); }
  .issues { list-style: none; margin: 0; padding: 0; display: grid; gap: 2px; }
  .issues li { border-radius: var(--r-md); transition: background-color var(--dur-base) var(--ease-out); min-width: 0; }
  .issues li.open { background: var(--hover); }
  /* A problem that just appeared: its row lights once and settles. */
  .fresh { animation: fresh var(--dur-glow) var(--ease-out); }
  .fresh.fail { animation-name: fresh-fail; }
  @keyframes fresh { from { background: var(--warn-flash); } }
  @keyframes fresh-fail { from { background: var(--fail-flash); } }
  .issue {
    width: 100%; display: grid; grid-template-columns: 4px 36px minmax(0, 1fr) auto 16px; gap: 14px; align-items: center;
    padding: 10px 12px 10px 8px; border: 0; border-radius: var(--r-md); background: transparent;
    color: var(--text); font-weight: 400; text-align: left; white-space: normal;
  }
  .issue:hover:not(:disabled) { background: var(--hover); border: 0; }
  .issue:active:not(:disabled) { transform: none; }
  .issue:focus-visible { outline-offset: -2px; }
  .what { display: grid; gap: 1px; min-width: 0; }
  .title { font-size: var(--t-h3); font-weight: 600; overflow-wrap: anywhere; }
  .detail { font-size: var(--t-small); color: var(--text2); }
  /* An opened row says its line in full. */
  .open .detail { white-space: normal; overflow-wrap: anywhere; }
  .value { font-family: var(--font-display); font-weight: 600; white-space: nowrap; }
  .value.fail { color: var(--fail-ink); }
  .value.warn { color: var(--warn-ink); }
  /* The chevron keeps the last column, with or without a figure before it. */
  .issue .turn { grid-column: 5; }
  .turn.up { transform: rotate(180deg); }
  .more-body { display: flex; flex-wrap: wrap; align-items: center; gap: 12px 20px; padding: 4px 16px 16px 76px; }
  .said { flex: 1 1 320px; display: grid; gap: 8px; max-width: 62ch; color: var(--text2); overflow-wrap: anywhere; }
  .acts { display: flex; flex-wrap: wrap; gap: 8px; }

  /* What is only good to know: one quiet line. */
  .notes { display: flex; flex-wrap: wrap; gap: 2px 14px; padding: 0 12px; margin-top: -8px; font-size: var(--t-small); color: var(--muted); }
  .notes b { font-weight: 500; color: var(--text2); }
  .notes a { color: inherit; text-decoration: none; }
  .notes a:hover { color: var(--text); text-decoration: underline; }

  .pair { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 14px; align-items: start; }
  .pair.single { grid-template-columns: minmax(0, 1fr); }
  .lines { list-style: none; margin: 0; padding: 0; display: grid; gap: 14px; }
  .lines li { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 6px 12px; align-items: baseline; min-width: 0; }
  .l-title { font-weight: 500; }
  .l-note { font-size: var(--t-small); color: var(--muted); white-space: nowrap; }
  .l-note.warn { color: var(--warn-ink); }
  .lines .bar { grid-column: 1 / -1; }
  .more { margin-top: 14px; }

  .inline-rail { display: none; --dot-ground: var(--bg); }
  @media (max-width: 1199px) {
    .inline-rail { display: block; padding-top: 20px; border-top: 1px solid var(--border); }
  }
  @container (max-width: 760px) {
    .pair { grid-template-columns: minmax(0, 1fr); }
  }
  @container (max-width: 520px) {
    .attention { padding: 4px; }
    .issue { grid-template-columns: 4px 36px minmax(0, 1fr) auto 16px; gap: 10px; padding: 10px 8px 10px 6px; }
    .more-body { padding: 2px 12px 14px 20px; }
  }
</style>
