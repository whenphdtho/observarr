<script>
  import { onMount, tick } from 'svelte';
  import { flip } from 'svelte/animate';
  import AlertChannelForm from './AlertChannelForm.svelte';
  import Icon from './icons/Icon.svelte';
  import Skeleton from './Skeleton.svelte';
  import { api } from './api.js';
  import { TYPES, RULES, RULE_NAMES, firing, severityTone, scrollToAlerts } from './alerts.js';
  import { now } from './ticker.js';
  import { formatAge } from './format.js';
  import { arrive, leave, reveal, conceal, smoothHeight, FLIP } from './motion.js';

  // Alerts is the System view's section for what Observarr sends while nobody has it open: the alerts firing now,
  // the channels (each tested before it is switched on), the rules and what was sent lately. #/system/alerts
  // scrolls here.
  const ALERTS_HASH = '#/system/alerts';
  const LOG_SIZE = 20;
  const REPEATS = [[0, 'Never'], [1, 'Every hour'], [6, 'Every 6 hours'], [12, 'Every 12 hours'], [24, 'Every day']];

  let channels = $state(null);
  let loadError = $state('');
  let adding = $state(false);
  let editing = $state(null); // the id of the channel being edited
  let testing = $state({}); // id → true while a test runs
  let results = $state({}); // id → { ok, note } of the test just sent
  let toggleError = $state({});

  let rules = $state(null);
  let draft = $state(null);
  let rulesError = $state('');
  let rulesSaved = $state(false);
  let savingRules = $state(false);
  const dirty = $derived(rules && draft && JSON.stringify(rules) !== JSON.stringify(draft));

  let log = $state(null);

  async function loadChannels() {
    try {
      channels = await api('GET', '/api/alerts/channels');
      loadError = '';
    } catch (err) {
      loadError = err.message;
    }
  }

  async function loadRules() {
    try {
      rules = await api('GET', '/api/alerts/rules');
      draft = structuredClone($state.snapshot(rules));
    } catch (err) {
      rulesError = err.message;
    }
  }

  async function loadLog() {
    try {
      log = await api('GET', `/api/alerts/log?limit=${LOG_SIZE}`);
    } catch {
      // Keep what is shown; the next refresh tries again.
    }
  }

  function replace(view) {
    channels = channels.map((c) => (c.id === view.id ? view : c));
  }

  async function test(ch) {
    testing = { ...testing, [ch.id]: true };
    results = { ...results, [ch.id]: undefined };
    try {
      const r = await api('POST', `/api/alerts/channels/${ch.id}/test`);
      results = { ...results, [ch.id]: { ok: r.ok, note: r.note } };
      if (r.channel) replace(r.channel);
      loadLog();
    } catch (err) {
      results = { ...results, [ch.id]: { ok: false, note: err.message } };
    } finally {
      testing = { ...testing, [ch.id]: false };
    }
  }

  async function setEnabled(ch, input) {
    toggleError = { ...toggleError, [ch.id]: '' };
    try {
      replace(await api('PUT', `/api/alerts/channels/${ch.id}`, { enabled: input.checked }));
    } catch (err) {
      toggleError = { ...toggleError, [ch.id]: err.message };
      input.checked = ch.enabled; // put the switch back
    }
  }

  async function saveRules(event) {
    event.preventDefault();
    savingRules = true;
    rulesError = '';
    rulesSaved = false;
    try {
      rules = await api('PUT', '/api/alerts/rules', $state.snapshot(draft));
      draft = structuredClone($state.snapshot(rules));
      rulesSaved = true;
    } catch (err) {
      rulesError = err.message;
    } finally {
      savingRules = false;
    }
  }

  // status is a channel's state in one line: { text, tone }.
  function status(ch) {
    if (ch.problem) return { text: ch.problem, tone: 'fail' };
    const last = ch.last;
    const age = last ? formatAge(last.at, $now) : '';
    if (ch.enabled && last && !last.ok) return { text: `On · last delivery failed ${age}: ${last.note}`, tone: 'fail' };
    if (ch.enabled) return { text: last ? `On · last sent ${age}` : 'On · nothing sent yet', tone: 'ok' };
    if (ch.tested) return { text: 'Off · test passed', tone: '' };
    if (last && !last.ok) return { text: `Off · test failed: ${last.note}`, tone: 'fail' };
    return { text: 'Off · send a test to switch it on', tone: '' };
  }

  const EVENTS = { resolved: 'Resolved', repeat: 'Still firing', summary: 'Summary' };
  const DELIVERY_TONE = { sent: 'ok', failed: 'fail', dropped: 'fail', suppressed: 'warn', held: 'warn' };

  function firingText(list) {
    if (!list) return '';
    return list.length === 0 ? 'Nothing firing' : `${list.length} firing`;
  }

  async function onHash() {
    if (window.location.hash !== ALERTS_HASH) return;
    await tick();
    scrollToAlerts();
  }

  onMount(() => {
    loadChannels();
    loadRules();
    loadLog();
    onHash();
    const id = setInterval(loadLog, 30 * 1000);
    window.addEventListener('hashchange', onHash);
    return () => {
      clearInterval(id);
      window.removeEventListener('hashchange', onHash);
    };
  });
</script>

<section id="alerts" class="section alerts" aria-labelledby="alerts-title">
  <div class="card-head">
    <h2 id="alerts-title">Alerts</h2>
    <span class="meta">{firingText($firing)}</span>
  </div>
  <p class="lede">Observarr tells you here what needs attention, even while nobody has it open.</p>

  {#if $firing?.length}
    <ul class="firing" aria-label="Firing now">
      {#each $firing as a (a.key)}
        <li class={severityTone(a.severity)} in:arrive out:leave animate:flip={FLIP}>
          <span class="dot {severityTone(a.severity)}" aria-hidden="true"></span>
          <span class="what">
            <span class="visually-hidden">{a.severity}: </span>
            <span class="title ellipsis" title={a.title}>{a.title}</span>
            {#if a.message}<span class="detail">{a.message}</span>{/if}
          </span>
          <span class="since muted" title={new Date(a.since).toLocaleString()}>since {formatAge(a.since, $now)}</span>
        </li>
      {/each}
    </ul>
  {/if}

  <div class="cols">
    <div class="col">
      <div class="sub-head">
        <h3>Channels</h3>
        {#if !adding}
          <button type="button" class="quiet" onclick={() => { adding = true; editing = null; }}>
            <Icon name="plus" size={15} />Add channel
          </button>
        {/if}
      </div>
      {#if adding}
        <div in:reveal out:conceal>
          <AlertChannelForm oncancel={() => (adding = false)}
            onsaved={(view) => { channels = [...channels, view].sort((a, b) => a.name.localeCompare(b.name)); adding = false; }} />
        </div>
      {/if}
      {#if loadError}
        <p class="error" role="alert">{loadError}</p>
      {:else if channels === null}
        <Skeleton kind="rows" count={2} label="Loading the channels…" />
      {:else if channels.length === 0 && !adding}
        <p class="none">
          No channel yet. Add ntfy, Discord, a webhook, email, Gotify or Telegram, send a test, then switch it on.
        </p>
      {:else}
        <ul class="channels">
          {#each channels as ch (ch.id)}
            {@const st = status(ch)}
            {@const result = results[ch.id]}
            <li>
              <div class="row">
                <span class="dot {st.tone}" aria-hidden="true"></span>
                <span class="who">
                  <span class="name ellipsis" title={ch.name}>{ch.name}</span>
                  <span class="target ellipsis muted" title={ch.target}>{TYPES[ch.type]?.label ?? ch.type}{ch.target ? ` · ${ch.target}` : ''}</span>
                  <span class="state" class:fail-text={st.tone === 'fail'}>{st.text}</span>
                </span>
                <span class="tools">
                  <button type="button" class="quiet small" onclick={() => test(ch)} disabled={testing[ch.id] || !!ch.problem}>
                    {testing[ch.id] ? 'Sending…' : 'Send test'}<span class="visually-hidden"> to {ch.name}</span>
                  </button>
                  <label class="switch" title={ch.tested || ch.enabled ? '' : 'Send a test first'}>
                    <input type="checkbox" role="switch" checked={ch.enabled} disabled={!ch.tested && !ch.enabled}
                      onchange={(e) => setEnabled(ch, e.currentTarget)} />
                    <span>On<span class="visually-hidden">: {ch.name}</span></span>
                  </label>
                  <button type="button" class="ibtn" aria-label={`Edit ${ch.name}`} title="Edit"
                    aria-expanded={editing === ch.id} onclick={() => { editing = editing === ch.id ? null : ch.id; adding = false; }}>
                    <Icon name="pencil" size={15} />
                  </button>
                </span>
              </div>
              <div aria-live="polite" class="row-status">
                {#if result}
                  <p class:ok-text={result.ok} class:error={!result.ok}>
                    {result.ok ? 'Test delivered. You can switch the channel on.' : `Test failed: ${result.note}`}
                  </p>
                {/if}
                {#if toggleError[ch.id]}<p class="error" role="alert">{toggleError[ch.id]}</p>{/if}
              </div>
              {#if editing === ch.id}
                <div in:reveal out:conceal>
                  <AlertChannelForm channel={ch} oncancel={() => (editing = null)}
                    onsaved={(view) => { replace(view); editing = null; results = { ...results, [ch.id]: undefined }; }}
                    onremoved={(id) => { channels = channels.filter((c) => c.id !== id); editing = null; }} />
                </div>
              {/if}
            </li>
          {/each}
        </ul>
      {/if}
    </div>

    <form class="col rules" onsubmit={saveRules}>
      <h3>Rules</h3>
      {#if draft === null}
        {#if rulesError}<p class="error" role="alert">{rulesError}</p>{:else}<Skeleton count={3} label="Loading the rules…" />{/if}
      {:else}
        <ul class="rule-list">
          {#each RULES as r (r.id)}
            <li>
              <label class="choice">
                <input type="checkbox" bind:checked={draft.enabled[r.id]} />
                <span>{r.label}</span>
              </label>
              {#if r.threshold}
                <label class="threshold">
                  <span class="muted">{r.threshold.label}</span>
                  <input type="number" inputmode="numeric" min={r.threshold.min} max={r.threshold.max}
                    bind:value={draft[r.threshold.key]} disabled={!draft.enabled[r.id]}
                    aria-label={`${r.label}: ${r.threshold.label} (${r.threshold.unit})`} />
                  <span class="muted">{r.threshold.unit}</span>
                </label>
              {/if}
            </li>
          {/each}
        </ul>
        <div class="more">
          <label>
            Remind of an alert still firing
            <select bind:value={draft.repeatHours}>
              {#each REPEATS as [h, label] (h)}<option value={h}>{label}</option>{/each}
            </select>
          </label>
          <label>
            Messages per channel and hour, at most
            <input type="number" inputmode="numeric" min="1" max="600" bind:value={draft.perHour}
              aria-describedby="alerts-per-hour-hint" />
            <span id="alerts-per-hour-hint" class="hint muted">Beyond, one message says how many were held back.</span>
          </label>
          <fieldset class="quiet-hours">
            <legend>
              <label class="choice">
                <input type="checkbox" bind:checked={draft.quiet.on} />
                <span>Quiet hours</span>
              </label>
            </legend>
            <div class="times">
              <label>From <input type="time" bind:value={draft.quiet.from} disabled={!draft.quiet.on} /></label>
              <label>to <input type="time" bind:value={draft.quiet.to} disabled={!draft.quiet.on} /></label>
            </div>
            <span class="hint muted">Only critical alerts go out; the rest follow in one summary.</span>
          </fieldset>
          <label>
            <span>Observarr's address, for links in alerts<span class="muted">{' · optional'}</span></span>
            <input type="url" bind:value={draft.link} placeholder="https://observarr.example.com" spellcheck="false" />
          </label>
        </div>
        <div aria-live="polite" class="status">
          {#if rulesError}<p class="error" role="alert">{rulesError}</p>{/if}
          {#if rulesSaved && !dirty}<p class="ok-text">Saved.</p>{/if}
        </div>
        <div class="actions">
          {#if dirty}
            <button type="button" class="quiet" onclick={() => (draft = structuredClone($state.snapshot(rules)))}>Undo</button>
          {/if}
          <button disabled={!dirty || savingRules}>{savingRules ? 'Saving…' : 'Save rules'}</button>
        </div>
      {/if}
    </form>
  </div>

  <div class="recent">
    <h3>Recently sent</h3>
    {#if log === null}
      <Skeleton count={2} label="Loading the log…" />
    {:else if log.length === 0}
      <p class="none">Nothing sent yet. Alerts and tests show here with what each channel made of them.</p>
    {:else}
      <div use:smoothHeight>
      <ul class="log">
        {#each log as e (e.id)}
          <li in:arrive animate:flip={FLIP}>
            <span class="dot {e.event === 'resolved' || e.event === 'test' ? 'ok' : severityTone(e.severity)}" aria-hidden="true"></span>
            <span class="what">
              <span class="line">
                {#if EVENTS[e.event]}<span class="event">{EVENTS[e.event]}</span>{/if}
                <span class="title">{e.title}</span>
              </span>
              {#if e.message}<span class="detail">{e.message}</span>{/if}
              <span class="deliveries">
                {#if e.deliveries.length === 0}
                  <span class="muted">{RULE_NAMES[e.rule] ? 'No channel is switched on for this rule' : 'Not sent'}</span>
                {/if}
                {#each e.deliveries as d (d.channelId)}
                  <span class="delivery {DELIVERY_TONE[d.status] ?? ''}" title={d.note || undefined}>
                    {d.channel}: {d.status}{d.status === 'failed' && d.note ? ` (${d.note})` : ''}
                  </span>
                {/each}
              </span>
            </span>
            <time class="muted" datetime={e.at} title={new Date(e.at).toLocaleString()}>{formatAge(e.at, $now)}</time>
          </li>
        {/each}
      </ul>
      </div>
    {/if}
  </div>
</section>

<style>
  .alerts { container-type: inline-size; scroll-margin-top: 16px; }
  .lede { color: var(--text2); font-size: var(--t-body); max-width: 72ch; margin: -8px 0 18px; }
  h3 { font-size: var(--t-h3); }
  .cols { display: grid; grid-template-columns: minmax(0, 3fr) minmax(0, 2fr); gap: 20px 32px; }
  @container (max-width: 760px) { .cols { grid-template-columns: minmax(0, 1fr); } }
  .col { display: grid; gap: 12px; align-content: start; min-width: 0; }
  .sub-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; min-height: 32px; }
  .none { color: var(--text2); font-size: var(--t-body); max-width: 60ch; }
  .hint { font-size: var(--t-small); }
  .ok-text { color: var(--ok); overflow-wrap: anywhere; font-size: var(--t-body); }
  p.error { font-size: var(--t-body); }

  /* Firing now: the rows the bell leads to. Their dot and words say the state; the rows stay plain. */
  .firing { position: relative; list-style: none; margin: 0 0 20px; padding: 0; display: grid; }
  .firing li {
    display: flex; align-items: center; gap: 10px; padding: 9px 2px; min-width: 0; border-bottom: 1px solid var(--border);
  }
  .firing li:first-child { border-top: 1px solid var(--border); }
  .firing .what { display: grid; min-width: 0; flex: 1; line-height: 1.35; }
  .firing .title { font-weight: var(--w-label); font-size: var(--t-body); }
  .detail { font-size: var(--t-small); color: var(--text2); overflow-wrap: anywhere; }
  .since { flex: none; font-size: var(--t-small); white-space: nowrap; }

  .channels { list-style: none; margin: 0; padding: 0; border-top: 1px solid var(--border); }
  .channels > li { border-bottom: 1px solid var(--border); min-width: 0; }
  .row { display: flex; align-items: center; gap: 10px; padding: 10px 0; min-width: 0; }
  .row > .dot { align-self: flex-start; margin-top: 7px; }
  .who { display: grid; min-width: 0; flex: 1; line-height: 1.4; }
  .who .name { font-weight: var(--w-label); font-size: var(--t-body); }
  .who .target, .who .state { font-size: var(--t-small); }
  .who .state { color: var(--text2); overflow-wrap: anywhere; }
  .tools { display: flex; align-items: center; gap: 8px; flex: none; }
  button.small { padding: 4px 10px; font-size: var(--t-small); }
  .switch { display: inline-flex; align-items: center; gap: 6px; font-size: var(--t-small); color: var(--text2); cursor: pointer; }
  .switch input { margin: 0; }
  .switch:has(input:disabled) { cursor: default; opacity: 0.6; }
  .row-status { display: grid; gap: 4px; }
  .row-status:empty { display: none; }
  .row-status p { padding: 0 0 10px 17px; font-size: var(--t-small); }
  @container (max-width: 520px) {
    .firing li { flex-wrap: wrap; row-gap: 2px; }
    .firing .since { flex-basis: 100%; padding-left: 17px; }
    .row { flex-wrap: wrap; }
    .tools { flex-basis: 100%; padding-left: 17px; }
  }

  .rules { gap: 14px; }
  .rule-list { list-style: none; margin: 0; padding: 0; display: grid; gap: 8px; }
  .rule-list li { display: grid; gap: 6px; }
  label { display: grid; gap: 6px; font-size: var(--t-body); color: var(--text2); min-width: 0; }
  label.choice { display: flex; align-items: flex-start; gap: 8px; color: var(--text); }
  label.choice input { margin: 2px 0 0; flex: none; }
  /* A rule's threshold sits under its name, in line with the name's text. */
  .threshold { display: flex; align-items: center; gap: 6px; padding-left: 24px; font-size: var(--t-small); white-space: nowrap; }
  .threshold input { width: 72px; padding: 4px 8px; text-align: right; }
  .more { display: grid; gap: 14px; padding-top: 14px; border-top: 1px solid var(--border); }
  fieldset { border: 0; margin: 0; padding: 0; min-width: 0; display: grid; gap: 8px; }
  legend { padding: 0; margin-bottom: 8px; }
  .times { display: flex; gap: 12px; flex-wrap: wrap; }
  .times label { display: inline-flex; align-items: center; gap: 8px; }
  .times input { width: auto; }
  .more input[type='number'] { max-width: 120px; }
  .status { display: grid; gap: 4px; }
  .status:empty { display: none; }
  .actions { display: flex; gap: 8px; justify-content: flex-end; flex-wrap: wrap; }

  .recent { margin-top: 24px; padding-top: 18px; border-top: 1px solid var(--border); display: grid; gap: 12px; }
  .log { list-style: none; margin: 0; padding: 0; }
  .log li { display: flex; align-items: flex-start; gap: 10px; padding: 9px 0; border-bottom: 1px solid var(--border); min-width: 0; }
  .log li:last-child { border-bottom: 0; }
  .log .dot { margin-top: 7px; }
  .log .what { display: grid; gap: 2px; flex: 1; min-width: 0; line-height: 1.4; }
  .log .line { display: flex; gap: 8px; align-items: baseline; min-width: 0; font-size: var(--t-body); }
  .log .event { flex: none; font-size: var(--t-small); color: var(--text2); }
  .log .title { font-weight: var(--w-label); overflow-wrap: anywhere; }
  .deliveries { display: flex; flex-wrap: wrap; gap: 4px 12px; font-size: var(--t-small); color: var(--text2); }
  .delivery { overflow-wrap: anywhere; }
  .delivery.fail { color: var(--fail); }
  .delivery.warn { color: var(--warn); }
  .log time { flex: none; font-size: var(--t-small); white-space: nowrap; }
</style>
