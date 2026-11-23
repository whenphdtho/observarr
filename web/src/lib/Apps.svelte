<script>
  import { onMount } from 'svelte';
  import { api } from './api.js';
  import { serviceList, reconnecting } from './live.js';
  import ServiceCard from './ServiceCard.svelte';
  import AddService from './AddService.svelte';
  import Discover from './Discover.svelte';

  let adapters = $state([]);
  let adaptersError = $state('');
  let containers = $state([]);

  let dialogOpen = $state(false);
  let dialogMode = $state('add');
  let editing = $state(null);

  let discoverOpen = $state(false);

  let addButtonEl;

  // focusAddButton is where focus goes after a card removes itself: the card's own Remove
  // button is gone by then, so focus needs a target that is guaranteed to still be there.
  function focusAddButton() {
    addButtonEl?.focus();
  }

  let wasReconnecting = false;

  onMount(() => {
    loadAdapters();
    loadContainers();
  });

  // Once a reconnect finishes, the adapters and containers lists (and any error left over from
  // before the drop) may be stale — e.g. the server restarted with a different set of adapters, or
  // Docker only became reachable after it came back. Reload both and drop any stale error so the
  // view reflects what's current now, not what failed before the drop.
  $effect(() => {
    const r = $reconnecting;
    if (wasReconnecting && !r) {
      adaptersError = '';
      loadAdapters();
      loadContainers();
    }
    wasReconnecting = r;
  });

  async function loadAdapters() {
    try {
      adapters = await api('GET', '/api/adapters');
    } catch (err) {
      adaptersError = err.message;
    }
  }

  // loadContainers feeds AddService's optional "Container" select. It is loaded once: the list
  // only needs to be roughly current when the dialog opens, and the live stream (opened by Shell)
  // already keeps every linked service's own container brief current on its card.
  async function loadContainers() {
    try {
      const snap = await api('GET', '/api/system');
      containers = snap.containers ?? [];
    } catch {
      // Docker may simply not be configured; the select then just offers "No container".
    }
  }

  function adapterFor(type) {
    return adapters.find((a) => a.type === type) ?? null;
  }

  function openAdd() {
    dialogMode = 'add';
    editing = null;
    dialogOpen = true;
  }

  function openEdit(service) {
    dialogMode = 'edit';
    editing = service;
    dialogOpen = true;
  }
</script>

<section class="apps">
  <div class="apps-head">
    <h2>Apps</h2>
    <div class="apps-head-actions">
      <button bind:this={addButtonEl} onclick={openAdd}>Add service</button>
      <button type="button" class="quiet" onclick={() => { discoverOpen = true; }}>Discover</button>
    </div>
  </div>

  {#if adaptersError}
    <p class="error" role="alert">{adaptersError}</p>
  {/if}

  {#if $reconnecting}
    <p class="muted" role="status">Reconnecting…</p>
  {/if}

  {#if $serviceList.length === 0}
    <div class="card empty">
      <p>No services yet — add your first app.</p>
      <button onclick={openAdd}>Add service</button>
    </div>
  {:else}
    <div class="grid">
      {#each $serviceList as service (service.id)}
        <ServiceCard {service} adapter={adapterFor(service.type)} onedit={openEdit} onremoved={focusAddButton} />
      {/each}
    </div>
  {/if}
</section>

<AddService bind:open={dialogOpen} mode={dialogMode} service={editing} {adapters} {containers} />
<Discover bind:open={discoverOpen} {adapters} {containers} />

<style>
  .apps { display: grid; gap: 16px; }
  .apps-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; flex-wrap: wrap; }
  .apps-head h2 { margin: 0; font-size: 18px; }
  .apps-head-actions { display: flex; gap: 8px; flex-wrap: wrap; }
  .empty { display: grid; gap: 14px; justify-items: start; }
  .empty p { margin: 0; }
  .grid {
    display: grid; gap: 16px;
    grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
  }
  @media (max-width: 699px) {
    .grid { grid-template-columns: 1fr; }
  }
</style>
