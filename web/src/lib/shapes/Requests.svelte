<script>
  import { now } from '../ticker.js';
  import { formatAge } from '../format.js';

  // Requests renders one shape.Requests reading: the requests by state and the newest ones. It knows nothing about
  // which app produced it; data.app names it when the adapter serves several apps.
  let { reading } = $props();

  const data = $derived(reading?.data);
  const counts = $derived(data?.counts);
  const latest = $derived(data?.latest ?? []);
  const age = $derived(reading ? formatAge(reading.at, $now) : '');

  // The tone of each state's chip; a state not listed here is neutral.
  const tones = {
    pending: 'warn', failed: 'fail', declined: 'quiet',
    approved: 'accent', processing: 'accent', 'partially available': 'ok', available: 'ok', completed: 'ok',
  };
  const sentence = (s) => (s ? s[0].toUpperCase() + s.slice(1) : s);

  // facts are the summary's parts: the states worth a look first, then the open issues.
  const facts = $derived.by(() => {
    if (!counts) return [];
    const out = [];
    if (counts.pending > 0) out.push({ text: `${counts.pending} waiting`, tone: 'warn-text' });
    if (counts.failed > 0) out.push({ text: `${counts.failed} failed`, tone: 'fail-text' });
    if (counts.processing > 0) out.push({ text: `${counts.processing} processing`, tone: '' });
    out.push({ text: `${counts.available} available`, tone: '' });
    if (data.openIssues > 0) out.push({ text: `${data.openIssues} open issue${data.openIssues === 1 ? '' : 's'}`, tone: '' });
    return out;
  });
  const summary = $derived(facts.map((f) => f.text).join(' · '));
</script>

<div class="requests">
  {#if !reading}
    <p class="waiting">Waiting for the first check…</p>
  {:else}
    <p class="row">
      <span class="label">Requests</span>
      {#if counts}<span class="count num" title={`${counts.total} requests in all`}>{counts.total}</span>{/if}
      {#if data?.app}<span class="app muted ellipsis" title={data.app}>· {data.app}</span>{/if}
      <span class="muted age">{age}</span>
    </p>
    {#if reading.error}
      <p class="error" role="alert">{reading.error}</p>
    {:else if data}
      {#if counts.total > 0}
        <p class="summary ellipsis" title={summary}>
          {#each facts as f, i}{#if i > 0}<span class="sep">{' · '}</span>{/if}<span class={f.tone}>{f.text}</span>{/each}
        </p>
      {/if}
      {#if latest.length === 0}
        <p class="waiting">No requests yet. They show here as soon as someone asks for a movie or a series.</p>
      {:else}
        <ul class="items" aria-label="Newest requests">
          {#each latest as r}
            {@const meta = [sentence(r.type), r.requestedBy && `by ${r.requestedBy}`, formatAge(r.at, $now)].filter(Boolean).join(' · ')}
            <li>
              <div class="head">
                <span class="title ellipsis" title={r.title}>{r.title}</span>
                <span class="chip {tones[r.state] ?? ''}">{sentence(r.state)}</span>
              </div>
              <p class="meta muted ellipsis" title={meta}>{meta}</p>
            </li>
          {/each}
        </ul>
      {/if}
    {/if}
  {/if}
</div>

<style>
  .requests { display: grid; gap: 6px; }
  .row { display: flex; align-items: baseline; gap: 8px; min-width: 0; padding-top: 12px; border-top: 1px solid var(--border); }
  .label { font-size: var(--t-body); font-weight: 600; flex: none; }
  .count { font-size: var(--t-small); color: var(--muted); flex: none; }
  .app { font-size: var(--t-small); flex: 0 1 auto; }
  .age { margin-left: auto; font-size: var(--t-small); white-space: nowrap; flex: none; }
  .error { font-size: var(--t-body); }
  .summary { font-size: var(--t-body); color: var(--text2); }
  .sep { color: var(--muted); }
  .items { list-style: none; margin: 0; padding: 0; display: grid; }
  .items li { display: grid; gap: 2px; min-width: 0; padding: 9px 0; border-bottom: 1px solid var(--border); }
  .items li:last-child { border-bottom: 0; padding-bottom: 0; }
  .head { display: flex; align-items: center; gap: 12px; min-width: 0; }
  .title { flex: 1; font-weight: 500; font-size: var(--t-body); }
  .meta { font-size: var(--t-small); }
  .chip {
    flex: none; padding: 1px 8px; border-radius: var(--r-pill); border: 1px solid var(--border2);
    font-size: var(--t-small); line-height: 18px; white-space: nowrap; color: var(--text2);
  }
  .chip.warn { color: var(--warn); border-color: var(--warn-edge); background: var(--warn-wash); }
  .chip.fail { color: var(--fail); border-color: var(--fail-edge); background: var(--fail-wash); }
  .chip.ok { color: var(--ok); }
  .chip.accent { color: var(--accent-text); }
  .chip.quiet { color: var(--muted); border-color: var(--border); }
</style>
