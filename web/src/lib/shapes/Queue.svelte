<script>
  import { flip } from 'svelte/animate';
  import { now } from '../ticker.js';
  import { arrive, leave, smoothHeight, FLIP, keyed } from '../motion.js';
  import { formatAge, formatSize, formatPercent, formatRate, formatLeft, prettyRelease, sentence } from '../format.js';

  // Queue renders one shape.Queue reading. It knows nothing about which app produced it — every
  // adapter that declares the "queue" kind fills the same shape. A downloader also reports its speed
  // and whether it is paused; both show only when set.
  let { reading } = $props();

  const data = $derived(reading?.data);
  const items = $derived(keyed(data?.items ?? [], (it) => it.title));
  const age = $derived(reading ? formatAge(reading.at, $now) : '');
</script>

<div class="queue" class:paused={data?.paused}>
  {#if !reading}
    <p class="waiting">Waiting for the first check…</p>
  {:else}
    <p class="row">
      <span class="label">Queue</span>
      {#if data}<span class="count">{data.total}</span>{/if}
      {#if data?.speed > 0}
        <span class="speed num ellipsis" title={`Downloading at ${formatRate(data.speed)}`}>{formatRate(data.speed)}</span>
      {/if}
      {#if data?.paused}<span class="paused-word">Paused</span>{/if}
      <span class="muted age">{age}</span>
    </p>
    {#if reading.error}
      <p class="error" role="alert">{reading.error}</p>
    {:else if items.length === 0}
      <p class="waiting">Queue is empty.</p>
    {:else}
      <div use:smoothHeight>
      <ul class="items">
        {#each items as { key, item } (key)}
          <li in:arrive out:leave animate:flip={FLIP}>
            <div class="head">
              <span class="title ellipsis" title={item.title}>{prettyRelease(item.title)}</span>
              <span class="pct num">{formatPercent(item.progress)}</span>
            </div>
            <div class="bar" class:warn={!!item.problem} role="progressbar" aria-valuemin="0" aria-valuemax="100"
              aria-valuenow={Math.round(item.progress * 100)}
              aria-label={`${item.title}: ${formatPercent(item.progress)} done`}>
              <i style:width={formatPercent(item.progress)}></i>
            </div>
            <div class="facts">
              <span class="status" class:warn-text={!!item.problem} title={item.status}>{sentence(item.status)}</span>
              <span title={`${formatSize(item.leftBytes)} to go of ${formatSize(item.sizeBytes)}`}>
                {formatSize(item.leftBytes)} to go
              </span>
              {#if formatLeft(item.eta)}<span>{formatLeft(item.eta)}</span>{/if}
            </div>
            {#if item.problem}
              <p class="problem">{item.problem}</p>
            {/if}
          </li>
        {/each}
      </ul>
      </div>
    {/if}
  {/if}
</div>

<style>
  .queue { display: grid; gap: 6px; }
  .row { display: flex; align-items: baseline; gap: 8px; min-width: 0; padding-top: 12px; border-top: 1px solid var(--border); }
  .label { font-size: var(--t-body); font-weight: 600; }
  .count { font-size: var(--t-small); color: var(--muted); }
  .speed { font-size: var(--t-small); color: var(--text2); }
  .speed::before { content: '·'; margin-right: 8px; color: var(--muted); }
  .paused-word { font-size: var(--t-small); color: var(--text2); }
  .paused-word::before { content: '·'; margin-right: 8px; color: var(--muted); }
  /* A paused queue is not a problem: its bars go grey, nothing else changes. */
  .paused .bar:not(.warn) > i { background: var(--bar-idle); }
  .age { margin-left: auto; font-size: var(--t-small); white-space: nowrap; }
  .error { font-size: var(--t-body); }
  .items { list-style: none; margin: 0; padding: 0; display: grid; }
  .items li { display: grid; gap: 6px; min-width: 0; padding: 10px 0; border-bottom: 1px solid var(--border); }
  .items li:last-child { border-bottom: 0; padding-bottom: 0; }
  .head { display: flex; align-items: baseline; gap: 12px; min-width: 0; }
  .title { flex: 1; font-weight: 500; font-size: var(--t-body); }
  .pct { font-size: var(--t-small); color: var(--text2); flex: none; }
  .problem { color: var(--warn); overflow-wrap: anywhere; font-size: var(--t-small); }
</style>
