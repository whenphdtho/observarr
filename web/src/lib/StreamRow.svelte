<script>
  import { formatPercent } from './format.js';
  import { METHODS, formatBitrate, codecText } from './streams.js';

  // StreamRow is one stream of a "streams" reading: its title and play method, its progress, then who plays it where
  // and at what bitrate. detail adds the codecs and, for a stream the server converts, why. where, if given, names
  // the service it plays from (the Overview lists several).
  let { stream, detail = false, where = '' } = $props();

  const method = $derived(METHODS[stream.method] ?? '');
  const pct = $derived(formatPercent(stream.progress));
  const device = $derived([stream.client, stream.device].filter(Boolean).join(' · '));
  const bitrate = $derived(formatBitrate(stream.bitrate));
  const codecs = $derived(codecText(stream));
  const why = $derived(stream.reasons.join(', '));
</script>

<div class="stream" class:paused={stream.paused}>
  <div class="head">
    <span class="title ellipsis" title={stream.title}>{stream.title}</span>
    {#if method}<span class="chip {stream.method}" title={why ? `${method}: ${why}` : method}>{method}</span>{/if}
  </div>
  <div class="bar" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow={Math.round(stream.progress * 100)}
    aria-label={`${stream.title}: ${pct} played${stream.paused ? ', paused' : ''}`}>
    <i style:width={pct}></i>
  </div>
  <!-- The short facts keep their width; where it plays gives way first. -->
  <div class="facts">
    {#if stream.user}<span class="keep" title={stream.user}>{stream.user}</span>{/if}
    {#if where}<span title={where}>{where}</span>{/if}
    {#if device}<span title={device}>{device}</span>{/if}
    <span class="keep state">{stream.paused ? `Paused at ${pct}` : pct}</span>
    {#if bitrate}<span class="keep" title={`${bitrate} sent to the client`}>{bitrate}</span>{/if}
  </div>
  {#if detail && codecs}
    <div class="facts"><span title={`Codecs: ${codecs}`}>{codecs}</span></div>
  {/if}
  {#if detail && why}
    <p class="why">Why: {why}</p>
  {/if}
</div>

<style>
  .stream { display: grid; gap: 6px; min-width: 0; }
  .head { display: flex; align-items: center; gap: 10px; min-width: 0; }
  .title { flex: 1; font-weight: 500; font-size: var(--t-body); }
  .chip {
    flex: none; padding: 1px 8px; border-radius: var(--r-pill); border: 1px solid var(--border2);
    font-size: var(--t-micro); font-weight: 500; line-height: 1.6; color: var(--text2); white-space: nowrap;
  }
  /* A transcode is what costs the server: its chip carries the accent, the direct methods stay quiet. */
  .chip.transcode { color: var(--accent-text); border-color: var(--accent); background: var(--accent-wash); }
  .facts > .keep { flex: none; }
  .why { font-size: var(--t-small); color: var(--muted); overflow-wrap: anywhere; }
  /* A paused stream: its bar fades, its state is said in words. */
  .paused .bar > i { opacity: 0.45; }
  .paused .state { color: var(--text2); }
</style>
