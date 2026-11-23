<script>
  import { api } from './api.js';
  import { widgets } from './shapes/index.js';
  import { formatPct, formatBytes, containerStatusClass } from './format.js';
  import { serviceStatus } from './status.js';
  import Icon from './icons/Icon.svelte';

  // ServiceCard renders one service: its identity (name, adapter, URL host) and a widget for
  // each shape its adapter declares. It never names an app type — `adapter` is just the matching
  // entry from GET /api/adapters, looked up by the caller. `onremoved` is called once the
  // service is actually gone, so the caller can move focus somewhere that outlives this card.
  let { service, adapter, onedit, onremoved } = $props();

  let dialogEl;
  let opener = null;
  let removedSuccessfully = false;
  let removeError = $state('');
  let removing = $state(false);

  const host = $derived(hostOf(service.url));
  const status = $derived(serviceStatus(service));
  const container = $derived(service.container);
  const containerDot = $derived(container ? containerStatusClass(container.state, container.health) : '');
  const containerTitle = $derived(
    !container ? '' :
      container.state === '' ? `${container.name}: container not found` :
      `${container.name}: ${container.state}${container.health ? ` (${container.health})` : ''}`,
  );

  function hostOf(url) {
    try {
      return new URL(url).host;
    } catch {
      return url;
    }
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

<article class="card service" id={`service-${service.id}`} tabindex="-1" aria-labelledby={`service-name-${service.id}`}>
  <header>
    <span class="tile-icon" aria-hidden="true">
      <Icon name={adapter?.icon} />
      <span class="status-dot {status}"></span>
    </span>
    <div class="ident">
      <h2 id={`service-name-${service.id}`} class="ellipsis" title={service.name}>{service.name}</h2>
      <p class="muted ellipsis" title={`${adapter?.name ?? service.type} · ${service.url}`}>
        {adapter?.name ?? service.type} · {host}
      </p>
    </div>
    <button type="button" class="ibtn" aria-label={`Edit ${service.name}`} title="Edit" onclick={() => onedit?.(service)}>
      <Icon name="pencil" size={16} />
    </button>
    <button type="button" class="ibtn danger" aria-label={`Remove ${service.name}`} title="Remove" onclick={openRemove}>
      <Icon name="trash-2" size={16} />
    </button>
  </header>

  {#if container}
    <p class="container-line" title={containerTitle}>
      <Icon name="box" size={14} />
      {#if container.state === ''}
        <span class="ellipsis">container not found</span>
      {:else}
        <span class="dot {containerDot}" aria-hidden="true"></span>
        <span class="ellipsis state">{container.state}{container.health ? ` (${container.health})` : ''}</span>
        <span class="num">{container.state === 'running' ? formatPct(container.cpuPercent) : '—'}</span>
        <span class="num">{container.state === 'running' ? formatBytes(container.memBytes) : '—'}</span>
        <span class="num">{container.restarts} restart{container.restarts === 1 ? '' : 's'}</span>
      {/if}
    </p>
  {/if}

  {#each service.kinds as kind (kind)}
    {#if widgets[kind]}
      {@const Widget = widgets[kind]}
      <Widget reading={service.readings?.[kind]} />
    {/if}
  {/each}
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
  .service { display: grid; gap: 14px; align-content: start; scroll-margin-top: 16px; }
  .service:focus { outline: none; }
  .service:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
  header { display: flex; align-items: center; gap: 12px; min-width: 0; }
  header .tile-icon { border: 1px solid var(--border); }
  header .status-dot { border-color: var(--surface); }
  .ident { display: grid; min-width: 0; flex: 1; line-height: 1.35; }
  h2 { font-size: 15px; font-weight: 600; letter-spacing: -0.01em; }
  .ident p { font-size: 12.5px; }
  header .ibtn { border-color: transparent; }
  .container-line {
    display: flex; align-items: center; gap: 8px; min-width: 0; padding: 8px 10px; border-radius: var(--r-sm);
    background: var(--surface2); font-size: 12.5px; color: var(--text2);
  }
  .container-line :global(.icon) { color: var(--muted); }
  .container-line .state { flex: 1 1 auto; }
  .container-line .num { flex: none; color: var(--muted); }
  dialog p { margin: 8px 0 14px; }
</style>
