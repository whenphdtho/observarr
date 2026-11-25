<script>
  import { now } from '../ticker.js';
  import { formatAge } from '../format.js';
  import { streamText, formatBitrate } from '../streams.js';
  import StreamRow from '../StreamRow.svelte';

  // Streams renders one shape.Streams reading: what plays now, then each stream. It knows nothing about which app
  // produced it.
  let { reading } = $props();

  const data = $derived(reading?.data);
  const streams = $derived(data?.streams ?? []);
  const age = $derived(reading ? formatAge(reading.at, $now) : '');
  const summary = $derived(data ? [streamText(data), formatBitrate(data.bitrate)].filter(Boolean).join(' · ') : '');
</script>

<div class="streams">
  {#if !reading}
    <p class="waiting">Waiting for the first check…</p>
  {:else}
    <p class="row">
      <span class="label">Now playing</span>
      {#if data}<span class="count">{data.total}</span>{/if}
      <span class="muted age">{age}</span>
    </p>
    {#if reading.error}
      <p class="error" role="alert">{reading.error}</p>
    {:else if streams.length === 0}
      <p class="waiting">Nothing is playing.</p>
    {:else}
      <p class="summary ellipsis" title={summary}>{summary}</p>
      <ul class="items">
        {#each streams as stream, i (i)}
          <li><StreamRow {stream} detail /></li>
        {/each}
      </ul>
      {#if data.total > streams.length}
        <p class="waiting more">and {data.total - streams.length} more</p>
      {/if}
    {/if}
  {/if}
</div>

<style>
  .streams { display: grid; gap: 6px; }
  .row { display: flex; align-items: baseline; gap: 8px; min-width: 0; padding-top: 12px; border-top: 1px solid var(--border); }
  .label { font-size: var(--t-body); font-weight: 600; }
  .count { font-size: var(--t-small); color: var(--muted); }
  .age { margin-left: auto; font-size: var(--t-small); white-space: nowrap; }
  .error { font-size: var(--t-body); }
  .summary { font-size: var(--t-small); color: var(--text2); }
  .items { list-style: none; margin: 0; padding: 0; display: grid; }
  .items li { min-width: 0; padding: 10px 0; border-bottom: 1px solid var(--border); }
  .items li:last-child { border-bottom: 0; padding-bottom: 0; }
  .more { font-size: var(--t-small); }
</style>
