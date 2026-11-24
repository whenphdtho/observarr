<script>
  // Skeleton stands in for content that is on its way: quiet blocks the shape of what will come, with the words
  // for screen readers. It does not move, so a slow start costs nothing. kind is 'lines', 'rows' (an icon and two
  // lines each) or 'chart'.
  let { label = 'Loading…', kind = 'lines', count = 3, height = 190 } = $props();
  const widths = ['72%', '56%', '64%', '48%', '60%'];
</script>

<div class="skeleton {kind}" role="status" aria-busy="true" style:--h={`${height}px`}>
  <span class="visually-hidden">{label}</span>
  {#if kind === 'chart'}
    <span class="bone figure"></span>
    <span class="bone plot"></span>
  {:else if kind === 'rows'}
    {#each { length: count } as _, i (i)}
      <span class="row"><span class="bone sq"></span><span class="pair">
        <span class="bone" style:width={widths[i % widths.length]}></span>
        <span class="bone thin" style:width="40%"></span>
      </span></span>
    {/each}
  {:else}
    {#each { length: count } as _, i (i)}
      <span class="bone" style:width={widths[i % widths.length]}></span>
    {/each}
  {/if}
</div>

<style>
  .skeleton { display: grid; gap: 10px; min-width: 0; }
  .bone { display: block; height: 12px; border-radius: 4px; background: var(--surface2); }
  .thin { height: 9px; }
  .figure { width: 160px; height: 28px; }
  .plot { height: calc(var(--h) - 40px); border-radius: var(--r); background: var(--surface); }
  .row { display: flex; align-items: center; gap: 12px; }
  .sq { width: 32px; height: 32px; border-radius: var(--r); flex: none; }
  .pair { display: grid; gap: 6px; flex: 1; min-width: 0; }
</style>
