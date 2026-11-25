<script>
  import ServiceCard from './ServiceCard.svelte';
  import EditService from './EditService.svelte';
  import FoundList from './FoundList.svelte';
  import Icon from './icons/Icon.svelte';
  import BrandMark from './BrandMark.svelte';
  import { adapters, adaptersError, adapterFor, snapshot } from './data.js';
  import { lastEvent } from './live.js';

  // Apps lists every service as a card and holds the Edit dialog. services is the
  // (search-filtered) list; filtered says whether a search is narrowing it. onadd opens the Add
  // apps view. With no services yet, it shows an onboarding card that lists the apps found on
  // this server right away; that card stays until the user is done with it, so adding the first
  // app doesn't hide the others found.
  let { services, filtered = false, onadd } = $props();

  const STORAGE_KEY = 'observarr.apps.expanded';

  // The Edit dialog's optional "Container" select only needs a roughly current list.
  const containers = $derived($snapshot?.containers ?? []);

  let editOpen = $state(false);
  let editing = $state(null);
  let expanded = $state(loadExpanded());
  let onboarding = $state(false);
  let addButtonEl;

  const allExpanded = $derived(services.length > 0 && services.every((s) => expanded[s.id]));
  const taken = $derived(services.map((s) => s.name));

  $effect(() => {
    // Only once the live stream has said so: before its first event the list is empty anyway.
    if ($lastEvent > 0 && services.length === 0 && !filtered) onboarding = true;
  });

  // The open/closed state of each card is remembered in this browser only; storage may be
  // unavailable (private mode, blocked site data), and the cards then just start collapsed.
  function loadExpanded() {
    try {
      const ids = JSON.parse(localStorage.getItem(STORAGE_KEY) ?? '[]');
      return Array.isArray(ids) ? Object.fromEntries(ids.map((id) => [id, true])) : {};
    } catch {
      return {};
    }
  }

  function saveExpanded() {
    try {
      const known = new Set(services.map((s) => String(s.id)));
      const ids = Object.keys(expanded).filter((id) => expanded[id] && (filtered || known.has(id))).map(Number);
      localStorage.setItem(STORAGE_KEY, JSON.stringify(ids));
    } catch {
      // Not remembered this time; nothing else depends on it.
    }
  }

  function toggle(id) {
    expanded = { ...expanded, [id]: !expanded[id] };
    saveExpanded();
  }

  function toggleAll() {
    const next = !allExpanded;
    expanded = { ...expanded, ...Object.fromEntries(services.map((s) => [s.id, next])) };
    saveExpanded();
  }

  // focusAddButton is where focus goes after a card removes itself: the card's own Remove
  // button is gone by then, so focus needs a target that is guaranteed to still be there.
  function focusAddButton() {
    addButtonEl?.focus();
  }

  function openEdit(service) {
    editing = service;
    editOpen = true;
  }

  function foundEmpty() {
    if (services.length > 0) onboarding = false;
  }
</script>

<section class="apps" aria-label="Services">
  <div class="toolbar">
    <p class="count">{services.length} service{services.length === 1 ? '' : 's'}{filtered ? ' match' : ''}</p>
    {#if services.length > 0}
      <button type="button" class="quiet" onclick={toggleAll}>
        {allExpanded ? 'Collapse all' : 'Expand all'}
      </button>
    {/if}
    <button type="button" bind:this={addButtonEl} onclick={onadd}>
      <Icon name="plus" size={15} />Add apps
    </button>
  </div>

  {#if $adaptersError}
    <p class="error" role="alert">{$adaptersError}</p>
  {/if}

  {#if onboarding && !filtered}
    <section class="card onboarding" aria-labelledby="onboarding-title">
      <div class="intro">
        <span class="intro-mark"><BrandMark size={36} /></span>
        <div class="intro-text">
          <h2 id="onboarding-title">{services.length === 0 ? 'Add your apps' : 'Add the rest of your apps'}</h2>
          <p class="muted">
            These are the apps Observarr can see on this server. Nothing is contacted until you add one — then
            Observarr checks it and starts watching its health and queue.
          </p>
        </div>
        {#if services.length > 0}
          <button type="button" class="quiet done" onclick={() => { onboarding = false; }}>Done</button>
        {/if}
      </div>
      <FoundList adapters={$adapters} {taken} onempty={foundEmpty} />
      <p class="more">
        <button type="button" class="link" onclick={onadd}>Scan your network or add an app by hand</button>
      </p>
    </section>
  {/if}

  {#if services.length === 0}
    {#if filtered}
      <div class="empty">
        <Icon name="search" size={28} />
        <b>No service matches the search</b>
        <span>Try part of a name, an app or an address — or press Esc to clear it.</span>
      </div>
    {/if}
  {:else}
    <div class="grid">
      {#each services as service (service.id)}
        <ServiceCard {service} adapter={adapterFor($adapters, service.type)} expanded={!!expanded[service.id]}
          ontoggle={toggle} onedit={openEdit} onremoved={focusAddButton} />
      {/each}
    </div>
  {/if}
</section>

<EditService bind:open={editOpen} service={editing} adapters={$adapters} {containers} />

<style>
  .apps { display: grid; gap: 16px; }
  .toolbar { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .count { margin-right: auto; font-size: var(--t-body); font-weight: 600; }
  /* Cards of one row share its height; an expanded card takes a whole row of its own. */
  .grid { display: grid; gap: 16px; grid-template-columns: repeat(auto-fill, minmax(min(100%, 320px), 1fr)); }

  .onboarding { display: grid; gap: 12px; padding: 22px 22px 16px; }
  .intro { display: flex; gap: 14px; align-items: flex-start; min-width: 0; }
  .intro-text { display: grid; gap: 4px; min-width: 0; flex: 1; }
  .intro p { font-size: var(--t-body); max-width: 62ch; }
  .done { flex: none; }
  .more { border-top: 1px solid var(--border); padding-top: 12px; }
  .link {
    padding: 0; background: transparent; border: 0; color: var(--accent-text); font-weight: 500;
    white-space: normal; text-align: left;
  }
  .link:hover:not(:disabled) { background: transparent; border: 0; text-decoration: underline; }

  @container (max-width: 400px) {
    .count { flex-basis: 100%; }
    .onboarding { padding: 16px 16px 12px; }
    .intro-mark { display: none; }
  }
</style>
