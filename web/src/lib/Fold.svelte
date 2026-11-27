<script>
  import { untrack } from 'svelte';
  import Icon from './icons/Icon.svelte';
  import { recall, remember } from './prefs.js';

  // Fold is a panel of the Overview that folds: a head with a chevron, the title and a one-line summary that stays
  // when the panel is shut, and the body below. Whether it is open is remembered in this browser under id. The
  // whole head opens and shuts it; controls (a snippet, e.g. a range switch) sit at its end and stay their own.
  // summary is text, or a snippet for a line with figures in it.
  let { id, title, summary = '', controls = null, children } = $props();

  // A panel keeps its id for life: what was remembered is read once.
  let open = $state(untrack(() => recall(`open.${id}`, true)));

  function toggle() {
    open = !open;
    remember(`open.${id}`, open);
  }
</script>

<section class="section panel" aria-labelledby={`${id}-title`}>
  <div class="head">
    <h2 id={`${id}-title`}>
      <button type="button" class="toggle" aria-expanded={open} aria-controls={`${id}-body`} onclick={toggle}>
        <span class="turn" class:shut={!open}><Icon name="chevron-down" size={16} /></span>{title}
      </button>
    </h2>
    {#if typeof summary === 'string'}
      {#if summary}<p class="summary ellipsis" title={summary}>{summary}</p>{/if}
    {:else}
      <p class="summary ellipsis">{@render summary()}</p>
    {/if}
    {#if controls}<div class="controls">{@render controls()}</div>{/if}
  </div>
  <!-- A shut body is out of reach of the keyboard and of screen readers, not only out of sight. -->
  <div class="fold" class:open id={`${id}-body`} inert={!open}>
    <div><div class="body">{@render children()}</div></div>
  </div>
</section>

<style>
  .panel { padding: 6px 8px 8px; }
  .head { position: relative; display: flex; align-items: center; gap: 8px 12px; min-width: 0; padding: 6px 10px 6px 6px; border-radius: var(--r); }
  h2 { flex: none; }
  .toggle {
    gap: 10px; min-height: 36px; padding: 6px 8px; border: 0; border-radius: var(--r); background: transparent;
    color: var(--text); font: inherit; letter-spacing: inherit;
  }
  /* The whole head is the switch: the button's own area is stretched over it, under the controls. */
  .toggle::after { content: ''; position: absolute; inset: 0; border-radius: var(--r); }
  .head:has(.toggle:hover) { background: var(--hover); }
  .toggle:hover:not(:disabled) { background: transparent; border: 0; }
  .toggle:active:not(:disabled) { transform: none; }
  .turn.shut { transform: rotate(-90deg); }
  .summary { flex: 1; text-align: right; font-size: var(--t-small); color: var(--muted); }
  .summary :global(b) { font-weight: 600; color: var(--text); }
  /* Beside controls the summary follows the title; alone it closes the line. */
  .head:has(.controls) .summary { text-align: left; font-size: var(--t-body); color: var(--text2); }
  .controls { position: relative; flex: none; max-width: 100%; }
  .body { padding: 4px 14px 12px; }
  @media (max-width: 720px) {
    .panel { padding: 4px; }
    .head { flex-wrap: wrap; padding-right: 8px; }
    /* A summary too long for the title's line takes the next one, whole. */
    .summary, .head:has(.controls) .summary { flex: 1 0 auto; max-width: 100%; padding-left: 8px; text-align: right; }
    .body { padding: 4px 10px 12px; }
  }
</style>
