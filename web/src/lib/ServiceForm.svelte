<script>
  import { untrack } from 'svelte';
  import { api } from './api.js';
  import { originOf, selfHostOf } from './urls.js';

  // ServiceForm adds a service by hand (mode "add") or changes one (mode "edit", with `service`).
  // It never names an app type: the list of apps comes from `adapters` (GET /api/adapters).
  // `containers` (from the system snapshot) feeds the optional container select: a service may be
  // linked to a Docker container so its card shows the container's state and usage. `ondone` runs
  // after a successful save, `oncancel` (when given) adds a Cancel button. The caller resets the
  // form by re-creating it ({#key}).
  let { mode = 'add', service = null, adapters = [], containers = [], oncancel, ondone } = $props();
  const uid = $props.id();

  // Initial values, read once: the caller re-creates the form for another service.
  const initial = untrack(() => (mode === 'edit' && service
    ? { type: service.type, name: service.name, url: service.url, container: service.container?.name ?? '',
      hasKey: service.hasKey }
    : { type: '', name: '', url: '', container: '', hasKey: false }));

  let type = $state(initial.type);
  let name = $state(initial.name);
  let url = $state(initial.url);
  let urlTouched = $state(initial.url !== ''); // an existing URL is never auto-replaced
  let apiKey = $state('');
  let clearKey = $state(false); // edit mode: remove the saved key of an app that does not need one
  let containerName = $state(initial.container); // '' = no linked container
  const originalContainer = initial.container; // edit mode: the value to diff a patch against

  let testState = $state('idle'); // idle | testing | ok | error
  let testError = $state('');
  let testedVersion = $state('');
  let testedKey = $state(null); // JSON of {type,url,apiKey} at the last successful test

  let saving = $state(false);
  let saveError = $state('');

  const sortedAdapters = $derived([...adapters].sort((a, b) => a.name.localeCompare(b.name)));
  // The container names, plus whatever is already linked (edit), even if that one is no longer in
  // the current list (a stopped or renamed container, say).
  const containerOptions = $derived.by(() => {
    const names = new Set(containers.map((c) => c.name));
    if (service?.container?.name) names.add(service.container.name);
    return [...names].sort((a, b) => a.localeCompare(b));
  });
  const info = $derived(adapters.find((a) => a.type === type) ?? null);
  const needsKey = $derived(info?.needsApiKey ?? false);
  // An adapter that names its secret field (keyLabel) without needing it takes an optional one.
  const showKey = $derived(needsKey || !!info?.keyLabel);
  const keyLabel = $derived(info?.keyLabel || 'API key');
  const keyName = $derived(info?.keyLabel ? info.keyLabel.toLowerCase() : 'API key');
  const selfHost = $derived(selfHostOf(url));

  // addressChanged (edit mode only) is true once the scheme, host or port differ from the
  // service's current URL — the server then requires a saved key again, so the UI mirrors that
  // requirement up front.
  const addressChanged = $derived(mode === 'edit' && !!service && originOf(url) !== originOf(service.url));
  const keyAgain = $derived(addressChanged && (needsKey || initial.hasKey) && !clearKey);
  const editKeyMissing = $derived(keyAgain && apiKey === '');
  const keyHint = $derived(
    mode !== 'edit' ? info?.keyHint ?? '' :
      clearKey ? 'Removed when you save' :
        keyAgain ? `Enter the ${keyName} again when changing the address` :
          initial.hasKey ? 'Leave blank to keep the current one' : info?.keyHint ?? '',
  );

  const currentKey = $derived(JSON.stringify({ type, url, apiKey }));
  const testOk = $derived(testState === 'ok' && testedKey === currentKey);
  const canTest = $derived(testState !== 'testing' && type !== '' && url.trim() !== '' && (!needsKey || apiKey !== ''));
  // Add mode requires a successful Test matching the current inputs. Edit mode never requires one:
  // the server re-detects itself whenever the url or key actually changed (and answers 422, shown
  // below as saveError, if that fails), so Test there is only ever a preview.
  const canSubmit = $derived(
    !saving && name.trim() !== '' && type !== '' && !editKeyMissing && (mode === 'edit' || testOk),
  );
  const needsTestHint = $derived(mode === 'add' && !testOk && !saving && name.trim() !== '' && type !== '' && url.trim() !== '');
  // A plain-HTTP app answers nothing sensible to an HTTPS request, which usually surfaces as a
  // generic transport failure — the most common cause, so worth naming.
  const httpsMismatchHint = $derived(
    testState === 'error' && url.trim().toLowerCase().startsWith('https://') && testError.toLowerCase().includes('request failed'),
  );

  function defaultUrlFor(t) {
    const a = adapters.find((x) => x.type === t);
    return a ? `http://${window.location.hostname}:${a.defaultPort}` : '';
  }

  function onTypeChange() {
    // Refill the URL with the new app's default host:port, but only while the user has not
    // touched the field themselves.
    if (!urlTouched) url = defaultUrlFor(type);
    if (name.trim() === '' || adapters.some((a) => a.name === name)) name = info?.name ?? name;
  }

  async function runTest() {
    testState = 'testing';
    testError = '';
    const key = currentKey;
    try {
      const res = await api('POST', '/api/services/test', { type, url, apiKey });
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
        if (url !== service.url) patch.url = url;
        if (clearKey) patch.apiKey = ''; // an empty key removes the saved one
        else if (apiKey !== '') patch.apiKey = apiKey;
        if (containerName !== originalContainer) patch.container = containerName;
        await api('PATCH', `/api/services/${service.id}`, patch);
      } else {
        await api('POST', '/api/services', { type, name: name.trim(), url, apiKey, container: containerName });
      }
      ondone?.(name.trim());
    } catch (err) {
      saveError = err.message;
    } finally {
      saving = false;
    }
  }
</script>

<form class="service-form" onsubmit={submit}>
  <div class="fields">
    <label>
      App
      <select bind:value={type} disabled={mode === 'edit'} required onchange={onTypeChange}>
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

    <label class="wide">
      URL
      <input bind:value={url} oninput={() => { urlTouched = true; }} required maxlength="2048" autocomplete="off"
        spellcheck="false" placeholder="http://host:port" aria-describedby={selfHost && mode === 'add' ? `${uid}-self` : undefined} />
    </label>
    {#if mode === 'add' && selfHost}
      <p id={`${uid}-self`} class="muted hint wide">{selfHost} is Observarr itself — use the app's container name or LAN address</p>
    {/if}

    {#if showKey}
      <label>
        <span>{keyLabel}{#if !needsKey}{' '}<span class="muted hint">· optional</span>{/if}</span>
        <input type="password" bind:value={apiKey} autocomplete="off" maxlength="256" required={keyAgain}
          disabled={clearKey} aria-describedby={keyHint ? `${uid}-keyhint` : undefined} />
        {#if keyHint}<span id={`${uid}-keyhint`} class="muted hint">{keyHint}</span>{/if}
      </label>
      {#if mode === 'edit' && initial.hasKey && !needsKey}
        <label class="check wide">
          <input type="checkbox" bind:checked={clearKey} onchange={() => { apiKey = ''; }} />
          Remove saved {keyName}
        </label>
      {/if}
    {/if}

    {#if containerOptions.length > 0}
      <label>
        <span>Container <span class="muted hint">· optional</span></span>
        <select bind:value={containerName}>
          <option value="">No container</option>
          {#each containerOptions as cname (cname)}
            <option value={cname}>{cname}</option>
          {/each}
        </select>
      </label>
    {/if}
  </div>

  <div class="status" aria-live="polite">
    {#if testState === 'testing'}
      <p class="muted">Testing…</p>
    {:else if testState === 'error'}
      <p class="error" role="alert">{testError}</p>
      {#if httpsMismatchHint}<p class="muted hint">Does the app use http://?</p>{/if}
    {:else if testState === 'ok'}
      {#if testOk}
        <p class="ok-text">Looks good — version {testedVersion}.</p>
      {:else}
        <p class="muted">Inputs changed since the last test — test again.</p>
      {/if}
    {:else if needsTestHint}
      <p class="muted hint">Run Test before adding</p>
    {/if}
    {#if saveError}
      <p class="error" role="alert">{saveError}</p>
    {/if}
  </div>

  <div class="actions">
    {#if oncancel}<button type="button" class="quiet" onclick={oncancel}>Cancel</button>{/if}
    <button type="button" class="quiet" disabled={!canTest} onclick={runTest}>Test</button>
    <button disabled={!canSubmit}>{mode === 'edit' ? 'Save' : 'Add'}</button>
  </div>
</form>

<style>
  .service-form { display: grid; gap: 14px; container-type: inline-size; }
  .fields { display: grid; gap: 14px; grid-template-columns: repeat(2, minmax(0, 1fr)); }
  @container (max-width: 440px) { .fields { grid-template-columns: minmax(0, 1fr); } }
  .fields .wide { grid-column: 1 / -1; }
  label { display: grid; gap: 6px; font-size: 13px; color: var(--text2); align-content: start; }
  label.check { display: flex; align-items: center; gap: 8px; }
  .hint { font-size: 12px; }
  .ok-text { color: var(--ok); }
  .status:empty { display: none; }
  .status { display: grid; gap: 4px; }
  p { font-size: 13px; }
  .actions { display: flex; gap: 8px; justify-content: flex-end; flex-wrap: wrap; }
</style>
