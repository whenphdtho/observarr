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
        <span class="count num" class:warn={problems > 0}
          title={`${sum.healthy} of ${sum.listed} mount${sum.listed === 1 ? '' : 's'} healthy${problems ? `, ${problems} need a look` : ''}${sum.total > sum.listed ? `, +${sum.total - sum.listed} not listed` : ''}`}>{sum.healthy}/{sum.listed}</span>
      {/if}
      <span class="muted age">{age}</span>
    </p>
    {#if reading.error}
      <p class="error" role="alert">{reading.error}</p>
    {:else if data}
      <div class="meta">
        <span class="chip num" title={cacheText}>{cacheText}</span>
        <span class="chip num" title={`${formatRate(data.speed)}, ${data.activeTransfers} transfer${data.activeTransfers === 1 ? '' : 's'} in progress`}>
          <span aria-hidden="true">⇅</span> {formatRate(data.speed)}
        </span>
        <a class="more" href="#/storage">Details in Storage</a>
      </div>
    {/if}
  {/if}
</div>

<style>
  .mounts { display: grid; gap: 8px; }
  .row { display: flex; align-items: center; gap: 8px; min-width: 0; padding-top: 12px; border-top: 1px solid var(--border); }
  .label { font-size: 13px; font-weight: 600; }
  .count {
    min-width: 20px; height: 20px; padding: 0 6px; border-radius: 99px; display: inline-grid; place-items: center;
    font-size: 11.5px; font-weight: 650; background: var(--surface2); color: var(--text2);
  }
  .count.warn { background: var(--warn-soft); color: var(--warn); }
  .age { margin-left: auto; font-size: 12px; white-space: nowrap; }
  .error { font-size: 13px; }
  .meta { display: flex; align-items: center; gap: 6px; min-width: 0; }
  .meta .chip { flex: 0 1 auto; min-width: 0; }
  .more { margin-left: auto; flex: none; font-size: 12.5px; white-space: nowrap; }
</style>
