<script>
  import { api } from './api.js';
  import { widgets } from './shapes/index.js';
  import { formatPct, formatBytes, containerStatusClass } from './format.js';

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

<article class="card service">
  <header>
    <h3 class="ellipsis" title={service.name}>{service.name}</h3>
    <span class="muted type ellipsis" title={adapter?.name ?? service.type}>{adapter?.name ?? service.type}</span>
  </header>
  <p class="muted url ellipsis" title={service.url}>{host}</p>

  {#if container}
    <p class="muted container-line" title={containerTitle}>
      {#if container.state === ''}
        <span class="ellipsis">container not found</span>
      {:else}
        <span class="dot" class:ok={containerDot === 'ok'} class:warn={containerDot === 'warn'}
          class:fail={containerDot === 'fail'} aria-hidden="true"></span>
        <span class="ellipsis state">{container.state}{container.health ? ` (${container.health})` : ''}</span>
        <span>{container.state === 'running' ? formatPct(container.cpuPercent) : '—'}</span>
        <span>{container.state === 'running' ? formatBytes(container.memBytes) : '—'}</span>
        <span>{container.restarts} restart{container.restarts === 1 ? '' : 's'}</span>
      {/if}
    </p>
  {/if}

  {#each service.kinds as kind (kind)}
    {#if widgets[kind]}
      {@const Widget = widgets[kind]}
      <Widget reading={service.readings?.[kind]} />
    {/if}
  {/each}

  <div class="actions">
    <button type="button" class="quiet" aria-label={`Edit ${service.name}`} onclick={() => onedit?.(service)}>Edit</button>
    <button type="button" class="quiet" aria-label={`Remove ${service.name}`} onclick={openRemove}>Remove</button>
  </div>
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
  .card.service { display: grid; gap: 10px; min-width: 0; }
  header { display: flex; align-items: baseline; gap: 8px; min-width: 0; }
  header h3 { margin: 0; font-size: 16px; min-width: 0; }
  .type { margin-left: auto; flex-shrink: 0; }
  .url { margin: 0; font-size: 13px; }
  .container-line { margin: 0; font-size: 13px; display: flex; align-items: center; gap: 8px; min-width: 0; }
  .container-line .dot { width: 8px; height: 8px; border-radius: 50%; flex-shrink: 0; background: var(--muted); }
  .container-line .dot.ok { background: var(--ok); }
  .container-line .dot.warn { background: var(--warn); }
  .container-line .dot.fail { background: var(--fail); }
  .container-line .state { flex-shrink: 1; min-width: 0; }
  .ellipsis { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .actions { display: flex; gap: 8px; justify-content: flex-end; }
  dialog {
    background: var(--surface); color: var(--text); border: 1px solid var(--border); border-radius: 12px;
    padding: 20px; max-width: min(420px, calc(100vw - 32px));
  }
  dialog::backdrop { background: rgba(0, 0, 0, 0.5); }
  dialog h2 { margin: 0 0 8px; font-size: 17px; }
  dialog p { margin: 0 0 12px; }
  dialog .actions { margin-top: 4px; }
</style>
