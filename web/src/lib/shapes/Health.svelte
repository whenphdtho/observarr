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

  function levelIcon(level) {
    if (level === 'error') return '✕';
    if (level === 'warning') return '!';
    return 'i';
  }

  // levelLabel is read by screen readers only — the icon is decorative (aria-hidden) and otherwise
  // carries the level by colour and shape alone.
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
      <span class="chip dotted state" class:ok={up} class:fail={!up}>{up ? 'Up' : 'Down'}</span>
      {#if data}
        {#if data.version}
          <span class="chip num ellipsis" title={`version ${data.version}`}>v{data.version}</span>
        {/if}
        <span class="chip num">{data.latencyMs} ms</span>
      {/if}
      <span class="muted age" title={reading.at}>{age}</span>
    </p>
    {#if reading.error}
      <p class="error" role="alert">{reading.error}</p>
    {/if}
    {#if warnings.length}
      <ul class="warnings">
        {#each warnings as w}
          <li class={`level-${w.level}`}>
            <span class="icon" aria-hidden="true">{levelIcon(w.level)}</span>
            <span class="visually-hidden">{levelLabel(w.level)}: </span>
            <span class="msg">{w.source ? `${w.source}: ${w.message}` : w.message}</span>
          </li>
        {/each}
      </ul>
    {/if}
  {/if}
</div>

<style>
  .health { display: grid; gap: 10px; }
  .row { display: flex; align-items: center; gap: 6px; min-width: 0; }
  .state { flex: none; font-weight: 550; }
  .chip { flex: 0 1 auto; }
  .age { margin-left: auto; padding-left: 6px; font-size: 12px; white-space: nowrap; }
  .error { font-size: 13px; }
  .warnings { list-style: none; margin: 0; padding: 0; display: grid; }
  .warnings li {
    display: flex; align-items: baseline; gap: 9px; min-width: 0; padding: 8px 0; border-top: 1px solid var(--border);
    font-size: 12.5px; color: var(--text2);
  }
  .icon {
    flex: none; width: 16px; height: 16px; border-radius: 50%; display: inline-grid; place-items: center;
    font-size: 10px; font-weight: 700; transform: translateY(2px);
  }
  .level-error .icon { color: var(--fail); background: var(--fail-soft); }
  .level-warning .icon { color: var(--warn); background: var(--warn-soft); }
  .level-info .icon { color: var(--text2); background: var(--surface2); }
  .msg { overflow-wrap: anywhere; min-width: 0; }
</style>
