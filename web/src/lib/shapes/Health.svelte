<script>
  import { now } from '../ticker.js';
  import { formatAge } from '../format.js';

  // Health renders one shape.Health reading. It knows nothing about which app produced it —
  // every adapter that declares the "health" kind fills the same shape.
  let { reading } = $props();

  const data = $derived(reading?.data);
  const up = $derived(!!reading && !reading.error);
  const warnings = $derived(data?.warnings ?? []);
  const age = $derived(reading ? formatAge(reading.at, $now) : '');

  function levelTone(level) {
    if (level === 'error') return 'fail';
    if (level === 'warning') return 'warn';
    return '';
  }

  // levelLabel is read by screen readers only — the dot is decorative (aria-hidden) and otherwise
  // carries the level by colour alone.
  function levelLabel(level) {
    if (level === 'error') return 'Error';
    if (level === 'warning') return 'Warning';
    return 'Info';
  }
</script>

<div class="health">
  {#if !reading}
    <p class="waiting">Waiting for the first check…</p>
  {:else}
    <p class="row">
      <span class="facts">
        <span class="state-word" class:fail-text={!up}><span class="dot {up ? 'ok' : 'fail'}" aria-hidden="true"></span>{up ? 'Up' : 'Down'}</span>
        {#if data}
          {#if data.version}<span title={`version ${data.version}`}>v{data.version}</span>{/if}
          <span title="Response time">{data.latencyMs} ms</span>
        {/if}
      </span>
      <span class="muted age" title={reading.at}>{age}</span>
    </p>
    {#if reading.error}
      <p class="error" role="alert">{reading.error}</p>
    {/if}
    {#if warnings.length}
      <ul class="warnings">
        {#each warnings as w}
          <li>
            <span class="dot {levelTone(w.level)}" aria-hidden="true"></span>
            <span class="visually-hidden">{levelLabel(w.level)}: </span>
            <span class="msg">{w.source ? `${w.source}: ${w.message}` : w.message}</span>
          </li>
        {/each}
      </ul>
    {/if}
  {/if}
</div>

<style>
  .health { display: grid; gap: 8px; }
  .row { display: flex; align-items: baseline; gap: 6px; min-width: 0; }
  .row .facts { flex: 1; color: var(--text2); }
  .row .state-word { flex: none; }
  .age { margin-left: auto; padding-left: 6px; font-size: var(--t-sm); white-space: nowrap; }
  .error { font-size: var(--t-base); }
  .warnings { list-style: none; margin: 0; padding: 0; display: grid; }
  .warnings li {
    display: flex; align-items: baseline; gap: 10px; min-width: 0; padding: 8px 0; border-top: 1px solid var(--border);
    font-size: var(--t-sm); color: var(--text2);
  }
  .warnings .dot { transform: translateY(-1px); }
  .msg { overflow-wrap: anywhere; min-width: 0; }
</style>
