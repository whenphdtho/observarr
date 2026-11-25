<script>
  import StreamRow from './StreamRow.svelte';
  import Icon from './icons/Icon.svelte';
  import { streamServices, streamsOf, streamSummary, streamText, formatBitrate } from './streams.js';

  // NowPlaying is the Overview's line on what media servers play: a summary and the first streams of every service
  // that fills the "streams" shape. The service cards show them all. onjump(id) shows a service's card.
  let { services, onjump } = $props();

  // A few rows are enough here; the rest are a click away.
  const MAX_ROWS = 6;

  const players = $derived(streamServices(services));
  const sum = $derived(streamSummary(players));
  const rows = $derived(players.flatMap((svc) => (streamsOf(svc)?.streams ?? []).map((stream) => ({ svc, stream }))));
  const shown = $derived(rows.slice(0, MAX_ROWS));
  const meta = $derived([streamText(sum), formatBitrate(sum.bitrate)].filter(Boolean).join(' · '));
  const named = $derived(players.length > 1);
  // The rest of the streams: those beyond MAX_ROWS, and those a reading counts but does not list.
  const more = $derived(sum.total - shown.length);
</script>

{#if players.length > 0}
  <section class="section now" aria-labelledby="now-title">
    <div class="card-head">
      <h2 id="now-title">Now playing</h2>
      {#if meta}<span class="meta" title={meta}>{meta}</span>{/if}
    </div>
    {#if sum.readings === 0}
      <p class="waiting">Waiting for the first check…</p>
    {:else if rows.length === 0}
      <p class="waiting">Nothing is playing.</p>
    {:else}
      <ul class="rows">
        {#each shown as { svc, stream }, i (i)}
          <li><StreamRow {stream} where={named ? svc.name : ''} /></li>
        {/each}
      </ul>
      {#if more > 0}
        <a class="go more" href="#/apps" onclick={players.length === 1 ? (e) => { e.preventDefault(); onjump(players[0].id); } : undefined}>
          {more} more in Apps<Icon name="chevron-right" size={14} />
        </a>
      {/if}
    {/if}
  </section>
{/if}

<style>
  .now .card-head { margin-bottom: 12px; }
  /* The heading keeps its line; the summary beside it gives way. */
  .now h2 { flex: none; white-space: nowrap; }
  .rows {
    list-style: none; margin: 0; padding: 0; display: grid; gap: 14px 28px;
    grid-template-columns: repeat(auto-fill, minmax(min(100%, 280px), 1fr));
  }
  .rows li { min-width: 0; }
  .more { margin-top: 12px; }
</style>
