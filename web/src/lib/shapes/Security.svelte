<script>
  import { now } from '../ticker.js';
  import { formatAge } from '../format.js';
  import { countOf, formatCount, formatUntil } from '../security.js';

  // Security renders one shape.Security reading compactly: the active bans and the latest decision
  // this machine made. It knows nothing about which app produced it.
  let { reading } = $props();

  const data = $derived(reading?.data);
  const bans = $derived(countOf(data?.byType, 'ban'));
  const last = $derived(data?.recent?.[0] ?? null);
  const age = $derived(reading ? formatAge(reading.at, $now) : '');
  const split = $derived(data ? `${formatCount(data.local)} local · ${formatCount(data.community)} from blocklists` : '');
</script>

<div class="security">
  {#if !reading}
    <p class="waiting">Waiting for the first check…</p>
  {:else}
    <p class="row">
      <span class="label">Blocked</span>
      {#if data}
        <span class="count num" title={`${formatCount(bans, data.capped)} active bans`}>{formatCount(bans, data.capped)}</span>
      {/if}
      <span class="muted age">{age}</span>
    </p>
    {#if reading.error}
      <p class="error" role="alert">{reading.error}</p>
    {:else if data}
      <p class="split muted ellipsis" title={split}>{split}</p>
      {#if last}
        {@const title = `${last.value} · ${last.type} · ${last.scenario}${last.until ? ` · ends ${formatUntil(last.until, $now)}` : ''}`}
        <p class="last" {title}>
          <span class="muted what">Latest</span>
          <span class="value num ellipsis">{last.value}</span>
          <span class="chip ellipsis scenario">{last.scenario || last.type}</span>
        </p>
      {:else}
        <p class="waiting">No decisions made here yet.</p>
      {/if}
    {/if}
  {/if}
</div>

<style>
  .security { display: grid; gap: 6px; }
  .row { display: flex; align-items: center; gap: 8px; min-width: 0; padding-top: 12px; border-top: 1px solid var(--border); }
  .label { font-size: 13px; font-weight: 600; }
  .count {
    min-width: 20px; height: 20px; padding: 0 6px; border-radius: 99px; display: inline-grid; place-items: center;
    font-size: 11.5px; font-weight: 650; background: var(--surface2); color: var(--text2);
  }
  .age { margin-left: auto; font-size: 12px; white-space: nowrap; }
  .error { font-size: 13px; }
  .split { font-size: 12.5px; }
  .last { display: flex; align-items: center; gap: 8px; min-width: 0; font-size: 13px; }
  .what { flex: none; font-size: 12.5px; }
  .value { flex: 0 1 auto; font-weight: 550; }
  .scenario { flex: 0 1 auto; margin-left: auto; }
</style>
