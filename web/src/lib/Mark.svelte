<script>
  // Mark is the brand mark: an eye on the accent, its pupil a live reading. size is the square's side in px.
  let { size = 26 } = $props();
  const glyph = $derived(Math.round(size * 0.7));
</script>

<span class="mark" aria-hidden="true" style:width={`${size}px`} style:height={`${size}px`}
  style:border-radius={`${Math.max(5, Math.round(size * 0.27))}px`}>
  <svg viewBox="0 0 24 24" width={glyph} height={glyph}>
    <path d="M2.8 12c2.5-4.3 5.6-6.4 9.2-6.4s6.7 2.1 9.2 6.4c-2.5 4.3-5.6 6.4-9.2 6.4S5.3 16.3 2.8 12z" />
    <circle cx="12" cy="12" r="3" />
    <circle class="glint" cx="13.3" cy="10.7" r="0.9" />
  </svg>
</span>

<style>
  .mark { display: grid; place-items: center; flex: none; background: var(--accent); }
  svg { fill: none; stroke: var(--accent-ink); stroke-width: 2.1; stroke-linejoin: round; }
  circle { fill: var(--accent-ink); stroke: none; }
  .glint { fill: var(--accent); }
</style>
