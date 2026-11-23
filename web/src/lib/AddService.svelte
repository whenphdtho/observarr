<script>
  import { untrack } from 'svelte';
  import { api } from './api.js';

  // AddService is the Add/Edit dialog. In add mode it creates a service; in edit mode it patches
  // one. It never names an app type: the list of apps comes from `adapters` (GET /api/adapters),
  // passed in by the caller. An optional `suggestion` (from Discover.svelte) prefills type
  // (locked), name, URL and (for a Docker find) the linked container; `onadded` is called with the
  // suggestion's id once it has been added, `ongone` when the server no longer knows the
  // suggestion (expired or already added). `containers` (from GET /api/system) feeds the optional
  // container select: a service may be linked to a Docker container so its card shows the
  // container's state and usage.
  let { open = $bindable(false), mode = 'add', service = null, suggestion = null, adapters = [],
    containers = [], onadded, ongone } = $props();

  // A host name, an IPv4 address or a bracketed IPv6 address: no scheme, port, path or spaces.
  // Used as the input's pattern (anchored by the browser) and for the buttons' enabled state.
  const hostPattern = '[A-Za-z0-9.\\-]+|\\[[0-9A-Fa-f:.]+\\]';
  const hostRe = new RegExp(`^(?:${hostPattern})$`, 'u');

  // SELF_HOSTNAMES: a URL pointing here is almost always a mistake — Observarr is talking to its
  // own container, never the app being added.
  const SELF_HOSTNAMES = new Set(['127.0.0.1', 'localhost', '::1']);

  let dialogEl;
  let opener = null;
  let wasOpen = false;

  let type = $state('');
  let name = $state('');
  let url = $state('');
  let urlTouched = $state(false);
  let apiKey = $state('');
  let hostInput = $state('');
  let useFoundKey = $state(false);
  let containerName = $state(''); // '' = no linked container
  let originalContainer = $state(''); // edit mode: the value to diff a patch against

  let testState = $state('idle'); // idle | testing | ok | error
  let testError = $state('');
  let testedVersion = $state('');
  let testedKey = $state(null); // JSON of {type,url,apiKey} at the last successful test

  let saving = $state(false);
  let saveError = $state('');

  const sortedAdapters = $derived([...adapters].sort((a, b) => a.name.localeCompare(b.name)));
  // The container's own name, plus whatever is already linked (edit) or suggested (Docker find),
  // even if that one is no longer in the current list (a stopped or renamed container, say).
  const containerOptions = $derived.by(() => {
    const names = new Set(containers.map((c) => c.name));
    if (service?.container?.name) names.add(service.container.name);
    if (suggestion?.container) names.add(suggestion.container);
    return [...names].sort((a, b) => a.localeCompare(b));
  });
  const info = $derived(adapters.find((a) => a.type === type) ?? null);
  const needsKey = $derived(info?.needsApiKey ?? false);
  // An adapter that names its secret field (keyLabel) without needing it takes an optional one.
  const showKey = $derived(needsKey || !!info?.keyLabel);
  const keyLabel = $derived(info?.keyLabel || 'API key');

  // A suggestion whose host is unknown ("http://:8989/..." style) shows a Host field instead of
  // the URL field; effectiveUrl is what actually gets tested/submitted.
  const needsHost = $derived(suggestion !== null && !suggestion.hostKnown);
  const effectiveUrl = $derived(needsHost ? composeUrl(suggestion?.url ?? '', hostInput.trim()) : url);
  const hostOk = $derived(!needsHost || hostRe.test(hostInput.trim()));
  const targetHost = $derived(hostOf(effectiveUrl));
  // Using the key the suggestion found means the browser never sees it, so there is nothing to
  // Test with; Add itself runs Detect server-side.
  const skipTest = $derived(suggestion !== null && useFoundKey);

  // selfHost is the URL's host when it resolves to Observarr's own container — a near-certain
  // mistake, since it's never the app being added — and '' otherwise.
  const selfHost = $derived.by(() => {
    if (effectiveUrl.trim() === '') return '';
    const h = hostnameOf(effectiveUrl).replace(/^\[|\]$/g, '');
    return SELF_HOSTNAMES.has(h) ? h : '';
  });

  // addressChanged (edit mode only) is true once the scheme, host or port differ from the
  // service's current URL — the server then requires the API key again (contract: changing a
  // service's address needs the key again), so the UI mirrors that requirement up front.
  const addressChanged = $derived(mode === 'edit' && !!service && originOf(url) !== originOf(service.url));
  const editKeyMissing = $derived(mode === 'edit' && needsKey && addressChanged && apiKey === '');

  const currentKey = $derived(JSON.stringify({ type, url: effectiveUrl, apiKey }));
  const testOk = $derived(testState === 'ok' && testedKey === currentKey);
  const canTest = $derived(
    testState !== 'testing' && type !== '' && effectiveUrl.trim() !== '' &&
      hostOk && (!needsKey || apiKey !== ''),
  );
  // Add mode requires a successful Test matching the current inputs, unless the found key is
  // used (Test is skipped then). Edit mode never requires one: the server re-detects itself
  // whenever the url or key actually changed (and answers 422, shown below as saveError, if that
  // fails), so Test there is only ever a preview.
  const canSubmit = $derived(
    !saving && name.trim() !== '' && type !== '' && hostOk && !editKeyMissing &&
      (mode === 'edit' || skipTest || testOk),
  );
  // needsTestHint explains the one remaining reason Add is disabled, once every other field is
  // already filled in correctly: Test has not yet passed for the current inputs.
  const needsTestHint = $derived(
    mode === 'add' && !skipTest && !testOk && !saving &&
      name.trim() !== '' && type !== '' && hostOk && effectiveUrl.trim() !== '',
  );
  // httpsMismatchHint: a plain-HTTP app answers nothing sensible to an HTTPS request (it's not
  // listening with TLS at all), which usually surfaces as a generic transport failure — this is
  // the single most common cause, so it's worth naming even though Test can fail for other reasons
  // too.
  const httpsMismatchHint = $derived(
    testState === 'error' && effectiveUrl.trim().toLowerCase().startsWith('https://') &&
      testError.toLowerCase().includes('request failed'),
  );

  // splitSuggestionUrl pulls scheme/port/path out of a hostless suggestion URL (Go's
  // url.URL.String() with an empty host, e.g. "http://:8989/app" or "http:///"), which the
  // browser's URL parser refuses to parse at all.
  function splitSuggestionUrl(raw) {
    const m = /^(https?):\/\/(?::(\d+))?(\/[^?#]*)?$/i.exec(raw ?? '');
    if (!m) return { scheme: 'http', port: '', path: '' };
    return { scheme: m[1].toLowerCase(), port: m[2] ?? '', path: m[3] ?? '' };
  }

  function composeUrl(raw, host) {
    const { scheme, port, path } = splitSuggestionUrl(raw);
    return `${scheme}://${host}${port ? ':' + port : ''}${path}`;
  }

  function hostOf(raw) {
    try {
      return new URL(raw).host;
    } catch {
      return raw;
    }
  }

  function hostnameOf(raw) {
    try {
      return new URL(raw).hostname;
    } catch {
      return '';
    }
  }

  // originOf is scheme+host (host already includes a non-default port) — comparing it is how the
  // UI tells "the address changed" from "only the path changed".
  function originOf(raw) {
    try {
      const u = new URL(raw);
      return `${u.protocol}//${u.host}`;
    } catch {
      return raw;
    }
  }

  function defaultUrlFor(t) {
    const a = adapters.find((x) => x.type === t);
    return a ? `http://${window.location.hostname}:${a.defaultPort}` : '';
  }

  function resetForm() {
    saveError = '';
    testError = '';
    testState = 'idle';
    testedKey = null;
    testedVersion = '';
    hostInput = '';
    useFoundKey = false;
    if (mode === 'edit' && service) {
      type = service.type;
      name = service.name;
      url = service.url;
      urlTouched = true; // an existing URL is never auto-replaced
      apiKey = '';
      containerName = service.container?.name ?? '';
      originalContainer = containerName;
    } else if (suggestion) {
      type = suggestion.type;
      name = suggestion.name;
      url = suggestion.hostKnown ? suggestion.url : '';
      urlTouched = true; // the type is locked, so there is no default port to refill with anyway
      apiKey = '';
      // A Docker find already knows which container it came from.
      containerName = suggestion.container ?? '';
      originalContainer = '';
    } else {
      type = ''; // "Select an app…" — the user always chooses explicitly, nothing is preselected
      name = '';
      url = '';
      urlTouched = false;
      apiKey = '';
      containerName = '';
      originalContainer = '';
    }
  }

  function onUrlInput() {
    urlTouched = true;
  }

  function onTypeChange() {
    // Add mode only (the type select is disabled in edit mode): refill the URL with the new
    // app's default host:port, but only while the user has not touched the field themselves.
    if (!urlTouched) url = defaultUrlFor(type);
  }

  async function runTest() {
    testState = 'testing';
    testError = '';
    const key = currentKey;
    try {
      const res = await api('POST', '/api/services/test', { type, url: effectiveUrl, apiKey });
      testedVersion = res.version;
      testedKey = key;
      testState = 'ok';
    } catch (err) {
      testError = err.message;
      testState = 'error';
      testedKey = null;
    }
  }

  async function submit(event) {
    event.preventDefault();
    if (!canSubmit) return;
    saving = true;
    saveError = '';
    try {
      if (mode === 'edit' && service) {
        const patch = { name: name.trim() };
        if (effectiveUrl !== service.url) patch.url = effectiveUrl;
        if (apiKey !== '') patch.apiKey = apiKey;
        if (containerName !== originalContainer) patch.container = containerName;
        await api('PATCH', `/api/services/${service.id}`, patch);
      } else {
        const payload = {
          type, name: name.trim(), url: effectiveUrl, apiKey: skipTest ? '' : apiKey, container: containerName,
        };
        if (suggestion) {
          payload.suggestion = suggestion.id;
          payload.useFoundKey = useFoundKey;
        }
        await api('POST', '/api/services', payload);
      }
      open = false;
      if (suggestion) onadded?.(suggestion.id);
    } catch (err) {
      if (err.status === 410) {
        saveError = 'This suggestion is gone — it expired or was already added';
        ongone?.();
      } else {
        saveError = err.message;
      }
    } finally {
      saving = false;
    }
  }

  function handleDialogClose() {
    open = false;
    opener?.focus();
    opener = null;
  }

  $effect(() => {
    if (!dialogEl) return;
    if (open) {
      // untrack: resetForm reads mode/service/adapters, which must not become dependencies of
      // this effect — otherwise an unrelated change to any of them while the dialog is already
      // open would re-run this block and wipe whatever the user has typed.
      if (!wasOpen) untrack(() => { resetForm(); opener = document.activeElement; });
      if (!dialogEl.open) dialogEl.showModal();
    } else if (dialogEl.open) {
      dialogEl.close();
    }
    wasOpen = open;
  });
</script>

<dialog bind:this={dialogEl} aria-labelledby="addServiceTitle" onclose={handleDialogClose}>
  <form onsubmit={submit}>
    <h2 id="addServiceTitle">{mode === 'edit' ? 'Edit service' : 'Add service'}</h2>

    <label>
      App
      <select bind:value={type} disabled={mode === 'edit' || suggestion !== null} required onchange={onTypeChange}>
        {#if type === ''}<option value="" disabled>Select an app…</option>{/if}
        {#each sortedAdapters as a (a.type)}
          <option value={a.type}>{a.name}</option>
        {/each}
      </select>
    </label>

    <label>
      Name
      <input bind:value={name} required maxlength="64" autocomplete="off" />
    </label>

    {#if needsHost}
      <label>
        Host
        <input bind:value={hostInput} required maxlength="253" autocomplete="off" spellcheck="false"
          pattern={hostPattern} title="A host name, an IPv4 address or an [IPv6] address"
          placeholder="e.g. 192.168.1.20 or nas.local" aria-describedby="composedUrl" />
      </label>
      {#if hostInput.trim() === ''}
        <p id="composedUrl" class="muted">Enter the host to complete the URL</p>
      {:else}
        <p id="composedUrl" class="muted ellipsis" title={effectiveUrl}>{effectiveUrl}</p>
      {/if}
    {:else}
      <label>
        URL
        <input bind:value={url} oninput={onUrlInput} required maxlength="2048" autocomplete="off" spellcheck="false" />
      </label>
    {/if}

    {#if mode === 'add' && selfHost}
      <p class="muted hint">{selfHost} is Observarr itself — use the app's container name or LAN address</p>
    {/if}

    {#if containerOptions.length > 0}
      <label>
        Container
        <select bind:value={containerName}>
          <option value="">No container</option>
          {#each containerOptions as cname (cname)}
            <option value={cname}>{cname}</option>
          {/each}
        </select>
      </label>
    {/if}

    {#if suggestion?.keyFound}
      <label class="checkbox">
        <input type="checkbox" bind:checked={useFoundKey} />
        Use the API key found in {suggestion.keySource}
      </label>
      {#if useFoundKey}
        <p class="muted ellipsis" title={`The key will be sent to ${targetHost}`}>The key will be sent to {targetHost}</p>
      {/if}
    {/if}

    {#if showKey && !skipTest}
      <label>
        {keyLabel}{needsKey ? '' : ' (optional)'}
        {#if mode === 'edit'}
          <span class="muted hint">
            {addressChanged ? `Enter the ${keyLabel.toLowerCase()} again when changing the address` : 'leave blank to keep the current one'}
          </span>
        {:else if info?.keyHint}
          <span class="muted hint">{info.keyHint}</span>
        {/if}
        <input type="password" bind:value={apiKey} autocomplete="off" maxlength="256"
          required={mode === 'edit' && addressChanged && needsKey} />
      </label>
    {/if}

    {#if skipTest}
      <!-- Test needs the key itself, which never reaches the browser; Add runs Detect server-side. -->
    {:else if testState === 'testing'}
      <p class="muted" role="status">Testing…</p>
    {:else if testState === 'error'}
      <p class="error" role="alert">{testError}</p>
      {#if httpsMismatchHint}<p class="muted hint">Does the app use http://?</p>{/if}
    {:else if testState === 'ok'}
      {#if testOk}
        <p class="ok-text" role="status">Looks good — version {testedVersion}.</p>
      {:else}
        <p class="muted" role="status">Inputs changed since the last test — test again.</p>
      {/if}
    {:else if needsTestHint}
      <p class="muted hint" role="status">Run Test before adding</p>
    {/if}

    {#if saveError}
      <p class="error" role="alert">{saveError}</p>
    {/if}

    <div class="actions">
      <button type="button" class="quiet" onclick={() => { open = false; }}>Cancel</button>
      {#if !skipTest}
        <button type="button" class="quiet" disabled={!canTest} onclick={runTest}>Test</button>
      {/if}
      <button disabled={!canSubmit}>{mode === 'edit' ? 'Save' : 'Add'}</button>
    </div>
  </form>
</dialog>

<style>
  form { display: grid; gap: 14px; }
  label.checkbox { display: flex; align-items: center; gap: 8px; }
  .hint { font-size: 12px; }
  .ok-text { color: var(--ok); }
  p { font-size: 13px; }
  .actions { margin-top: 4px; }
</style>
