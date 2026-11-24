<script>
  import { now } from '../ticker.js';
  import { formatAge, formatBytes, formatRate } from '../format.js';
  import { mountSummary, mountProblem } from '../storage.js';

  // Mounts renders one shape.Mounts reading compactly: mounts healthy of all, cache used of its limit and the
  // transfer speed. The Storage view shows the details. It knows nothing about which app produced it.
  let { reading } = $props();

  const data = $derived(reading?.data);
  const sum = $derived(mountSummary([{ readings: { mounts: reading } }]));
  const problems = $derived((data?.mounts ?? []).filter((m) => mountProblem(m)).length);
  const age = $derived(reading ? formatAge(reading.at, $now) : '');
  const cacheText = $derived(
    sum.caches === 0 ? 'no cache' :
      sum.cacheMax > 0 ? `cache ${formatBytes(sum.cacheUsed)} / ${formatBytes(sum.cacheMax)}` :
        `cache ${formatBytes(sum.cacheUsed)}`,
  );
</script>

<div class="mounts">
  {#if !reading}
    <p class="waiting">Waiting for the first check…</p>
  {:else}
    <p class="row">
      <span class="label">Mounts</span>
      {#if data}
        <span class="count" class:warn-text={problems > 0}
          title={`${sum.healthy} of ${sum.listed} mount${sum.listed === 1 ? '' : 's'} healthy${problems ? `, ${problems} need a look` : ''}${sum.total > sum.listed ? `, +${sum.total - sum.listed} not listed` : ''}`}>{sum.healthy}/{sum.listed}</span>
      {/if}
      <span class="muted age">{age}</span>
    </p>
    {#if reading.error}
      <p class="error" role="alert">{reading.error}</p>
    {:else if data}
      <div class="meta">
        <span class="facts">
          <span title={cacheText}>{cacheText}</span>
          <span title={`${formatRate(data.speed)}, ${data.activeTransfers} transfer${data.activeTransfers === 1 ? '' : 's'} in progress`}>
            <span aria-hidden="true">⇅</span> {formatRate(data.speed)}
          </span>
        </span>
        <a class="more" href="#/storage">Details in Storage</a>
      </div>
    {/if}
  {/if}
</div>

<style>
  .mounts { display: grid; gap: 8px; }
  .row { display: flex; align-items: baseline; gap: 8px; min-width: 0; padding-top: 12px; border-top: 1px solid var(--border); }
  .label { font-size: var(--t-base); font-weight: 600; }
  .count { font-size: var(--t-sm); color: var(--muted); }
  .count.warn-text { color: var(--warn); }
  .age { margin-left: auto; font-size: var(--t-sm); white-space: nowrap; }
  .error { font-size: var(--t-base); }
  .meta { display: flex; align-items: baseline; gap: 12px; min-width: 0; }
  .meta .facts { flex: 0 1 auto; color: var(--text2); }
  .more { margin-left: auto; flex: none; font-size: var(--t-sm); white-space: nowrap; }
</style>
