<script>
  import { now } from '../ticker.js';
  import { formatAge, formatBytes, formatPercent } from '../format.js';

  // Queue renders one shape.Queue reading. It knows nothing about which app produced it — every
  // adapter that declares the "queue" kind fills the same shape.
  let { reading } = $props();

  const data = $derived(reading?.data);
  const items = $derived(data?.items ?? []);
  const age = $derived(reading ? formatAge(reading.at, $now) : '');
</script>

<div class="queue">
  {#if !reading}
    <p class="muted">Waiting for the first check…</p>
  {:else}
    <p class="row">
      <span class="muted">Queue</span>
      {#if data}<span class="count">{data.total}</span>{/if}
      <span class="muted age">{age}</span>
    </p>
    {#if reading.error}
      <p class="error" role="alert">{reading.error}</p>
    {:else if items.length === 0}
      <p class="muted">Queue is empty.</p>
    {:else}
      <ul class="items">
        {#each items as item}
          <li>
            <div class="title ellipsis" title={item.title}>{item.title}</div>
            <div class="progress" role="progressbar" aria-valuemin="0" aria-valuemax="100"
              aria-valuenow={Math.round(item.progress * 100)}
              aria-label={`${item.title}: ${formatPercent(item.progress)} done`}>
              <div class="fill" style={`width: ${formatPercent(item.progress)}`}></div>
            </div>
            <div class="meta">
              <span class="status ellipsis" title={item.status}>{item.status}</span>
              <span class="muted ellipsis" title={`${formatBytes(item.leftBytes)} left of ${formatBytes(item.sizeBytes)}`}>
                {formatBytes(item.leftBytes)} left
              </span>
              {#if item.eta}<span class="muted ellipsis" title={item.eta}>{item.eta}</span>{/if}
            </div>
            {#if item.problem}
              <div class="problem">{item.problem}</div>
            {/if}
          </li>
        {/each}
      </ul>
    {/if}
  {/if}
</div>

<style>
  .queue { display: grid; gap: 6px; }
  .row { display: flex; align-items: center; gap: 8px; margin: 0; min-width: 0; }
  .count { font-weight: 600; }
  .age { margin-left: auto; }
  .ellipsis { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0; }
  .items { list-style: none; margin: 0; padding: 0; display: grid; gap: 10px; }
  .items li { border-top: 1px solid var(--border); padding-top: 8px; display: grid; gap: 4px; min-width: 0; }
  .items li:first-child { border-top: 0; padding-top: 0; }
  .title { font-weight: 600; }
  .progress {
    /* --field-border, not --border: the track is a UI component and needs the ≥3:1 contrast
       against --surface that --border (meant for quiet dividers) does not reliably clear. */
    height: 6px; border-radius: 4px; background: var(--field-border); overflow: hidden;
  }
  .fill { height: 100%; background: var(--accent); }
  /* Each piece (status, bytes left, ETA) shrinks and ellipsizes rather than wrapping its own line,
     so the row stays on one line whenever it fits and only truncates the piece(s) that don't. */
  .meta { display: flex; gap: 10px; flex-wrap: nowrap; min-width: 0; }
  .meta > span { max-width: 100%; flex-shrink: 1; }
  .status { text-transform: capitalize; }
  .problem { color: var(--fail); overflow-wrap: anywhere; }

  /* Below ~420px there isn't room for three shrunk, ellipsized pieces side by side without
     truncating all of them; let the row wrap instead so each piece gets its own line, full width
     and untruncated. */
  @media (max-width: 419px) {
    .meta { flex-wrap: wrap; }
    .meta > span.ellipsis { white-space: normal; overflow: visible; text-overflow: clip; }
  }
</style>
