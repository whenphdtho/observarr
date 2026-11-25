<script>
  // Donut shows the shares of a whole as a ring: one segment per item ({ label, value, color }), the whole
  // in its centre (total, with caption under it), and a key with ring markers, each share in percent and
  // format(value). Pointing at a segment shows that item in the centre instead. Items of no value are left out.
  let { items, total, caption, format, label, size = 168 } = $props();

  const STROKE = 18;
  const GAP = 2; // px of page between neighbouring segments
  const r = $derived((size - STROKE) / 2);
  const c = $derived(2 * Math.PI * r);
  const shown = $derived(items.filter((i) => i.value > 0));
  const sum = $derived(shown.reduce((n, i) => n + i.value, 0));
  const segs = $derived.by(() => {
    let at = 0;
    return shown.map((i) => {
      const len = (i.value / sum) * c;
      const s = { ...i, share: i.value / sum, dash: `${Math.max(0.6, len - (shown.length > 1 ? GAP : 0)).toFixed(2)} ${c.toFixed(2)}`, offset: (-at).toFixed(2) };
      at += len;
      return s;
    });
  });
  let hover = $state(-1);
  const focus = $derived(segs[hover] ?? null);
  const pct = (share) => (share < 0.01 ? '<1%' : `${Math.round(share * 100)}%`);
</script>

<div class="donut">
  <div class="ring-wrap" style:width={`${size}px`} style:height={`${size}px`}>
    <svg width={size} height={size} viewBox={`0 0 ${size} ${size}`} role="img" aria-label={label}>
      <g transform={`rotate(-90 ${size / 2} ${size / 2})`}>
        <circle class="track" cx={size / 2} cy={size / 2} {r} stroke-width={STROKE} />
        {#each segs as s, k (s.label)}
          <circle class="seg" class:dim={hover >= 0 && hover !== k} cx={size / 2} cy={size / 2} {r} stroke-width={STROKE}
            style:stroke={s.color} stroke-dasharray={s.dash} stroke-dashoffset={s.offset}
            role="presentation" onpointerenter={() => { hover = k; }} onpointerleave={() => { hover = -1; }} />
        {/each}
      </g>
    </svg>
    <span class="centre" aria-hidden="true">
      {#if focus}
        <b class="num">{pct(focus.share)}</b>
        <span class="ellipsis">{focus.label}</span>
      {:else}
        <b class="num">{total}</b>
        <span class="ellipsis">{caption}</span>
      {/if}
    </span>
  </div>
  <ul class="key" aria-label="Shares">
    {#each segs as s, k (s.label)}
      <li class:on={hover === k}>
        <i class="ring" style:color={s.color}></i>
        <span class="ellipsis name" title={s.label}>{s.label}</span>
        <span class="num pct">{pct(s.share)}</span>
        <span class="num muted amt">{format(s.value)}</span>
      </li>
    {/each}
  </ul>
</div>

<style>
  .donut { display: flex; align-items: center; gap: 20px 28px; flex-wrap: wrap; min-width: 0; }
  .ring-wrap { position: relative; flex: none; }
  svg { display: block; }
  .track { fill: none; stroke: var(--surface2); }
  .seg { fill: none; transition: opacity 140ms var(--ease); }
  .seg.dim { opacity: 0.35; }
  .centre {
    position: absolute; inset: 0; display: grid; place-content: center; justify-items: center; text-align: center;
    padding: 0 30px; pointer-events: none; min-width: 0;
  }
  .centre b { font-size: var(--t-figure); font-weight: 600; letter-spacing: var(--track-figure); line-height: 1.2; white-space: nowrap; }
  .centre span { font-size: var(--t-small); color: var(--muted); max-width: 100%; }
  .key { list-style: none; margin: 0; padding: 0; display: grid; gap: 7px; flex: 1 1 180px; min-width: 0; }
  .key li {
    display: grid; grid-template-columns: auto minmax(0, 1fr) auto auto; align-items: center; gap: 8px;
    font-size: var(--t-body); min-width: 0; border-radius: var(--r-sm);
  }
  .key li.on .name { color: var(--text); text-decoration: underline; text-underline-offset: 3px; }
  .name { color: var(--text2); }
  .pct { font-weight: 600; text-align: right; }
  .amt { font-size: var(--t-small); min-width: 7ch; text-align: right; white-space: nowrap; }
</style>
