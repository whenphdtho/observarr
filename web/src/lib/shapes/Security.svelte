<script>
  import { now } from '../ticker.js';
  import { formatAge, formatCount, formatUntil } from '../format.js';

  // Security renders one shape.Security reading compactly: the addresses blocked and the latest decision
  // this machine made. It knows nothing about which app produced it.
  let { reading } = $props();

  const data = $derived(reading?.data);
  const last = $derived(data?.recent?.[0] ?? null);
  const age = $derived(reading ? formatAge(reading.at, $now) : '');
  const split = $derived(data ? `${formatCount(data.local)} blocked here · ${formatCount(data.community)} from blocklists` : '');
</script>

<div class="security">
  {#if !reading}
    <p class="waiting">Waiting for the first check…</p>
  {:else}
    <p class="row">
      <span class="label">Blocked addresses</span>
      {#if data}
        <span class="count num">{formatCount(data.total, data.capped)}</span>
      {/if}
      <span class="muted age">{age}</span>
    </p>
    {#if reading.error}
      <p class="error" role="alert">{reading.error}</p>
    {:else if data}
      <p class="split muted ellipsis" title={split}>{split}</p>
      {#if last}
        {@const title = `${last.value} · ${last.scenario || last.type}${last.until ? ` · ends ${formatUntil(last.until, $now)}` : ''}`}
        <p class="last" {title}>
          <span class="muted what">Latest</span>
          <span class="value num ellipsis">{last.value}</span>
          <span class="muted ellipsis scenario">{last.scenario || last.type}</span>
        </p>
      {:else}
        <p class="waiting">Nothing was blocked from here yet.</p>
      {/if}
    {/if}
  {/if}
</div>

<style>
  .security { display: grid; gap: 6px; }
  .row { display: flex; align-items: baseline; gap: 8px; min-width: 0; padding-top: 12px; border-top: 1px solid var(--border); }
  .label { font-size: var(--t-body); font-weight: 600; }
  .count { font-size: var(--t-small); color: var(--muted); }
  .age { margin-left: auto; font-size: var(--t-small); white-space: nowrap; }
  .error { font-size: var(--t-body); }
  .split { font-size: var(--t-small); }
  .last { display: flex; align-items: baseline; gap: 8px; min-width: 0; font-size: var(--t-body); }
  .what { flex: none; font-size: var(--t-small); }
  .value { flex: 0 1 auto; font-weight: 500; }
  .scenario { flex: 0 1 auto; margin-left: auto; font-size: var(--t-small); }
</style>
