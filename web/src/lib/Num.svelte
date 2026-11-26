<script>
  import { Tween } from 'svelte/motion';
  import { dur, ease, ms } from './motion.js';

  // Num shows a live number that glides to each new reading instead of jumping, in tabular figures so the width
  // stays put. format turns the current (in-between) value into text; a value that is not a finite number is
  // shown as it is.
  let { value, format = (v) => String(Math.round(v)) } = $props();

  const finite = $derived(typeof value === 'number' && Number.isFinite(value));
  const tween = new Tween(0, { easing: ease('out') });
  let started = false;

  $effect(() => {
    if (!finite) return;
    // The first reading is shown as it is: only changes glide.
    tween.set(value, { duration: started ? ms(dur('slow')) : 0 });
    started = true;
  });
</script>

{#if finite}{format(tween.current)}{:else}{value}{/if}
