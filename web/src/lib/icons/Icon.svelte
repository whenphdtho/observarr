<script>
  import { icons } from './icons.js';

  // Icon draws one icon from icons.js, falling back to a generic box for a name it does not know
  // (an adapter's icon hint comes from the server). Always decorative: the control or text next
  // to it carries the meaning.
  let { name, size = 18 } = $props();

  const shapes = $derived(icons[name] ?? icons.box);
</script>

<svg class="icon" width={size} height={size} viewBox="0 0 24 24" aria-hidden="true" focusable="false">
  {#each shapes as [tag, attrs], i (i)}
    <svelte:element this={tag} xmlns="http://www.w3.org/2000/svg" {...attrs} />
  {/each}
</svg>

<style>
  .icon {
    flex: none; fill: none; stroke: currentColor; stroke-width: 1.75;
    stroke-linecap: round; stroke-linejoin: round;
  }
</style>
