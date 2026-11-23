<script>
  import { api } from './api.js';
  import { hostOf, selfHostOf, withHost } from './urls.js';
  import Icon from './icons/Icon.svelte';

  // SuggestionRow is one app discovery found. Nothing is contacted until the user opens the row
  // with Add and presses "Test & add": the server then checks the app (Detect) and saves it in one
  // step (POST /api/services with the suggestion's id). A key the server found stays there: the
  // browser only ever asks to use it. It never names an app type — `adapter` is the matching entry
  // of GET /api/adapters. onadded(id, name) runs once the service is saved, ondismissed(id) once
  // the row was dismissed, ongone() when the server no longer knows the suggestion.
  let { sug, adapter = null, taken = [], onadded, ondismissed, ongone } = $props();
  const uid = $props.id();

  let open = $state(false);
  let name = $state('');
  let url = $state('');
  let apiKey = $state('');
  let useFoundKey = $state(false);
  let busy = $state(''); // '' | 'adding' | 'dismissing'
  let error = $state('');
  let gone = $state(false);
  let nameEl = $state();
  let addEl;

  const appName = $derived(adapter?.name ?? sug.type);
  const needsKey = $derived(adapter?.needsApiKey ?? false);
  // An adapter that names its secret field (keyLabel) without needing it takes an optional one.
  const showKey = $derived(needsKey || !!adapter?.keyLabel);
  const keyLabel = $derived(adapter?.keyLabel || 'API key');
  // A host-less find (a config folder only knows the port) gets this browser's host as a guess.
  const guessedUrl = $derived(sug.hostKnown ? sug.url : withHost(sug.url, window.location.hostname));
  const where = $derived(
    sug.container ? `Container ${sug.container}` :
      sug.source === 'config' ? 'Config folder' :
        sug.source === 'linked' ? 'Linked app' : 'Network',
  );
  const keyTarget = $derived(hostOf(url));
  const selfHost = $derived(selfHostOf(url));
  const canAdd = $derived(
    busy === '' && !gone && name.trim() !== '' && url.trim() !== '' && (!needsKey || useFoundKey || apiKey !== ''),
  );

  // freeName is base, or — when a service already has that name (`taken`, compared ignoring case)
  // — base with the container's name or a number, so the prefilled name doesn't collide.
  function freeName(base) {
    const used = new Set(taken.map((n) => n.toLowerCase()));
    if (!used.has(base.toLowerCase())) return base;
    if (sug.container && !used.has(`${base} ${sug.container}`.toLowerCase())) return `${base} ${sug.container}`;
    for (let i = 2; ; i++) {
      if (!used.has(`${base} ${i}`.toLowerCase())) return `${base} ${i}`;
    }
  }

  async function toggle() {
    if (!open) {
      name = freeName(sug.name || appName);
      url = guessedUrl;
      apiKey = '';
      useFoundKey = false; // a found key is only ever used when the user ticks the box
      error = '';
      open = true;
      await Promise.resolve();
      nameEl?.focus();
    } else {
      open = false;
      addEl?.focus();
    }
  }

  async function add(event) {
    event.preventDefault();
    if (!canAdd) return;
    busy = 'adding';
    error = '';
    try {
      await api('POST', '/api/services', {
        type: sug.type, name: name.trim(), url: url.trim(), apiKey: useFoundKey ? '' : apiKey,
        container: sug.container ?? '', suggestion: sug.id, useFoundKey,
      });
      onadded?.(sug.id, name.trim());
    } catch (err) {
      if (err.status === 410) {
        gone = true;
        error = 'This suggestion is gone — it expired or was already added.';
        ongone?.();
      } else if (err.status === 409) {
        error = `${err.message} — choose another name.`;
      } else {
        error = err.message;
      }
    } finally {
      busy = '';
    }
  }

  async function dismiss() {
    busy = 'dismissing';
    error = '';
    try {
      await api('POST', `/api/discovery/${sug.id}/dismiss`, {});
      ondismissed?.(sug.id);
    } catch (err) {
      if (err.status === 410) {
        ongone?.();
      } else {
        error = err.message;
      }
    } finally {
      busy = '';
    }
  }
</script>

<li class="sug" class:open>
  <div class="row">
    <span class="tile-icon" aria-hidden="true"><Icon name={adapter?.icon} size={17} /></span>
    <div class="info">
      <span class="name ellipsis" title={appName}>{appName}</span>
      <span class="meta ellipsis" title={`${where} · ${sug.hostKnown ? sug.url : 'host unknown'}`}>
        {where} · {sug.hostKnown ? hostOf(sug.url) : 'host unknown'}
      </span>
    </div>
    {#if sug.keyFound}
      <span class="chip key" title={`API key found in ${sug.keySource}`}>key found</span>
    {/if}
    <div class="buttons">
      <button type="button" class:quiet={open} bind:this={addEl} aria-expanded={open} aria-controls={open ? `${uid}-form` : undefined}
        disabled={busy !== '' || gone} onclick={toggle}>
        {#if open}Close{:else}<Icon name="plus" size={14} />Add{/if}
        <span class="visually-hidden"> {appName}{sug.container ? ` (${sug.container})` : ''}</span>
      </button>
      <button type="button" class="quiet" disabled={busy !== ''} onclick={dismiss}
        title="Don't suggest this again">
        Dismiss<span class="visually-hidden"> {appName}{sug.container ? ` (${sug.container})` : ''}</span>
      </button>
    </div>
  </div>

  {#if open}
    <form id={`${uid}-form`} class="mini" onsubmit={add} aria-label={`Add ${appName}`}>
      <label>
        Name
        <input bind:this={nameEl} bind:value={name} required maxlength="64" autocomplete="off" />
      </label>
      <label>
        URL
        <input bind:value={url} required maxlength="2048" autocomplete="off" spellcheck="false" />
        {#if !sug.hostKnown}
          <span class="muted hint">Only the port is known — check the host.</span>
        {:else if selfHost}
          <span class="muted hint">{selfHost} is Observarr itself — use the app's container name or LAN address.</span>
        {/if}
      </label>
      {#if sug.keyFound}
        <label class="check">
          <input type="checkbox" bind:checked={useFoundKey} />
          <span>Use the API key found in <b>{sug.keySource}</b></span>
        </label>
        {#if useFoundKey}
          <p class="muted hint wide ellipsis" title={`The key will be sent to ${keyTarget}`}>The key will be sent to {keyTarget}</p>
        {/if}
      {/if}
      {#if showKey && !useFoundKey}
        <label>
          <span>{keyLabel}{#if !needsKey}{' '}<span class="muted hint">· optional</span>{/if}</span>
          <input type="password" bind:value={apiKey} autocomplete="off" maxlength="256"
            aria-describedby={adapter?.keyHint ? `${uid}-keyhint` : undefined} />
          {#if adapter?.keyHint}<span id={`${uid}-keyhint`} class="muted hint">{adapter.keyHint}</span>{/if}
        </label>
      {/if}
      <div class="form-foot">
        <div class="state" aria-live="polite">
          {#if busy === 'adding'}
            <span class="spinner" aria-hidden="true"></span><span class="muted">Checking {appName}…</span>
          {:else if error}
            <p class="error" role="alert">{error}</p>
          {:else}
            <span class="muted hint">Observarr contacts the app only when you press Test & add.</span>
          {/if}
        </div>
        <button disabled={!canAdd}>Test & add</button>
      </div>
    </form>
  {:else if error}
    <p class="error row-error" role="alert">{error}</p>
  {/if}
</li>

<style>
  .sug { list-style: none; border-top: 1px solid var(--border); padding: 12px 0; min-width: 0; }
  .sug:first-child { border-top: 0; }
  .row { display: flex; align-items: center; gap: 12px; min-width: 0; }
  .row .tile-icon { width: 34px; height: 34px; }
  .info { display: grid; min-width: 0; flex: 1; line-height: 1.35; }
  .name { font-weight: 600; font-size: 13.5px; }
  .meta { font-size: 12.5px; color: var(--muted); }
  .key { flex: 0 1 auto; }
  .buttons { display: flex; gap: 8px; flex: none; }
  .buttons button { min-width: 74px; }
  .mini {
    display: grid; gap: 12px; margin: 12px 0 2px 46px; padding: 14px; border-radius: var(--r-md);
    background: var(--surface2); border: 1px solid var(--border); grid-template-columns: repeat(auto-fit, minmax(min(100%, 200px), 1fr));
  }
  .mini input:not([type='checkbox']) { background: var(--bg); }
  label { display: grid; gap: 6px; font-size: 13px; color: var(--text2); align-content: start; min-width: 0; }
  label.check { grid-column: 1 / -1; display: flex; align-items: flex-start; gap: 8px; color: var(--text); }
  label.check input { flex: none; margin-top: 2px; }
  label.check span { overflow-wrap: anywhere; }
  .hint { font-size: 12px; }
  .wide { grid-column: 1 / -1; }
  .form-foot { grid-column: 1 / -1; display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
  .state { flex: 1 1 200px; display: flex; align-items: center; gap: 8px; min-width: 0; font-size: 13px; }
  .form-foot button { margin-left: auto; }
  .row-error { margin: 8px 0 0 46px; font-size: 13px; }
  .spinner {
    width: 14px; height: 14px; border-radius: 50%; flex: none;
    border: 2px solid var(--field-border); border-top-color: var(--accent); animation: spin 0.8s linear infinite;
  }
  @keyframes spin { to { transform: rotate(360deg); } }

  @container (max-width: 560px) {
    .row { flex-wrap: wrap; }
    .info { flex: 1 1 calc(100% - 46px); }
    .key { margin-left: 46px; }
    .buttons { flex: 1 1 100%; padding-left: 46px; }
    .buttons button { flex: 1; }
    .mini { margin-left: 0; }
    .row-error { margin-left: 0; }
  }
</style>
