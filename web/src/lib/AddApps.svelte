<script>
  import { onMount } from 'svelte';
  import { api } from './api.js';
  import { adapters, adapterFor, snapshot } from './data.js';
  import { serviceList } from './live.js';
  import FoundList from './FoundList.svelte';
  import SuggestionRow from './SuggestionRow.svelte';
  import ServiceForm from './ServiceForm.svelte';
  import Icon from './icons/Icon.svelte';

  // AddApps is the one place to add apps (#/apps/add): what was found on this server (listed at
  // once, nothing contacted), then — on request — a network scan or the apps a linked app knows,
  // and the manual form. Nothing is added, and no app is contacted, without the user's click.
  let { onback } = $props();

  const containers = $derived($snapshot?.containers ?? []);
  const taken = $derived($serviceList.map((s) => s.name));

  let linkedAvailable = $state(false);
  let elsewhere = $state([]); // suggestions from the network scan and linked apps
  let open = $state({ network: false, linked: false });
  let running = $state(''); // '' | 'network' | 'linked'
  let runError = $state('');
  let runProblems = $state([]);
  let ran = $state({ network: false, linked: false });
  let targets = $state('');
  let manualKey = $state(0);
  let manualNotice = $state('');

  async function loadState() {
    try {
      const res = await api('GET', '/api/discovery');
      linkedAvailable = res.sources.linked;
      elsewhere = res.suggestions.filter((s) => s.source === 'network' || s.source === 'linked');
      if (elsewhere.some((s) => s.source === 'network')) open.network = true;
      if (elsewhere.some((s) => s.source === 'linked')) open.linked = true;
    } catch {
      // The rest of the page still works; a scan reports its own errors.
    }
  }

  async function run(source, path, body) {
    if (running) return;
    running = source;
    runError = '';
    runProblems = [];
    try {
      const res = await api('POST', path, body);
      runProblems = res.errors ?? [];
      ran[source] = true;
      await loadState();
    } catch (err) {
      runError = err.status === 409 ? 'Another search is running — try again shortly.' : err.message;
    } finally {
      running = '';
    }
  }

  function scan(event) {
    event.preventDefault();
    run('network', '/api/discovery/network', { targets });
  }

  function drop(id) {
    elsewhere = elsewhere.filter((s) => s.id !== id);
  }

  function manualDone(name) {
    manualNotice = `${name} added.`;
    manualKey += 1;
  }

  onMount(loadState);
</script>

<section class="add-apps" aria-label="Add apps">
  <div class="toolbar">
    <a class="back" href="#/apps" onclick={(e) => { e.preventDefault(); onback?.(); }}>
      <Icon name="chevron-right" size={15} />Back to apps
    </a>
  </div>

  <section class="card" aria-labelledby="found-title">
    <div class="card-head">
      <h2 id="found-title"><Icon name="server" size={17} />Found on this server</h2>
      <span class="meta">Nothing is contacted until you add it</span>
    </div>
    <FoundList adapters={$adapters} {taken} />
  </section>

  <section class="card" aria-labelledby="elsewhere-title">
    <div class="card-head">
      <h2 id="elsewhere-title"><Icon name="radar" size={17} />Look elsewhere</h2>
    </div>

    <div class="fold">
      <button type="button" class="fold-btn" aria-expanded={open.network}
        aria-controls={open.network ? 'fold-network' : undefined}
        onclick={() => { open.network = !open.network; }}>
        <Icon name="chevron-right" size={15} />
        <span class="fold-text">
          <b>Scan my network</b>
          <span class="muted">Looks for apps on hosts or private subnets you name</span>
        </span>
      </button>
      {#if open.network}
        <div class="fold-body" id="fold-network">
          <form class="scan" onsubmit={scan}>
            <label>
              Hosts or private subnets
              <textarea bind:value={targets} rows="2" disabled={running !== ''}
                placeholder="e.g. 192.168.1.0/24, nas.local"></textarea>
            </label>
            <div class="scan-foot">
              <span class="muted hint">Can take up to two minutes.</span>
              {#if running === 'network'}
                <span class="spinner" role="status"><span class="visually-hidden">Scanning…</span></span>
              {/if}
              <button disabled={running !== '' || targets.trim() === ''}>Scan</button>
            </div>
          </form>
          {@render results('network')}
        </div>
      {/if}
    </div>

    {#if linkedAvailable}
      <div class="fold">
        <button type="button" class="fold-btn" aria-expanded={open.linked}
          aria-controls={open.linked ? 'fold-linked' : undefined}
          onclick={() => { open.linked = !open.linked; }}>
          <Icon name="chevron-right" size={15} />
          <span class="fold-text">
            <b>Linked apps</b>
            <span class="muted">Asks the apps you added which other apps they are connected to</span>
          </span>
        </button>
        {#if open.linked}
          <div class="fold-body" id="fold-linked">
            <div class="scan-foot">
              {#if running === 'linked'}
                <span class="spinner" role="status"><span class="visually-hidden">Finding…</span></span>
              {/if}
              <button type="button" disabled={running !== ''} onclick={() => run('linked', '/api/discovery/linked', {})}>
                Find linked apps
              </button>
            </div>
            {@render results('linked')}
          </div>
        {/if}
      </div>
    {/if}

    {#if runError}<p class="error" role="alert">{runError}</p>{/if}
    {#if runProblems.length > 0}
      <details class="problems">
        <summary>{runProblems.length} address{runProblems.length === 1 ? '' : 'es'} could not be checked</summary>
        <ul>
          {#each runProblems as p, i (i)}<li><b>{p.target}</b>: {p.message}</li>{/each}
        </ul>
      </details>
    {/if}
  </section>

  <section class="card" aria-labelledby="manual-title">
    <div class="card-head">
      <h2 id="manual-title"><Icon name="plus" size={17} />Add manually</h2>
    </div>
    <p class="visually-hidden" role="status">{manualNotice}</p>
    {#key manualKey}
      <ServiceForm adapters={$adapters} {containers} ondone={manualDone} />
    {/key}
    {#if manualNotice}<p class="ok-text" aria-hidden="true">{manualNotice}</p>{/if}
  </section>
</section>

{#snippet results(source)}
  {@const list = elsewhere.filter((s) => s.source === source)}
  {#if list.length > 0}
    <ul class="rows">
      {#each list as sug (sug.id)}
        <SuggestionRow {sug} adapter={adapterFor($adapters, sug.type)} {taken}
          onadded={drop} ondismissed={drop} ongone={loadState} />
      {/each}
    </ul>
  {:else if ran[source] && running === ''}
    <p class="muted none">Nothing new found.</p>
  {/if}
{/snippet}

<style>
  .add-apps { display: grid; gap: 16px; }
  .toolbar { display: flex; align-items: center; }
  .back {
    display: inline-flex; align-items: center; gap: 6px; font-size: 13px; font-weight: 550; color: var(--text2);
    text-decoration: none; padding: 4px 8px 4px 4px; border-radius: 8px;
  }
  .back:hover { color: var(--text); background: var(--surface2); }
  .back :global(.icon) { transform: rotate(180deg); }
  .card-head { flex-wrap: wrap; }

  .fold { border-top: 1px solid var(--border); }
  .card-head + .fold { border-top: 0; }
  .fold-btn {
    width: 100%; justify-content: flex-start; align-items: flex-start; gap: 10px; padding: 12px 4px; text-align: left;
    background: transparent; border: 0; color: var(--text); border-radius: 8px; white-space: normal;
  }
  .fold-btn:hover:not(:disabled) { background: var(--surface2); border: 0; }
  .fold-btn :global(.icon) { margin-top: 2px; color: var(--muted); transition: transform 160ms var(--ease); }
  .fold-btn[aria-expanded='true'] :global(.icon) { transform: rotate(90deg); }
  .fold-text { display: grid; gap: 1px; min-width: 0; font-weight: 400; }
  .fold-text b { font-weight: 600; font-size: 13.5px; }
  .fold-text .muted { font-size: 12.5px; }
  .fold-body { display: grid; gap: 10px; padding: 0 0 14px 29px; min-width: 0; }
  .scan { display: grid; gap: 10px; }
  label { display: grid; gap: 6px; font-size: 13px; color: var(--text2); }
  textarea { resize: vertical; min-height: 44px; }
  .scan-foot { display: flex; align-items: center; gap: 10px; justify-content: flex-end; flex-wrap: wrap; }
  .scan-foot .hint { margin-right: auto; }
  .hint { font-size: 12px; }
  .rows { margin: 0; padding: 0; display: grid; }
  .none { font-size: 13px; }
  .problems { font-size: 12.5px; color: var(--muted); margin-top: 8px; }
  .problems summary { cursor: pointer; width: fit-content; }
  .problems ul { margin: 6px 0 0; padding-left: 18px; display: grid; gap: 2px; overflow-wrap: anywhere; }
  .problems b { color: var(--text2); font-weight: 550; }
  .ok-text { color: var(--ok); font-size: 13px; margin-top: 10px; }
  .spinner {
    width: 14px; height: 14px; border-radius: 50%; flex: none;
    border: 2px solid var(--field-border); border-top-color: var(--accent); animation: spin 0.8s linear infinite;
  }
  @keyframes spin { to { transform: rotate(360deg); } }
  @container (max-width: 560px) {
    .fold-body { padding-left: 0; }
  }
</style>
