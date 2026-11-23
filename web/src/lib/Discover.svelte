<script>
  import { tick } from 'svelte';
  import { api } from './api.js';
  import AddService from './AddService.svelte';

  // Discover lets the user run discovery sources and add what they find, one click at a time.
  // It never names an app type: display names come from `adapters` (GET /api/adapters), passed
  // in by the caller, looked up by the suggestion's `type`. `containers` is forwarded to the
  // Add dialog for its container select.
  let { open = $bindable(false), adapters = [], containers = [] } = $props();

  let dialogEl;
  let resultsHeading;
  let opener = null;
  let wasOpen = false;

  let loading = $state(false);
  let loadError = $state('');
  let sources = $state({ network: false, linked: false, config: false, docker: false });
  let suggestions = $state([]);

  let running = $state(null); // 'network' | 'linked' | 'config' | 'docker' | null
  let actionError = $state('');
  let resultErrors = $state([]);

  let targetsText = $state('');

  let addOpen = $state(false);
  let addSuggestion = $state(null);

  function appName(type) {
    return adapters.find((a) => a.type === type)?.name ?? type;
  }

  // missingSources names whichever of Docker and config-folder discovery the operator hasn't set
  // up (each needs extra deployment steps — see docs/deploy.md), so the dialog can say so instead
  // of just leaving them out with no explanation.
  const missingSources = $derived.by(() => {
    const names = [];
    if (!sources.docker) names.push('Docker');
    if (!sources.config) names.push('config-folder');
    return names;
  });

  async function loadState() {
    loading = true;
    loadError = '';
    try {
      const res = await api('GET', '/api/discovery');
      sources = res.sources;
      suggestions = res.suggestions;
    } catch (err) {
      loadError = err.message;
    } finally {
      loading = false;
    }
  }

  // runSource runs one source's endpoint, then reloads the current suggestion list (the server
  // may have replaced an earlier find for the same app with a fresh one, which needs a new id).
  async function runSource(name, path, body) {
    if (running) return;
    running = name;
    actionError = '';
    resultErrors = [];
    try {
      const res = await api('POST', path, body);
      resultErrors = res.errors ?? [];
      await loadState();
    } catch (err) {
      actionError = err.status === 409
        ? 'A discovery run is already in progress — try again shortly.'
        : err.message;
    } finally {
      running = null;
    }
  }

  function scanNetwork() {
    runSource('network', '/api/discovery/network', { targets: targetsText });
  }

  function findLinked() {
    runSource('linked', '/api/discovery/linked', {});
  }

  function readConfig() {
    runSource('config', '/api/discovery/config', {});
  }

  function findDocker() {
    runSource('docker', '/api/discovery/docker', {});
  }

  // dismiss removes one suggestion and moves focus to the next row's Add button (or, with no
  // next row, to the Results heading), so keyboard users keep their place.
  async function dismiss(id) {
    actionError = '';
    const index = suggestions.findIndex((s) => s.id === id);
    try {
      await api('POST', `/api/discovery/${id}/dismiss`, {});
      suggestions = suggestions.filter((s) => s.id !== id);
    } catch (err) {
      if (err.status !== 410) {
        actionError = err.message;
        return;
      }
      await loadState(); // expired or already added: show what the server still offers
    }
    await tick();
    const next = index >= 0 ? suggestions[index] : undefined; // the row that took its place
    const button = next && dialogEl.querySelector(`[data-add="${CSS.escape(next.id)}"]`);
    (button ?? resultsHeading)?.focus();
  }

  function openAdd(sug) {
    addSuggestion = sug;
    addOpen = true;
  }

  // handleAdded runs once AddService has added a suggestion: both dialogs close (AddService
  // closes itself already) and the suggestion is gone from the list for good.
  function handleAdded(id) {
    suggestions = suggestions.filter((s) => s.id !== id);
    open = false;
  }

  function handleDialogClose() {
    open = false;
    opener?.focus();
    opener = null;
  }

  $effect(() => {
    if (!dialogEl) return;
    if (open) {
      if (!wasOpen) {
        opener = document.activeElement;
        actionError = '';
        loadState();
      }
      if (!dialogEl.open) dialogEl.showModal();
    } else if (dialogEl.open) {
      dialogEl.close();
    }
    wasOpen = open;
  });
</script>

<dialog bind:this={dialogEl} aria-labelledby="discoverTitle" onclose={handleDialogClose}>
  <h2 id="discoverTitle">Discover services</h2>

  <section class="source">
    <h3>Network</h3>
    <p class="muted field-hint">Scans the hosts or subnets you list below for apps it can reach and identify.</p>
    <label class="field">
      Hosts or private subnets
      <textarea bind:value={targetsText} rows="2" disabled={running !== null}
        placeholder="e.g. 192.168.1.0/24, nas.local"></textarea>
    </label>
    <p class="muted field-hint">This can take up to two minutes.</p>
    <div class="source-actions">
      <button type="button" disabled={running !== null} onclick={scanNetwork}>Scan</button>
      {#if running === 'network'}
        <span class="spinner" role="status"><span class="visually-hidden">Scanning…</span></span>
      {/if}
    </div>
  </section>

  {#if sources.linked}
    <section class="source">
      <h3>Linked apps</h3>
      <div class="source-actions">
        <button type="button" disabled={running !== null} onclick={findLinked}>Find</button>
        {#if running === 'linked'}
          <span class="spinner" role="status"><span class="visually-hidden">Finding…</span></span>
        {/if}
      </div>
    </section>
  {/if}

  {#if sources.config}
    <section class="source">
      <h3>Config folders</h3>
      <p class="muted field-hint">Suggests an app by reading its mounted config file, including its API key if found.</p>
      <div class="source-actions">
        <button type="button" disabled={running !== null} onclick={readConfig}>Read</button>
        {#if running === 'config'}
          <span class="spinner" role="status"><span class="visually-hidden">Reading…</span></span>
        {/if}
      </div>
    </section>
  {/if}

  {#if sources.docker}
    <section class="source">
      <h3>Docker</h3>
      <p class="muted field-hint">Suggests an app from a running container; one Observarr can reach is verified.</p>
      <div class="source-actions">
        <button type="button" disabled={running !== null} onclick={findDocker}>Find</button>
        {#if running === 'docker'}
          <span class="spinner" role="status"><span class="visually-hidden">Finding…</span></span>
        {/if}
      </div>
    </section>
  {/if}

  {#if missingSources.length > 0}
    <p class="muted field-hint">
      {missingSources.join(' and ')} discovery need{missingSources.length === 1 ? 's' : ''} extra setup.
    </p>
    <p class="muted field-hint">
      How to set this up:
      <a href="https://github.com/whenphdtho/observarr/blob/main/docs/deploy.md" target="_blank" rel="noopener noreferrer">
        deploy guide
      </a>
    </p>
  {/if}

  {#if actionError}
    <p class="error" role="alert">{actionError}</p>
  {/if}

  <section class="results">
    <h3 bind:this={resultsHeading} tabindex="-1">Results</h3>
    {#if loadError}
      <p class="error" role="alert">{loadError}</p>
    {:else if loading}
      <p class="muted" role="status">Loading…</p>
    {:else if suggestions.length === 0}
      <p class="muted">Nothing new found.</p>
    {:else}
      <ul class="suggestions">
        {#each suggestions as sug (sug.id)}
          <li class="suggestion">
            <div class="info">
              <span class="ellipsis" title={appName(sug.type)}>{appName(sug.type)}</span>
              <span class="muted ellipsis" title={sug.hostKnown ? sug.url : 'host needed'}>
                {sug.hostKnown ? sug.url : 'host needed'}
              </span>
              {#if sug.container}
                <span class="muted ellipsis" title={`Docker container ${sug.container}`}>
                  container {sug.container}
                </span>
              {/if}
              {#if sug.version}
                <span class="muted ellipsis" title={`version ${sug.version}`}>version {sug.version}</span>
              {/if}
              {#if sug.keyFound}
                <span class="muted ellipsis" title={`API key found in ${sug.keySource}`}>
                  API key found in {sug.keySource}
                </span>
              {/if}
              {#if !sug.verified}
                <span class="unverified">Not verified — {sug.reason}</span>
              {/if}
            </div>
            <div class="actions">
              <button type="button" data-add={sug.id} onclick={() => openAdd(sug)}>Add</button>
              <button type="button" class="quiet" onclick={() => dismiss(sug.id)}>Dismiss</button>
            </div>
          </li>
        {/each}
      </ul>
    {/if}

    {#if resultErrors.length > 0}
      <ul class="source-errors">
        {#each resultErrors as e, i (i)}
          <li class="muted">{e.target}: {e.message}</li>
        {/each}
      </ul>
    {/if}
  </section>

  <div class="actions">
    <button type="button" class="quiet" onclick={() => { open = false; }}>Close</button>
  </div>
</dialog>

<AddService bind:open={addOpen} mode="add" suggestion={addSuggestion} {adapters} {containers}
  onadded={handleAdded} ongone={loadState} />

<style>
  dialog { width: min(560px, calc(100vw - 32px)); }
  h2 { margin-bottom: 16px; }
  h3 { margin: 0 0 8px; font-size: 13.5px; font-weight: 600; }
  .source { margin-bottom: 16px; padding-bottom: 16px; border-bottom: 1px solid var(--border); }
  .field { margin-bottom: 8px; }
  .field-hint { margin: 0 0 8px; font-size: 13px; }
  textarea { resize: vertical; min-height: 44px; }
  .source-actions { display: flex; align-items: center; gap: 10px; }
  .spinner {
    width: 14px; height: 14px; border-radius: 50%; flex-shrink: 0;
    border: 2px solid var(--field-border); border-top-color: var(--accent);
    animation: spin 0.8s linear infinite;
  }
  @keyframes spin { to { transform: rotate(360deg); } }
  .results { margin-top: 18px; }
  .suggestions, .source-errors { list-style: none; margin: 0; padding: 0; display: grid; gap: 10px; }
  .source-errors { margin-top: 10px; font-size: 13px; overflow-wrap: anywhere; }
  .suggestion {
    display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 10px; min-width: 0;
    padding: 12px 14px; border: 1px solid var(--border); border-radius: var(--r-md); background: var(--surface);
  }
  .info { display: grid; gap: 2px; min-width: 0; flex: 1 1 220px; font-size: 13px; }
  .info > :first-child { font-weight: 600; font-size: 13.5px; }
  .unverified { color: var(--warn); overflow-wrap: anywhere; }
  p { font-size: 13px; }
  dialog > .actions { margin-top: 18px; }
</style>
