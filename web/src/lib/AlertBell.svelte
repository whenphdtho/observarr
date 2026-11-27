<script>
  import Icon from './icons/Icon.svelte';
  import { arrive } from './motion.js';
  import { synced, attention } from './live.js';
  import { scrollToAlerts } from './alerts.js';
  import { tally } from './overview.js';
  import { plural } from './format.js';

  // AlertBell sits in the shell's header and leads to the Alerts section of the System view. Its count is the
  // title's: the problems, in the error colour; without any, the warnings, in the warning colour; without either,
  // none. Its name says both. What is muted is not counted. The live stream keeps the list current.
  const HASH = '#/system/alerts';
  const sums = $derived(tally($synced ? $attention : null));
  const count = $derived(sums.fails || sums.warns);
  const tone = $derived(sums.fails ? 'fail' : sums.warns ? 'warn' : '');
  const label = $derived(count === 0 ? 'Alerts: nothing needs you' :
    `Alerts: ${[sums.fails && plural(sums.fails, 'problem'), sums.warns && plural(sums.warns, 'warning')].filter(Boolean).join(', ')}`);

  // Already there: the hash does not change, so scroll by hand.
  function open(event) {
    if (window.location.hash !== HASH) return;
    event.preventDefault();
    scrollToAlerts();
  }
</script>

<a class="bell {tone}" href={HASH} aria-label={label} title={label} onclick={open}>
  <Icon name="bell" size={17} />
  <!-- A new count rises into place, so a change is noticed without reading. -->
  {#key count}
    {#if count}<span class="count num" aria-hidden="true" in:arrive|global={{ y: 4 }}>{count > 99 ? '99+' : count}</span>{/if}
  {/key}
</a>

<style>
  .bell {
    position: relative; display: inline-grid; place-items: center; width: 34px; height: 34px; flex: none;
    border-radius: var(--r); color: var(--muted); border: 1px solid transparent;
    transition-property: background-color, color, border-color;
    transition-duration: var(--dur-fast); transition-timing-function: var(--ease-out);
  }
  .bell:hover { color: var(--text); background: var(--hover); border-color: var(--border2); }
  /* The count: the page colour on the state colour, at least 4.5:1 in both themes. The bell itself stays neutral. */
  .count {
    position: absolute; top: -3px; right: -5px; min-width: 17px; height: 17px; padding: 0 4px; border-radius: var(--r-pill);
    display: grid; place-items: center; font-size: var(--t-micro); font-weight: var(--w-strong); line-height: 1;
    color: var(--bg);
  }
  .bell.warn .count { background: var(--warn); }
  .bell.fail .count { background: var(--fail); }
</style>
