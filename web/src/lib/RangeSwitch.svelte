<script>
  import { RANGES } from './history.js';

  // RangeSwitch picks the time range of a chart over time: a segmented control whose current option is a
  // filled pill. value is the range id (bindable); ranges limits the options to these ids.
  let { value = $bindable(), ranges = RANGES.map((r) => r.id), label = 'Time range' } = $props();
  const options = $derived(RANGES.filter((r) => ranges.includes(r.id)));
</script>

<div class="seg" role="group" aria-label={label}>
  {#each options as r (r.id)}
    <button type="button" aria-pressed={value === r.id} title={r.name} onclick={() => { value = r.id; }}>{r.label}</button>
  {/each}
</div>
