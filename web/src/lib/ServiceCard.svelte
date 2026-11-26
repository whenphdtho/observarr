<script>
  import AppIcon from './AppIcon.svelte';
  import { reveal, conceal, flash } from './motion.js';
  import { api } from './api.js';
  import { widgets } from './shapes/index.js';
  import { formatPct, formatBytes, formatPercent, containerStatusClass } from './format.js';
  import { serviceStatus, cardSummary } from './status.js';
  import { attention } from './live.js';
  import { hostOf } from './urls.js';
  import Icon from './icons/Icon.svelte';

  // ServiceCard renders one service. Collapsed, it always has the same rows — header, one summary
  // line and one lead row — so the cards of a grid row line up. Expanded, it shows a widget for
  // each shape its adapter declares, the linked container, and Edit/Remove. It never names an app
  // type — `adapter` is just the matching entry from GET /api/adapters, looked up by the caller.
  // `onremoved` is called once the service is actually gone, so the caller can move focus somewhere
  // that outlives this card.
  let { service, adapter, expanded = false, ontoggle, onedit, onremoved } = $props();

  let dialogEl;
  let opener = null;
  let removedSuccessfully = false;
  let removeError = $state('');
  let removing = $state(false);

  const host = $derived(hostOf(service.url));
  const status = $derived(serviceStatus(service, $attention));
  const health = $derived(service.readings?.health ?? null);
  const version = $derived(health?.data?.version ?? '');
  const card = $derived(cardSummary(service, $attention));
  const lead = $derived(card.lead);
  const detailsId = $derived(`service-details-${service.id}`);
  const container = $derived(service.container);
  const containerDot = $derived(container ? containerStatusClass(container.state, container.health) : '');
  const containerTitle = $derived(
    !container ? '' :
      container.state === '' ? `${container.name}: container not found` :
      `${container.name}: ${container.state}${container.health ? ` (${container.health})` : ''}`,
  );
  const pill = $derived(!health ? { text: 'Checking', tone: '' } : service.up ? { text: 'Up', tone: 'ok' } : { text: 'Down', tone: 'fail' });

  function warningText(w) {
    return w.source ? `${w.source}: ${w.message}` : w.message;
  }

  function openRemove() {
    removeError = '';
    opener = document.activeElement;
    dialogEl?.showModal();
  }

  function handleDialogClose() {
    // On a successful removal onremoved() already ran (right after the close() call below, since
    // the SSE "removed" event — and this card's unmount — often arrives before this 'close' event
    // does). Cancel/Esc restore focus to the opener as usual, since nothing changed.
    if (!removedSuccessfully) opener?.focus();
    opener = null;
    removedSuccessfully = false;
  }

  // handleCancel blocks Esc from closing the dialog while a removal is in flight, so the request
  // can't be left to resolve against an already-closed, focus-restored dialog.
  function handleCancel(event) {
    if (removing) event.preventDefault();
  }

  async function confirmRemove() {
    removing = true;
    removeError = '';
    try {
      await api('DELETE', `/api/services/${service.id}`);
      removedSuccessfully = true;
      dialogEl?.close();
      onremoved?.();
    } catch (err) {
      removeError = err.message;
    } finally {
      removing = false;
    }
  }
</script>

<article class="card service {status === 'fail' || status === 'warn' ? status : ''}" class:open={expanded} id={`service-${service.id}`} tabindex="-1"
  aria-labelledby={`service-name-${service.id}`} use:flash={status}>
  <header>
    <span class="tile-icon" aria-hidden="true"><AppIcon {adapter} size={20} /></span>
    <div class="ident">
      <h2 id={`service-name-${service.id}`} class="ellipsis" title={service.name}>{service.name}</h2>
      <p class="muted ellipsis" title={`${adapter?.name ?? service.type} · ${service.url}`}>
        {adapter?.name ?? service.type} · {host}
      </p>
    </div>
    <div class="badges">
      <span class="state-word" class:fail-text={pill.tone === 'fail'}><span class="dot {status}" aria-hidden="true"></span>{pill.text}</span>
      {#if version}<span class="version muted ellipsis" title={`version ${version}`}>v{version}</span>{/if}
    </div>
  </header>

  <p class="summary ellipsis {card.summary.tone}" title={card.summary.text}>{card.summary.text}</p>

  {#if !expanded}
    <div class="lead">
      {#if lead.kind === 'item'}
        <div class="lead-head">
          <span class="ellipsis lead-title" title={lead.item.title}>{lead.item.title}</span>
          <span class="num pct">{formatPercent(lead.item.progress)}</span>
        </div>
        <div class="bar" class:warn={!!lead.item.problem} role="progressbar" aria-valuemin="0" aria-valuemax="100"
          aria-valuenow={Math.round(lead.item.progress * 100)}
          aria-label={`${lead.item.title}: ${formatPercent(lead.item.progress)} done`}>
          <i style:width={formatPercent(lead.item.progress)}></i>
        </div>
      {:else if lead.kind === 'warning'}
        <p class="lead-warning level-{lead.warning.level}" title={warningText(lead.warning)}>
          <Icon name="triangle-alert" size={14} />
          <span class="visually-hidden">{lead.warning.level === 'error' ? 'Error' : 'Warning'}: </span>
          <span class="ellipsis">{warningText(lead.warning)}</span>
        </p>
      {:else}
        <p class="lead-note" class:ellipsis={lead.tone !== 'fail'} class:fail={lead.tone === 'fail'} title={lead.text}>{lead.text}</p>
      {/if}
    </div>
  {/if}

  {#if expanded}
    <div class="details" id={detailsId} in:reveal out:conceal>
      {#if container}
        <p class="container-line" title={containerTitle}>
          <Icon name="box" size={14} />
          {#if container.state === ''}
            <span class="ellipsis">{container.name}: container not found</span>
          {:else}
            <span class="dot {containerDot}" aria-hidden="true"></span>
            <span class="ellipsis state">{container.name} · {container.state}{container.health ? ` (${container.health})` : ''}</span>
            <span class="num">{container.state === 'running' ? formatPct(container.cpuPercent) : '—'}</span>
            <span class="num">{container.state === 'running' ? formatBytes(container.memBytes) : '—'}</span>
            <span class="num">{container.restarts} restart{container.restarts === 1 ? '' : 's'}</span>
          {/if}
        </p>
      {/if}
      <div class="widgets">
        {#each service.kinds as kind (kind)}
          {#if widgets[kind]}
            {@const Widget = widgets[kind]}
            <div class="widget"><Widget reading={service.readings?.[kind]} /></div>
          {/if}
        {/each}
      </div>
    </div>
  {/if}

  <footer>
    <button type="button" class="quiet toggle" aria-expanded={expanded} aria-controls={expanded ? detailsId : undefined}
      onclick={() => ontoggle?.(service.id)}>
      <Icon name="chevron-right" size={15} />
      {expanded ? 'Hide details' : 'Show details'}
      <span class="visually-hidden">of {service.name}</span>
    </button>
    {#if expanded}
      <span class="manage">
        <button type="button" class="quiet" onclick={() => onedit?.(service)}>
          <Icon name="pencil" size={14} />Edit<span class="visually-hidden"> {service.name}</span>
        </button>
        <button type="button" class="quiet danger" onclick={openRemove}>
          <Icon name="trash-2" size={14} />Remove<span class="visually-hidden"> {service.name}</span>
        </button>
      </span>
    {/if}
  </footer>
</article>

<dialog bind:this={dialogEl} aria-labelledby={`remove-title-${service.id}`}
  onclose={handleDialogClose} oncancel={handleCancel}>
  <h2 id={`remove-title-${service.id}`} class="ellipsis" title={service.name}>Remove {service.name}?</h2>
  <p class="muted">This stops monitoring it. You can add it again later.</p>
  {#if removeError}<p class="error" role="alert">{removeError}</p>{/if}
  <div class="actions">
    <button type="button" class="quiet" disabled={removing} onclick={() => dialogEl?.close()}>Cancel</button>
    <button type="button" onclick={confirmRemove} disabled={removing}>Remove</button>
  </div>
</dialog>

<style>
  .service {
    display: grid; grid-template-rows: auto auto 1fr auto; gap: 10px; scroll-margin-top: 16px; padding: 14px 16px 12px;
    transition: border-color var(--dur-base) var(--ease-out);
  }
  .service.open { grid-column: 1 / -1; grid-template-rows: none; }
  .service:focus { outline: none; }
  .service:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
  /* A card with a problem is edged in its state's colour; a healthy one keeps the quiet edge. */
  .service.fail { border-color: var(--fail-edge); }
  .service.warn { border-color: var(--warn-edge); }
  header { display: flex; align-items: center; gap: 12px; min-width: 0; }
  .ident { display: grid; min-width: 0; flex: 1; line-height: 1.35; }
  h2 { font-size: var(--t-h3); letter-spacing: 0; }
  .ident p { font-size: var(--t-small); }
  /* State and version sit on the name's line. */
  .badges {
    display: flex; align-items: baseline; gap: 10px; min-width: 0; flex: 0 1 auto; align-self: flex-start;
    padding-top: 2px; font-size: var(--t-small);
  }
  .badges .state-word { flex: none; color: var(--text2); }
  .badges .state-word.fail-text { color: var(--fail); }
  .badges .version { flex: 0 1 auto; max-width: 110px; }

  .summary { font-size: var(--t-body); color: var(--text2); }
  .summary.warn { color: var(--warn); }
  .summary.fail { color: var(--fail); }

  /* The lead row has one fixed height, whatever it holds, so collapsed cards line up. */
  .lead { height: 40px; display: grid; align-content: center; gap: 7px; min-width: 0; }
  .lead-head { display: flex; align-items: baseline; gap: 12px; min-width: 0; }
  .lead-title { flex: 1; font-size: var(--t-body); }
  .pct { flex: none; font-size: var(--t-small); color: var(--text2); }
  .lead-warning { display: flex; align-items: center; gap: 8px; min-width: 0; font-size: var(--t-small); color: var(--text2); }
  .lead-warning :global(.icon) { color: var(--warn); }
  .lead-warning.level-error :global(.icon) { color: var(--fail); }
  .lead-note { font-size: var(--t-small); color: var(--muted); }
  /* An error keeps up to two lines of the fixed-height row; its full text is in the title. */
  .lead-note.fail {
    color: var(--fail); overflow-wrap: anywhere; overflow: hidden;
    display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; line-clamp: 2; line-height: 1.4;
  }

  .details { display: grid; gap: 12px; min-width: 0; }
  .widgets { display: grid; gap: 4px 32px; grid-template-columns: repeat(auto-fit, minmax(min(100%, 300px), 1fr)); align-items: start; }
  .widget { min-width: 0; }
  .container-line { display: flex; align-items: center; gap: 8px; min-width: 0; font-size: var(--t-small); color: var(--text2); }
  .container-line :global(.icon) { color: var(--muted); }
  .container-line .state { flex: 1 1 auto; }
  .container-line .num { flex: none; color: var(--muted); }

  footer {
    display: flex; align-items: center; gap: 8px; flex-wrap: wrap; min-width: 0;
    padding-top: 10px; border-top: 1px solid var(--border);
  }
  .toggle { padding: 5px 11px 5px 7px; gap: 4px; }
  .toggle :global(.icon) { color: var(--text2); transition: transform var(--dur-base) var(--ease-out); }
  .toggle[aria-expanded='true'] :global(.icon) { transform: rotate(90deg); }
  .manage { display: flex; gap: 8px; margin-left: auto; }
  .manage button { padding: 5px 11px; }
  .manage .danger:hover:not(:disabled) { color: var(--fail); }
  dialog p { margin: 8px 0 14px; }

  @container (max-width: 400px) {
    .badges .version { display: none; }
  }
</style>
