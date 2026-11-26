<script>
  import Icon from './icons/Icon.svelte';
  import { arrive } from './motion.js';
  import { firing, severityTone, scrollToAlerts } from './alerts.js';

  // AlertBell sits in the shell's header: how many errors and warnings fire as alerts now, in the colour of the
  // worst, leading to the Alerts section of the System view. The live stream keeps the list current.
  const HASH = '#/system/alerts';
  const counted = $derived(($firing ?? []).filter((a) => a.severity !== 'info'));
  const count = $derived(counted.length);
  const tone = $derived(count ? severityTone(counted[0].severity) : '');
  const label = $derived(count === 0 ? 'Alerts: nothing firing' : `Alerts: ${count} firing`);

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
    position: relative; display: inline-grid; place-items: center; width: 32px; height: 32px; flex: none;
    border-radius: var(--r); color: var(--muted); border: 1px solid transparent;
    transition-property: background-color, color, border-color;
    transition-duration: var(--dur-fast); transition-timing-function: var(--ease-out);
  }
  .bell:hover { color: var(--text); background: var(--surface2); border-color: var(--border2); }
  .bell.warn { color: var(--warn); }
  .bell.fail { color: var(--fail); }
  /* The count: the page colour on the state colour, at least 4.5:1 in both themes. */
  .count {
    position: absolute; top: -3px; right: -5px; min-width: 17px; height: 17px; padding: 0 4px; border-radius: var(--r-pill);
    display: grid; place-items: center; font-size: var(--t-micro); font-weight: var(--w-strong); line-height: 1;
    color: var(--accent-ink); background: var(--accent-fill);
  }
  .bell.warn .count { color: var(--bg); background: var(--warn); }
  .bell.fail .count { color: var(--bg); background: var(--fail); }
</style>
