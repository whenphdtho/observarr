<script>
  import { formatChange } from './format.js';

  // Delta is the change of a figure against an earlier period, as a plain signed percentage ("+12%", "−4%") in
  // the text colour: whether more is good depends on the reader, so it is not coloured. against names the earlier
  // period ("the 24 hours before"). A change under half a percent counts as none; one that is not known shows
  // nothing.
  let { ratio, against = '' } = $props();

  const text = $derived(formatChange(ratio));
  const words = $derived(text === '0%' ? `no change against ${against}` : `${ratio > 0 ? 'up' : 'down'} ${text.slice(1)} against ${against}`);
</script>

{#if Number.isFinite(ratio)}
  <span class="delta num" title={words}>
    <span aria-hidden="true">{text}</span>
    <span class="visually-hidden">{words}</span>
  </span>
{/if}

<style>
  .delta { font-size: var(--t-small); font-weight: 600; white-space: nowrap; line-height: 1; color: var(--text2); }
</style>
