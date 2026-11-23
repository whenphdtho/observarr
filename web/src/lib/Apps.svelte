<script>
  import ServiceCard from './ServiceCard.svelte';
  import AddService from './AddService.svelte';
  import Discover from './Discover.svelte';
  import Icon from './icons/Icon.svelte';
  import { adapters, adaptersError, adapterFor, snapshot } from './data.js';

  // Apps lists every service as a card and holds the Add, Edit and Discover dialogs. services is
  // the (search-filtered) list; filtered says whether a search is narrowing it.
  let { services, filtered = false } = $props();

  // The dialogs' optional "Container" select only needs a roughly current list; the snapshot
  // the live stream keeps current is good enough.
  const containers = $derived($snapshot?.containers ?? []);

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

<section class="apps" aria-label="Services">
  <div class="toolbar">
    <p class="count">{services.length} service{services.length === 1 ? '' : 's'}{filtered ? ' match' : ''}</p>
    <button type="button" class="quiet" onclick={() => { discoverOpen = true; }}>
      <Icon name="radar" size={15} />Discover
    </button>
    <button type="button" bind:this={addButtonEl} onclick={openAdd}>
      <Icon name="plus" size={15} />Add service
    </button>
  </div>

  {#if $adaptersError}
    <p class="error" role="alert">{$adaptersError}</p>
  {/if}

  {#if services.length === 0}
    <div class="empty">
      <Icon name={filtered ? 'search' : 'layout-grid'} size={28} />
      {#if filtered}
        <b>No service matches the search</b>
      {:else}
        <b>No services yet</b>
        <span>Add your first app, or let Discover find them.</span>
        <button type="button" onclick={openAdd}><Icon name="plus" size={15} />Add service</button>
      {/if}
    </div>
  {:else}
    <div class="grid">
      {#each services as service (service.id)}
        <ServiceCard {service} adapter={adapterFor($adapters, service.type)} onedit={openEdit} onremoved={focusAddButton} />
      {/each}
    </div>
  {/if}
</section>

<AddService bind:open={dialogOpen} mode={dialogMode} service={editing} adapters={$adapters} {containers} />
<Discover bind:open={discoverOpen} adapters={$adapters} {containers} />

<style>
  .apps { display: grid; gap: 16px; }
  .toolbar { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .count { margin-right: auto; font-size: 13.5px; font-weight: 600; }
  .grid {
    display: grid; gap: 16px; grid-template-columns: repeat(auto-fill, minmax(min(100%, 300px), 1fr));
    /* Each card keeps its own height instead of stretching to the tallest card in its row. */
    align-items: start;
  }
  @container (max-width: 400px) {
    .count { flex-basis: 100%; }
  }
</style>
