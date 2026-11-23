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
    <p class="muted">Waiting for the first check…</p>
  {:else}
    <p class="row">
      <span class="dot" class:ok={up} class:fail={!up} aria-hidden="true"></span>
      <span class="state">{up ? 'Up' : 'Down'}</span>
      {#if data}
        <span class="muted ellipsis" title={`version ${data.version}`}>v{data.version}</span>
        <span class="muted">{data.latencyMs} ms</span>
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
  .health { display: grid; gap: 6px; }
  .row { display: flex; align-items: center; gap: 8px; margin: 0; min-width: 0; }
  .state { flex-shrink: 0; }
  .dot { width: 10px; height: 10px; border-radius: 50%; flex-shrink: 0; }
  .dot.ok { background: var(--ok); }
  .dot.fail { background: var(--fail); }
  .age { margin-left: auto; }
  .ellipsis { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0; }
  .msg { overflow-wrap: anywhere; min-width: 0; }
  .warnings { list-style: none; margin: 0; padding: 0; display: grid; gap: 4px; }
  .warnings li { display: flex; align-items: baseline; gap: 6px; min-width: 0; }
  .icon { flex-shrink: 0; width: 1.2em; text-align: center; }
  .level-error .icon { color: var(--fail); }
  .level-warning .icon { color: var(--warn); }
  .level-info .icon { color: var(--muted); }
  .visually-hidden {
    position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap;
  }
</style>
