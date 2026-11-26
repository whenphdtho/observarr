<script>
  import AppIcon from './AppIcon.svelte';
  import { api, download } from './api.js';
  import { adapters } from './data.js';

  // Backup exports the saved apps (and dismissed finds) as a settings file, with their keys encrypted under a
  // passphrase or left out, and restores such a file: preview first, then add alongside or replace, then a
  // summary. Restored apps are polled like any saved app; nothing contacts them before.
  const MIN_PASS = 12;
  const MAX_FILE = 1 << 20;

  let withKeys = $state(true);
  let pass = $state('');
  let pass2 = $state('');
  let password = $state(''); // the account's: the keys leave the server only with it
  let exportError = $state('');
  let exporting = $state(false);
  let exported = $state('');

  const passShort = $derived(withKeys && pass !== '' && [...pass].length < MIN_PASS);
  const passMismatch = $derived(withKeys && pass2 !== '' && pass !== pass2);
  const canExport = $derived(!exporting &&
    (!withKeys || ([...pass].length >= MIN_PASS && pass === pass2 && password !== '')));

  async function exportFile(event) {
    event.preventDefault();
    if (!canExport) return;
    exporting = true;
    exportError = '';
    exported = '';
    try {
      const { blob, name } = await download('/api/settings/export',
        withKeys ? { passphrase: pass, password } : { withoutKeys: true });
      const a = document.createElement('a');
      a.href = URL.createObjectURL(blob);
      a.download = name;
      a.click();
      // Some browsers start the download only after the click returns: keep the file's URL a moment longer.
      setTimeout(() => URL.revokeObjectURL(a.href), 1000);
      exported = withKeys ? `Downloaded ${name}. Keep the passphrase: the keys cannot be restored without it.` :
        `Downloaded ${name}, without keys.`;
      pass = pass2 = password = '';
    } catch (err) {
      exportError = err.message;
    } finally {
      exporting = false;
    }
  }

  // Restore: 'pick' a file (and its passphrase), 'preview' what it holds, then 'done'.
  let step = $state('pick');
  let fileText = $state('');
  let fileName = $state('');
  let fileError = $state('');
  let hasKeys = $state(false);
  let restorePass = $state('');
  let skipKeys = $state(false);
  let mode = $state('add');
  let confirmRemove = $state(false); // replace: the user agreed to remove the apps saved now
  let report = $state(null);
  let restoreError = $state('');
  let busy = $state(false);

  const byType = $derived(Object.fromEntries($adapters.map((a) => [a.type, a])));
  const taken = $derived(report?.services.filter((s) => s.nameTaken).length ?? 0);
  const toAdd = $derived(report ? report.services.length - (mode === 'add' ? taken : 0) : 0);
  const canApply = $derived(!busy && toAdd > 0 && (mode === 'add' || report.saved === 0 || confirmRemove));

  async function pickFile(event) {
    const file = event.currentTarget.files?.[0];
    fileText = fileName = fileError = restoreError = '';
    hasKeys = false;
    if (!file) return;
    if (file.size > MAX_FILE) {
      fileError = 'This file is larger than 1 MiB; a settings file is much smaller.';
      return;
    }
    fileName = file.name;
    fileText = await file.text();
    try {
      hasKeys = JSON.parse(fileText)?.secrets != null;
    } catch {
      hasKeys = false; // the server says what is wrong with it
    }
  }

  function body(m) {
    const b = { file: fileText, mode: m };
    if (hasKeys && skipKeys) b.withoutKeys = true;
    else if (hasKeys) b.passphrase = restorePass;
    return b;
  }

  async function preview(event) {
    event.preventDefault();
    busy = true;
    restoreError = '';
    try {
      report = await api('POST', '/api/settings/import', body('preview'));
      mode = 'add';
      confirmRemove = false;
      step = 'preview';
    } catch (err) {
      restoreError = err.message;
    } finally {
      busy = false;
    }
  }

  async function apply() {
    busy = true;
    restoreError = '';
    try {
      report = await api('POST', '/api/settings/import', body(mode));
      step = 'done';
      restorePass = '';
    } catch (err) {
      restoreError = err.message;
    } finally {
      busy = false;
    }
  }

  function reset() {
    step = 'pick';
    report = null;
    fileText = fileName = restorePass = restoreError = '';
    hasKeys = skipKeys = confirmRemove = false;
  }

  function keyText(s) {
    if (s.key === 'included') return 'key included';
    if (s.key === 'missing') return 'enter key after restoring';
    return 'no key';
  }

  function plural(n, word) {
    return `${n} ${word}${n === 1 ? '' : 's'}`;
  }
</script>

<section class="section backup" aria-labelledby="backup-title">
  <div class="card-head">
    <h2 id="backup-title">Backup and restore</h2>
  </div>
  <p class="lede">
    One file with every app (address, container link, key), the alert channels and rules, and the finds you
    dismissed. Accounts, sessions,
    history and the audit log stay on this server.
  </p>

  <div class="cols">
    <form class="col" onsubmit={exportFile}>
      <h3>Export</h3>
      <fieldset>
        <legend>Keys</legend>
        <label class="choice">
          <input type="radio" name="export-keys" value={true} bind:group={withKeys} />
          <span>Encrypt keys with a passphrase</span>
        </label>
        <label class="choice">
          <input type="radio" name="export-keys" value={false} bind:group={withKeys} />
          <span>Leave keys out <span class="muted">· enter them again after restoring</span></span>
        </label>
      </fieldset>
      {#if withKeys}
        <label>
          Passphrase
          <input type="password" bind:value={pass} autocomplete="new-password" maxlength="1024"
            aria-invalid={passShort} aria-describedby="backup-pass-hint" />
          <span id="backup-pass-hint" class="hint" class:error={passShort} class:muted={!passShort}>
            At least {MIN_PASS} characters. Without it the keys cannot be restored.
          </span>
        </label>
        <label>
          Confirm passphrase
          <input type="password" bind:value={pass2} autocomplete="new-password" maxlength="1024"
            aria-invalid={passMismatch} aria-describedby={passMismatch ? 'backup-pass-mismatch' : undefined} />
          {#if passMismatch}<span id="backup-pass-mismatch" class="hint error">The passphrases differ.</span>{/if}
        </label>
        <label>
          Your password
          <input type="password" bind:value={password} autocomplete="current-password" maxlength="1024"
            aria-describedby="backup-password-hint" />
          <span id="backup-password-hint" class="hint muted">The one you sign in with: the keys leave only with it.</span>
        </label>
      {/if}
      <div aria-live="polite" class="status">
        {#if exportError}<p class="error" role="alert">{exportError}</p>{/if}
        {#if exported}<p class="ok-text">{exported}</p>{/if}
      </div>
      <div class="actions">
        <button disabled={!canExport}>{exporting ? 'Preparing…' : 'Download'}</button>
      </div>
    </form>

    <div class="col">
      <h3>Restore</h3>
      {#if step === 'pick'}
        <form class="col" onsubmit={preview}>
          <label>
            Settings file
            <input type="file" accept=".json,application/json" onchange={pickFile} />
          </label>
          {#if fileError}<p class="error" role="alert">{fileError}</p>{/if}
          {#if hasKeys}
            <label>
              Passphrase
              <input type="password" bind:value={restorePass} autocomplete="off" maxlength="1024" disabled={skipKeys}
                required={!skipKeys} />
            </label>
            <label class="choice">
              <input type="checkbox" bind:checked={skipKeys} onchange={() => { restorePass = ''; }} />
              <span>Restore without keys <span class="muted">· enter them again after restoring</span></span>
            </label>
          {/if}
          <div aria-live="polite" class="status">
            {#if restoreError}<p class="error" role="alert">{restoreError}</p>{/if}
          </div>
          <div class="actions">
            <button disabled={!fileText || busy || (hasKeys && !skipKeys && !restorePass)}>
              {busy ? 'Reading…' : 'Preview'}
            </button>
          </div>
        </form>
      {:else}
        <p class="facts-line">
          {#if step === 'done'}
            <strong>Restored {plural(report.services.length, 'app')}.</strong>
            {#if report.removed}{plural(report.removed, 'app')} removed.{/if}
          {:else}
            <strong class="ellipsis" title={fileName}>{fileName}</strong>
            <span class="muted">{plural(report.services.length, 'app')}{report.createdAt ?
              `, exported ${new Date(report.createdAt).toLocaleDateString()}` : ''}{report.observarrVersion ?
              ` by Observarr ${report.observarrVersion}` : ''}</span>
          {/if}
        </p>

        {#if report.services.length > 0}
          <ul class="apps">
            {#each report.services as s (s.name)}
              {@const skip = step === 'preview' && mode === 'add' && s.nameTaken}
              <li class:skip>
                <span class="icon"><AppIcon adapter={byType[s.type]} size={16} /></span>
                <span class="who">
                  <span class="ellipsis name" title={s.name}>{s.name}</span>
                  <span class="ellipsis muted url" title={s.url}>{byType[s.type]?.name ?? s.type} at {s.url}</span>
                </span>
                <span class="key" class:warn-text={skip || s.key === 'missing'}>
                  {skip ? 'name in use · skipped' : keyText(s)}
                </span>
              </li>
            {/each}
          </ul>
        {/if}

        {#if report.skipped.length > 0}
          <div class="skipped">
            <p class="muted">Skipped:</p>
            <ul>
              {#each report.skipped as s, i (i)}
                <li><span class="name">{s.name}</span> <span class="muted">({byType[s.type]?.name ?? s.type})</span> — {s.reason}</li>
              {/each}
            </ul>
          </div>
        {/if}
        {#if report.dismissed > 0}
          <p class="muted small">{step === 'done' ? 'Restored' : 'Also restores'} {plural(report.dismissed, 'dismissed find')}.</p>
        {/if}

        {#if step === 'preview'}
          <fieldset>
            <legend>Apps saved here</legend>
            <label class="choice">
              <input type="radio" name="restore-mode" value="add" bind:group={mode} />
              <span>Add alongside{#if taken}{' '}<span class="muted">· {plural(taken, 'name')} in use will be skipped</span>{/if}</span>
            </label>
            <label class="choice">
              <input type="radio" name="restore-mode" value="replace" bind:group={mode} />
              <span>Replace all <span class="muted">· removes every app saved here first</span></span>
            </label>
          </fieldset>
          {#if mode === 'replace' && report.saved > 0}
            <label class="choice confirm">
              <input type="checkbox" bind:checked={confirmRemove} />
              <span>Remove the {plural(report.saved, 'app')} saved now</span>
            </label>
          {/if}
          <p class="muted small">Nothing is saved until you apply. Restored apps are checked on their first poll.</p>
          <div aria-live="polite" class="status">
            {#if restoreError}<p class="error" role="alert">{restoreError}</p>{/if}
          </div>
          <div class="actions">
            <button type="button" class="quiet" disabled={busy} onclick={reset}>Cancel</button>
            <button type="button" disabled={!canApply} onclick={apply}>
              {busy ? 'Applying…' : mode === 'replace' ? `Replace with ${plural(toAdd, 'app')}` : `Add ${plural(toAdd, 'app')}`}
            </button>
          </div>
        {:else}
          {#if report.services.some((s) => s.key === 'missing')}
            <p class="muted small">Apps marked “enter key after restoring” wait on their card until the key is entered.</p>
          {/if}
          <div class="actions">
            <button type="button" class="quiet" onclick={reset}>Restore another file</button>
          </div>
        {/if}
      {/if}
    </div>
  </div>
</section>

<style>
  .backup { container-type: inline-size; }
  .lede { color: var(--text2); font-size: var(--t-body); max-width: 72ch; margin: -8px 0 18px; }
  .cols { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 20px 32px; }
  @container (max-width: 640px) { .cols { grid-template-columns: minmax(0, 1fr); } }
  .col { display: grid; gap: 14px; align-content: start; min-width: 0; }
  h3 { font-size: var(--t-h3); }

  fieldset { border: 0; margin: 0; padding: 0; display: grid; gap: 8px; min-width: 0; }
  legend { padding: 0; margin-bottom: 8px; font-size: var(--t-body); color: var(--text2); }
  label { display: grid; gap: 6px; font-size: var(--t-body); color: var(--text2); min-width: 0; }
  label.choice { display: flex; align-items: flex-start; gap: 8px; color: var(--text); }
  label.choice input { margin: 2px 0 0; flex: none; accent-color: var(--accent); }
  input[type='radio'] { width: 16px; height: 16px; }
  input[type='file'] { padding: 5px; font-size: var(--t-body); color: var(--text2); }
  input[type='file']::file-selector-button {
    font: inherit; color: var(--text); background: var(--surface2); border: 1px solid var(--btn-edge);
    border-radius: var(--r-sm); padding: 4px 10px; margin-right: 10px; cursor: pointer;
  }
  input[type='file']::file-selector-button:hover { background: var(--surface3); border-color: var(--btn-edge-hi); }
  .hint, .small { font-size: var(--t-small); }
  p { font-size: var(--t-body); }
  .ok-text { color: var(--ok); overflow-wrap: anywhere; }
  .status { display: grid; gap: 4px; }
  .status:empty { display: none; }
  .actions { display: flex; gap: 8px; justify-content: flex-end; flex-wrap: wrap; }

  .facts-line { display: flex; flex-wrap: wrap; gap: 2px 8px; align-items: baseline; min-width: 0; }
  .facts-line strong { font-weight: var(--w-strong); max-width: 100%; }

  .apps { list-style: none; margin: 0; padding: 0; border-top: 1px solid var(--border); }
  .apps li {
    display: flex; align-items: center; gap: 10px; padding: 8px 0; border-bottom: 1px solid var(--border); min-width: 0;
  }
  .apps .icon { color: var(--text2); flex: none; display: grid; }
  .who { display: grid; min-width: 0; flex: 1; line-height: 1.35; }
  .who .name { font-weight: var(--w-label); font-size: var(--t-body); }
  .who .url { font-size: var(--t-small); }
  .key { flex: none; font-size: var(--t-small); color: var(--muted); white-space: nowrap; }
  .apps li.skip .who { opacity: 0.6; }
  .skipped ul { margin: 4px 0 0; padding-left: 18px; font-size: var(--t-body); }
  .skipped li { overflow-wrap: anywhere; }
  .skipped .name { font-weight: var(--w-label); }

  /* Narrow: the key note goes under the address instead of beside it. */
  @container (max-width: 420px) {
    .apps li { flex-wrap: wrap; row-gap: 2px; }
    .key { flex-basis: 100%; padding-left: 26px; white-space: normal; }
  }
</style>
