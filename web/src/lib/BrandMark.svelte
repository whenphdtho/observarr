<script>
  // BrandMark is the brand mark: round glasses on a dark tile, a violet and a blue rim joined by a white bridge,
  // a cyan glint on each lens. It stays on its dark tile whatever is behind it. size is the tile's side in px.
  let { size = 26 } = $props();
</script>

<span class="brand-mark" aria-hidden="true" style:width={`${size}px`} style:height={`${size}px`}
  style:border-radius={`${(size * 0.24).toFixed(1)}px`} style:padding={`${(size * 0.12).toFixed(1)}px`}>
  <svg viewBox="0 0 100 100">
    <circle class="rim-a" cx="28" cy="52" r="19" />
    <circle class="rim-b" cx="72" cy="52" r="19" />
    <path class="bridge" d="M45 48Q50 42 55 48" />
    <path class="glint" d="M17.7 48.2A11 11 0 0 1 24.2 41.7" />
    <path class="glint" d="M61.7 48.2A11 11 0 0 1 68.2 41.7" />
  </svg>
</span>

<style>
  /* A hairline inside the tile keeps its edge on the dark rails. */
  .brand-mark {
    display: block; flex: none; box-sizing: border-box; background: var(--brand-tile);
    box-shadow: inset 0 0 0 1px var(--brand-edge);
  }
  svg { display: block; width: 100%; height: 100%; fill: none; stroke-linecap: round; }
  .rim-a, .rim-b { stroke-width: 9; }
  .rim-a { stroke: var(--brand-a); }
  .rim-b { stroke: var(--brand-b); }
  .bridge { stroke: var(--brand-ink); stroke-width: 7; }
  .glint { stroke: var(--brand-glint); stroke-width: 5; }
</style>
