<script>
  // Delta is the change of a figure against an earlier period: a small triangle up or down and the percentage,
  // green when the change is good and red when it is bad. good says which way is good: 'up' (more traffic,
  // more sign-ins) or 'down' (CPU, memory). against names the earlier period ("the 24 hours before"). A change
  // under half a percent counts as none.
  let { ratio, good = 'up', against = '' } = $props();

  const pct = $derived(Math.abs(ratio) * 100);
  const dir = $derived(pct < 0.5 ? 'flat' : ratio > 0 ? 'up' : 'down');
  const tone = $derived(dir === 'flat' ? 'flat' : dir === good ? 'good' : 'bad');
  const text = $derived(pct < 0.5 ? '0%' : pct > 999 ? '>999%' : `${pct < 10 ? pct.toFixed(1) : Math.round(pct)}%`);
  const words = $derived(dir === 'flat' ? `no change against ${against}` : `${dir} ${text} against ${against}`);
</script>

<span class="delta {tone}" title={words}>
  {#if dir !== 'flat'}
    <svg viewBox="0 0 8 8" width="8" height="8" aria-hidden="true"><path d={dir === 'up' ? 'M4 1L7.5 7H.5z' : 'M4 7L.5 1h7z'} /></svg>
  {/if}
  <span aria-hidden="true">{text}</span>
  <span class="visually-hidden">{words}</span>
</span>

<style>
  .delta {
    display: inline-flex; align-items: center; gap: 4px; font-size: var(--t-small); font-weight: 600; white-space: nowrap;
    font-variant-numeric: tabular-nums; line-height: 1;
  }
  svg { fill: currentColor; flex: none; }
  .good { color: var(--ok); }
  .bad { color: var(--fail); }
  .flat { color: var(--muted); }
</style>
