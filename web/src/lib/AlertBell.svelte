<script>
  import { onMount } from 'svelte';
  import Icon from './icons/Icon.svelte';
  import { firing, loadFiring, severityTone, scrollToAlerts } from './alerts.js';

  // AlertBell sits in the shell's header: how many alerts fire now, in the colour of the worst, leading to the
  // Alerts section of the System view. It asks every 30 s; the answer is a short list from memory.
  const HASH = '#/system/alerts';
  const count = $derived($firing?.length ?? 0);
  const tone = $derived(count ? severityTone($firing[0].severity) : '');
  const label = $derived(count === 0 ? 'Alerts: nothing firing' : `Alerts: ${count} firing`);

  // Already there: the hash does not change, so scroll by hand.
  function open(event) {
    if (window.location.hash !== HASH) return;
    event.preventDefault();
    scrollToAlerts();
  }

  onMount(() => {
    loadFiring();
    const id = setInterval(loadFiring, 30 * 1000);
    return () => clearInterval(id);
  });
</script>

<a class="bell {tone}" href={HASH} aria-label={label} title={label} onclick={open}>
  <Icon name="bell" size={17} />
  {#if count}<span class="count num" aria-hidden="true">{count > 99 ? '99+' : count}</span>{/if}
</a>

<style>
  .bell {
    position: relative; display: inline-grid; place-items: center; width: 32px; height: 32px; flex: none;
    border-radius: var(--r); color: var(--muted); border: 1px solid transparent;
    transition: background 140ms var(--ease), color 140ms var(--ease), border-color 140ms var(--ease);
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
