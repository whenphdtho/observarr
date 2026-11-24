<script module>
  import { api } from './api.js';

  // One listing at a time, shared by every FoundList on the page: the server answers a second one
  // that starts meanwhile with 409.
  let listing = null;

  function listLocal() {
    listing ??= api('GET', '/api/discovery/local').finally(() => { listing = null; });
    return listing;
  }
</script>

<script>
  import { onMount } from 'svelte';
  import { adapterFor } from './data.js';
  import { ADDING_APPS_DOCS } from './urls.js';
  import SuggestionRow from './SuggestionRow.svelte';

  // FoundList shows the apps found on this server — recognised Docker containers and config
  // folders — as soon as it appears. Listing them contacts no app (GET /api/discovery/local only
  // reads the container list and the folders); each row asks before anything is sent. `taken` are
  // the names already in use; onempty() runs once the last row is gone.
  let { adapters = [], taken = [], onempty } = $props();

  let loading = $state(true);
  let error = $state('');
  let sources = $state({ docker: false, config: false });
  let rows = $state([]);
  let problems = $state([]);
  let notice = $state('');
  let listEl = $state();

  const setUp = $derived(sources.docker || sources.config);

  async function reload() {
    loading = rows.length === 0;
    error = '';
    try {
      const res = await listLocal();
      sources = res.sources;
      rows = res.suggestions;
      problems = res.errors ?? [];
    } catch (err) {
      error = err.status === 409 ? 'Still listing the apps on this server — try again in a moment.' : err.message;
    } finally {
      loading = false;
    }
  }

  // remove drops a row and keeps keyboard users in place: focus moves to the next row's first
  // button, else the previous one's, else the list's status line.
  function remove(id) {
    const index = rows.findIndex((r) => r.id === id);
    rows = rows.filter((r) => r.id !== id);
    queueMicrotask(() => {
      const items = listEl?.querySelectorAll(':scope > li') ?? [];
      const next = items[Math.min(index, items.length - 1)];
      const button = next?.querySelector('button');
      if (button) button.focus();
      else listEl?.parentElement?.querySelector('.found-status')?.focus();
    });
    if (rows.length === 0) onempty?.();
  }

  function added(id, name) {
    notice = `${name} added.`;
    remove(id);
  }

  function dismissed(id) {
    notice = 'Dismissed — it won\'t be suggested again.';
    remove(id);
  }

  onMount(reload);
</script>

<div class="found">
  <p class="visually-hidden" role="status">{notice}</p>
  {#if loading}
    <p class="found-status loading muted" role="status">
      <span class="spinner" aria-hidden="true"></span>Looking at the containers and config folders on this server…
    </p>
  {:else if error}
    <p class="error" role="alert">{error}</p>
    <button type="button" class="quiet retry" onclick={reload}>Try again</button>
  {:else if !setUp}
    <p class="found-status muted" tabindex="-1">
      Observarr can list the apps on this server when it can read the Docker container list or the apps' config
      folders. Both need a small change to how Observarr runs —
      <a href={ADDING_APPS_DOCS} target="_blank"
        rel="noopener noreferrer">see “Adding your apps”</a>.
    </p>
  {:else if rows.length === 0}
    <p class="found-status muted" tabindex="-1">Nothing new found on this server.</p>
  {/if}

  {#if rows.length > 0}
    <ul class="rows" bind:this={listEl}>
      {#each rows as sug (sug.id)}
        <SuggestionRow {sug} adapter={adapterFor(adapters, sug.type)} {taken}
          onadded={added} ondismissed={dismissed} ongone={reload} />
      {/each}
    </ul>
  {/if}

  {#if !loading && problems.length > 0}
    <details class="problems">
      <summary>{problems.length} thing{problems.length === 1 ? '' : 's'} could not be read</summary>
      <ul>
        {#each problems as p, i (i)}
          <li><b>{p.target}</b>: {p.message}</li>
        {/each}
      </ul>
    </details>
  {/if}
</div>

<style>
  .found { display: grid; gap: 8px; min-width: 0; }
  .found-status { font-size: var(--t-base); padding: 6px 0; max-width: 90ch; }
  .found-status.loading { display: flex; align-items: center; gap: 10px; }
  .found-status:focus { outline: none; }
  .rows { margin: 0; padding: 0; display: grid; }
  .retry { justify-self: start; }
  .problems { font-size: var(--t-sm); color: var(--muted); }
  .problems summary { cursor: pointer; width: fit-content; }
  .problems ul { margin: 6px 0 0; padding-left: 18px; display: grid; gap: 2px; overflow-wrap: anywhere; }
  .problems b { color: var(--text2); font-weight: 550; }
  .spinner {
    width: 14px; height: 14px; border-radius: 50%; flex: none;
    border: 2px solid var(--field-border); border-top-color: var(--accent); animation: spin 0.8s linear infinite;
  }
  @keyframes spin { to { transform: rotate(360deg); } }
</style>
