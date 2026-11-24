<script>
  import { now } from '../ticker.js';
  import { formatAge, formatBytes, formatPercent } from '../format.js';

  // Queue renders one shape.Queue reading. It knows nothing about which app produced it — every
  // adapter that declares the "queue" kind fills the same shape.
  let { reading } = $props();

  const data = $derived(reading?.data);
  const items = $derived(data?.items ?? []);
  const age = $derived(reading ? formatAge(reading.at, $now) : '');
</script>

<div class="queue">
  {#if !reading}
    <p class="waiting">Waiting for the first check…</p>
  {:else}
    <p class="row">
      <span class="label">Queue</span>
      {#if data}<span class="count">{data.total}</span>{/if}
      <span class="muted age">{age}</span>
    </p>
    {#if reading.error}
      <p class="error" role="alert">{reading.error}</p>
    {:else if items.length === 0}
      <p class="waiting">Queue is empty.</p>
    {:else}
      <ul class="items">
        {#each items as item}
          <li>
            <div class="head">
              <span class="title ellipsis" title={item.title}>{item.title}</span>
              <span class="pct num">{formatPercent(item.progress)}</span>
            </div>
            <div class="bar" class:warn={!!item.problem} role="progressbar" aria-valuemin="0" aria-valuemax="100"
              aria-valuenow={Math.round(item.progress * 100)}
              aria-label={`${item.title}: ${formatPercent(item.progress)} done`}>
              <i style:width={formatPercent(item.progress)}></i>
            </div>
            <div class="facts">
              <span class="status" class:warn-text={!!item.problem} title={item.status}>{item.status}</span>
              <span title={`${formatBytes(item.leftBytes)} left of ${formatBytes(item.sizeBytes)}`}>
                {formatBytes(item.leftBytes)} left
              </span>
              {#if item.eta}<span title={`ETA ${item.eta}`}>{item.eta}</span>{/if}
            </div>
            {#if item.problem}
              <p class="problem">{item.problem}</p>
            {/if}
          </li>
        {/each}
      </ul>
    {/if}
  {/if}
</div>

<style>
  .queue { display: grid; gap: 6px; }
  .row { display: flex; align-items: baseline; gap: 8px; min-width: 0; padding-top: 12px; border-top: 1px solid var(--border); }
  .label { font-size: var(--t-base); font-weight: 600; }
  .count { font-size: var(--t-sm); color: var(--muted); }
  .age { margin-left: auto; font-size: var(--t-sm); white-space: nowrap; }
  .error { font-size: var(--t-base); }
  .items { list-style: none; margin: 0; padding: 0; display: grid; }
  .items li { display: grid; gap: 6px; min-width: 0; padding: 10px 0; border-bottom: 1px solid var(--border); }
  .items li:last-child { border-bottom: 0; padding-bottom: 0; }
  .head { display: flex; align-items: baseline; gap: 12px; min-width: 0; }
  .title { flex: 1; font-weight: 550; font-size: var(--t-base); }
  .pct { font-size: var(--t-sm); color: var(--text2); flex: none; }
  .status { text-transform: capitalize; }
  .problem { color: var(--warn); overflow-wrap: anywhere; font-size: var(--t-sm); }
</style>
