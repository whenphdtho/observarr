<script>
  import { untrack } from 'svelte';
  import { api } from './api.js';
  import { TYPES, RULES } from './alerts.js';
  import { CONTROL } from './format.js';

  // AlertChannelForm adds a channel (channel null) or edits one, in place in the Alerts section. A saved secret is
  // never shown: its field stays empty and keeps it, typing replaces it, and an optional one can be removed. A
  // channel is saved off; it is switched on after a test got through, and changing where it sends needs a new test.
  let { channel = null, onsaved, oncancel, onremoved } = $props();

  // Initial values, read once: the caller makes a new form for another channel.
  const initial = untrack(() => channel);
  const editing = initial !== null;
  let type = $state(initial?.type ?? 'ntfy');
  let name = $state(initial?.name ?? '');
  const before = initial?.settings ?? {};
  let values = $state({ security: 'starttls', ...Object.fromEntries(Object.entries(before).map(([k, v]) => [k, String(v)])) });
  let removed = $state({});
  let rules = $state(new Set(initial?.rules ?? RULES.map((r) => r.id)));
  let submitted = $state(false);
  let touched = $state({});
  let error = $state('');
  let busy = $state(false);
  let confirming = $state(false);

  const fields = $derived(TYPES[type].fields);
  const saved = (f) => editing && f.secret && channel.hasSecret?.[f.key] && !removed[f.key];

  function problem(f) {
    const v = (values[f.key] ?? '').trim();
    if (f.choices) return '';
    if (v === '') return saved(f) || f.optional ? '' : 'Required.';
    return f.check?.(v) ?? '';
  }
  const problems = $derived(Object.fromEntries(fields.map((f) => [f.key, problem(f)])));
  const nameProblem = $derived(!name.trim() ? 'Required.' : CONTROL.test(name) ? 'No line breaks or other control characters.' : '');
  const valid = $derived(!nameProblem && Object.values(problems).every((p) => !p));
  const shown = (key) => submitted || touched[key];

  function settingsOf() {
    const out = {};
    for (const f of fields) {
      if (f.secret) continue;
      const v = (values[f.key] ?? '').trim();
      if (v === '') continue;
      out[f.key] = f.type === 'number' ? Number(v) : v;
    }
    return out;
  }

  function secretsOf() {
    const out = {};
    for (const f of fields) {
      if (!f.secret) continue;
      const v = (values[f.key] ?? '').trim();
      if (v) out[f.key] = v;
      else if (removed[f.key]) out[f.key] = '';
    }
    return out;
  }

  function toggleRule(id, on) {
    const next = new Set(rules);
    if (on) next.add(id);
    else next.delete(id);
    rules = next;
  }

  async function submit(event) {
    event.preventDefault();
    submitted = true;
    if (!valid) return;
    busy = true;
    error = '';
    const settings = settingsOf();
    const secrets = secretsOf();
    const ruleList = RULES.map((r) => r.id).filter((id) => rules.has(id));
    try {
      let view;
      if (!editing) {
        view = await api('POST', '/api/alerts/channels', { type, name: name.trim(), settings, secrets, rules: ruleList });
      } else {
        const body = { name: name.trim(), rules: ruleList };
        // Only changed settings are sent: sending them switches the channel off until it is tested again.
        if (fields.some((f) => !f.secret && String(settings[f.key] ?? '') !== String(before[f.key] ?? ''))) {
          body.settings = settings;
        }
        if (Object.keys(secrets).length) body.secrets = secrets;
        view = await api('PUT', `/api/alerts/channels/${channel.id}`, body);
      }
      onsaved(view);
    } catch (err) {
      error = err.message;
    } finally {
      busy = false;
    }
  }

  async function remove() {
    busy = true;
    error = '';
    try {
      await api('DELETE', `/api/alerts/channels/${channel.id}`);
      onremoved(channel.id);
    } catch (err) {
      error = err.message;
      busy = false;
    }
  }
</script>

<form class="channel-form" onsubmit={submit} novalidate aria-label={editing ? `Edit ${channel.name}` : 'Add a channel'}>
  <div class="grid">
    {#if !editing}
      <label>
        Type
        <select bind:value={type}>
          {#each Object.entries(TYPES) as [id, t] (id)}<option value={id}>{t.label}</option>{/each}
        </select>
      </label>
    {/if}
    <label>
      Name
      <input bind:value={name} maxlength="64" placeholder="Phone" autocomplete="off"
        aria-invalid={shown('name') && !!nameProblem} onblur={() => (touched.name = true)} />
      {#if shown('name') && nameProblem}<span class="hint error">{nameProblem}</span>{/if}
    </label>
    {#each fields as f (type + f.key)}
      {#if f.choices}
        <fieldset class="choices">
          <legend>{f.label}</legend>
          <div class="seg" role="group" aria-label={f.label}>
            {#each f.choices as [v, label] (v)}
              <button type="button" aria-pressed={values[f.key] === v} onclick={() => (values[f.key] = v)}>{label}</button>
            {/each}
          </div>
        </fieldset>
      {:else}
        {@const id = `alert-field-${f.key}`}
        {@const bad = shown(f.key) && !!problems[f.key]}
        <label>
          <span class="label-row">
            {f.label}{#if f.optional}<span class="muted">{' · optional'}</span>{/if}
          </span>
          <input {id} bind:value={values[f.key]} type={f.secret ? 'password' : f.type === 'number' ? 'number' : 'text'}
            inputmode={f.type === 'number' ? 'numeric' : undefined}
            placeholder={saved(f) ? 'Saved · leave empty to keep' : (f.placeholder ?? '')}
            autocomplete={f.secret ? 'new-password' : 'off'} spellcheck="false" maxlength={f.secret ? 2048 : 512}
            aria-invalid={bad} aria-describedby={bad || f.hint ? `${id}-hint` : undefined}
            onblur={() => (touched[f.key] = true)} />
          {#if bad}
            <span id={`${id}-hint`} class="hint error">{problems[f.key]}</span>
          {:else if f.hint}
            <span id={`${id}-hint`} class="hint muted">{f.hint}</span>
          {/if}
          {#if saved(f) && f.optional}
            <button type="button" class="linkish" onclick={() => { removed[f.key] = true; values[f.key] = ''; }}>
              Remove the saved {f.label.toLowerCase()}
            </button>
          {:else if removed[f.key]}
            <span class="hint warn-text">Removed when you save.</span>
          {/if}
        </label>
      {/if}
    {/each}
  </div>

  <fieldset class="rules">
    <legend>Sends</legend>
    {#each RULES as r (r.id)}
      <label class="choice">
        <input type="checkbox" checked={rules.has(r.id)} onchange={(e) => toggleRule(r.id, e.currentTarget.checked)} />
        <span>{r.label}</span>
      </label>
    {/each}
  </fieldset>

  {#if editing}
    <p class="muted small">Changing where it sends switches the channel off until a test gets through.</p>
  {:else}
    <p class="muted small">Saved off: send a test, then switch it on.</p>
  {/if}
  <div aria-live="polite" class="status">
    {#if error}<p class="error" role="alert">{error}</p>{/if}
  </div>

  {#if confirming}
    <div class="actions confirm" role="group" aria-label="Confirm removal">
      <span class="what">Remove {channel.name}? Its log entries stay.</span>
      <button type="button" class="quiet" onclick={() => (confirming = false)} disabled={busy}>Keep</button>
      <button type="button" onclick={remove} disabled={busy}>Remove</button>
    </div>
  {:else}
    <div class="actions">
      {#if editing}
        <button type="button" class="quiet danger" onclick={() => (confirming = true)} disabled={busy}>Remove</button>
      {/if}
      <button type="button" class="quiet" onclick={oncancel} disabled={busy}>Cancel</button>
      <button disabled={busy}>{busy ? 'Saving…' : editing ? 'Save' : 'Add channel'}</button>
    </div>
  {/if}
</form>

<style>
  .channel-form {
    display: grid; gap: 14px; padding: 14px 0 16px; container-type: inline-size; min-width: 0;
  }
  .grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px 16px; }
  @container (max-width: 480px) { .grid { grid-template-columns: minmax(0, 1fr); } }
  label { display: grid; gap: 6px; font-size: var(--t-body); color: var(--text2); min-width: 0; align-content: start; }
  .label-row { min-width: 0; }
  fieldset { border: 0; margin: 0; padding: 0; min-width: 0; }
  legend { padding: 0; margin-bottom: 6px; font-size: var(--t-body); color: var(--text2); }
  .choices .seg { background: var(--field); }
  .rules { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 6px 16px; }
  .rules legend { grid-column: 1 / -1; }
  @container (max-width: 480px) { .rules { grid-template-columns: minmax(0, 1fr); } }
  label.choice { display: flex; align-items: flex-start; gap: 8px; color: var(--text); }
  label.choice input { margin: 2px 0 0; flex: none; }
  .hint, .small { font-size: var(--t-small); }
  .linkish {
    justify-self: start; padding: 0; border: 0; background: none; color: var(--accent-text); font-size: var(--t-small);
    font-weight: 400; text-decoration: underline; text-underline-offset: 3px;
  }
  .linkish:hover:not(:disabled) { background: none; border: 0; color: var(--accent-text-hi); }
  .status { display: grid; gap: 4px; }
  .status:empty { display: none; }
  .actions { display: flex; gap: 8px; justify-content: flex-end; flex-wrap: wrap; align-items: center; }
  .actions .danger { margin-right: auto; }
  .actions .danger:hover:not(:disabled) { color: var(--fail); }
  .confirm .what { margin-right: auto; font-size: var(--t-body); overflow-wrap: anywhere; }
</style>
