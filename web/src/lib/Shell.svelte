<script>
  import { onMount, tick } from 'svelte';
  import Overview from './Overview.svelte';
  import Network from './Network.svelte';
  import Apps from './Apps.svelte';
  import System from './System.svelte';
  import Rail from './Rail.svelte';
  import Icon from './icons/Icon.svelte';
  import { live, serviceList, reconnecting, lastEvent } from './live.js';
  import { loadAdapters, loadSystem } from './data.js';
  import { matches } from './status.js';
  import { now } from './ticker.js';
  import { formatAge } from './format.js';

  let { user, onlogout } = $props();

  const VIEWS = [
    { id: 'overview', hash: '', label: 'Overview', icon: 'house', sub: 'Your apps and host at a glance' },
    { id: 'network', hash: '#/network', label: 'Network', icon: 'network', sub: 'Throughput, traffic by app and budget' },
    { id: 'apps', hash: '#/apps', label: 'Apps', icon: 'layout-grid', sub: 'Health and queue of every service' },
    { id: 'system', hash: '#/system', label: 'System', icon: 'server', sub: 'Host, disks and containers' },
  ];

  // The view is remembered in the URL hash (#/network, #/apps, #/system), so a reload or a shared link keeps it.
  function viewFromHash() {
    return VIEWS.find((v) => v.hash && v.hash === window.location.hash)?.id ?? 'overview';
  }

  let view = $state(viewFromHash());
  let query = $state('');
  let searchEl;
  let mainEl;

  const current = $derived(VIEWS.find((v) => v.id === view));
  const services = $derived($serviceList.filter((s) => matches(query, s.name, s.type, s.url)));
  const age = $derived($lastEvent ? ($now - $lastEvent < 10000 ? 'just now' : formatAge($lastEvent, $now)) : '');

  function select(id) {
    if (view === id) return;
    view = id;
    const hash = VIEWS.find((v) => v.id === id).hash;
    history.replaceState(null, '', window.location.pathname + window.location.search + hash);
    mainEl?.scrollTo(0, 0);
    window.scrollTo(0, 0);
  }

  // jumpTo shows a service's card on the Apps view and moves focus to it.
  async function jumpTo(id) {
    select('apps');
    await tick();
    const card = document.getElementById(`service-${id}`);
    if (!card) return;
    const smooth = !matchMedia('(prefers-reduced-motion: reduce)').matches;
    card.scrollIntoView({ block: 'start', behavior: smooth ? 'smooth' : 'auto' });
    card.focus({ preventScroll: true });
  }

  // "/" focuses the search field, unless the user is typing somewhere or a dialog is open.
  function onKeydown(event) {
    if (event.key !== '/' || event.ctrlKey || event.metaKey || event.altKey) return;
    if (event.target.closest?.('input, textarea, select, [contenteditable]')) return;
    if (document.querySelector('dialog[open]')) return;
    event.preventDefault();
    searchEl?.focus();
  }

  function onSearchKeydown(event) {
    if (event.key === 'Escape' && query) query = '';
  }

  // Once a reconnect finishes, the adapters and the system figures may be stale (the server may
  // have restarted with other adapters, or Docker only became reachable since): load both again.
  let wasReconnecting = false;
  $effect(() => {
    const r = $reconnecting;
    if (wasReconnecting && !r) {
      loadAdapters();
      loadSystem();
    }
    wasReconnecting = r;
  });

  onMount(() => {
    // One stream for the whole app; the server also samples the host faster while it is open.
    live.open();
    loadAdapters();
    loadSystem();
    const onHashChange = () => { view = viewFromHash(); };
    window.addEventListener('hashchange', onHashChange);
    return () => window.removeEventListener('hashchange', onHashChange);
  });
</script>

<svelte:window onkeydown={onKeydown} />

<div class="app">
  <aside class="left">
    <div class="brand">
      <span class="mark" aria-hidden="true">
        <svg viewBox="0 0 24 24" width="18" height="18">
          <path d="M3.5 12c2.3-3.9 5.1-5.8 8.5-5.8s6.2 1.9 8.5 5.8c-2.3 3.9-5.1 5.8-8.5 5.8S5.8 15.9 3.5 12z" />
          <circle cx="12" cy="12" r="2.6" />
        </svg>
      </span>
      Observarr
    </div>

    <div class="search">
      <Icon name="search" size={16} />
      <input bind:this={searchEl} bind:value={query} type="search" placeholder="Search…"
        aria-label="Search services and containers" aria-keyshortcuts="/" onkeydown={onSearchKeydown}
        autocomplete="off" spellcheck="false" />
      <kbd aria-hidden="true">/</kbd>
    </div>

    <nav aria-label="Views">
      <p class="cap" aria-hidden="true">Views</p>
      {#each VIEWS as v (v.id)}
        <a class="vbtn" href={v.hash || '#/'} aria-current={view === v.id ? 'page' : undefined}
          onclick={(e) => { e.preventDefault(); select(v.id); }}>
          <Icon name={v.icon} />
          {v.label}
        </a>
      {/each}
    </nav>

    <div class="acct">
      <span class="av" aria-hidden="true">{user.slice(0, 1).toUpperCase()}</span>
      <span class="nm">
        <span class="ellipsis" title={user}>{user}</span>
        <span class="muted">signed in</span>
      </span>
      <button type="button" class="ibtn" onclick={onlogout} aria-label="Sign out" title={`Sign out ${user}`}>
        <Icon name="log-out" size={17} />
      </button>
    </div>
  </aside>

  <main class="center" bind:this={mainEl}>
    <div class="wrap">
      <header class="top">
        <div class="titles">
          <h1>{current.label}</h1>
          <p class="sub ellipsis" title={current.sub}>{current.sub}</p>
        </div>
        <p class="live" class:stale={$reconnecting} role="status">
          <i aria-hidden="true"></i>
          <span>{$reconnecting ? 'Reconnecting…' : 'Live'}</span>
          {#if age && !$reconnecting}<span class="ago" aria-hidden="true">· {age}</span>{/if}
        </p>
      </header>

      {#if view === 'overview'}
        <Overview {services} onjump={jumpTo} />
      {:else if view === 'network'}
        <Network />
      {:else if view === 'apps'}
        <Apps {services} filtered={query.trim() !== ''} />
      {:else}
        <System {query} />
      {/if}
    </div>
  </main>

  <aside class="right" aria-label="Summary">
    <Rail {services} onjump={jumpTo} />
  </aside>
</div>

<style>
  .app {
    display: grid; grid-template-columns: 240px minmax(0, 1fr) 300px; height: 100vh; height: 100dvh; overflow: hidden;
  }
  .app > * { min-height: 0; height: 100%; }
  .left, .right { background: var(--rail); overflow: hidden auto; display: flex; flex-direction: column; }
  .left { border-right: 1px solid var(--border); padding: 22px 14px 16px; }
  .right { border-left: 1px solid var(--border); padding: 22px 20px 16px; }
  .center { overflow-y: auto; overflow-x: hidden; min-width: 0; scrollbar-gutter: stable; }
  .wrap { max-width: 1160px; margin: 0 auto; padding: 24px 32px 48px; container-type: inline-size; }

  .brand {
    display: flex; align-items: center; gap: 11px; padding: 0 10px 22px;
    font-size: 16px; font-weight: 650; letter-spacing: -0.01em;
  }
  .mark { width: 30px; height: 30px; border-radius: 9px; }

  .search {
    display: flex; align-items: center; gap: 9px; margin-bottom: 18px; padding: 0 11px; color: var(--muted);
    background: var(--surface); border: 1px solid var(--border2); border-radius: var(--r-sm);
  }
  .search:focus-within { border-color: var(--accent); }
  .search input { flex: 1; min-width: 0; padding: 8px 0; border: 0; border-radius: 0; background: transparent; outline: none; }
  .search input::placeholder { color: var(--muted); }
  .search input::-webkit-search-cancel-button { display: none; }
  .search kbd {
    font: 550 11px 'Inter', sans-serif; color: var(--muted); border: 1px solid var(--border2); border-radius: 5px; padding: 0 5px;
  }

  .cap { padding: 4px 10px 8px; font-size: 12.5px; color: var(--muted); font-weight: 550; }
  .vbtn {
    display: flex; align-items: center; gap: 11px; padding: 8px 10px; margin-bottom: 2px; border-radius: var(--r-sm);
    color: var(--text2); font-weight: 500; font-size: 13.5px; text-decoration: none; white-space: nowrap;
    transition: background 140ms var(--ease), color 140ms var(--ease);
  }
  .vbtn:hover { background: var(--surface); color: var(--text); }
  .vbtn[aria-current='page'] { background: var(--surface2); color: var(--text); }
  .vbtn[aria-current='page'] :global(.icon) { color: var(--accent); }

  .acct { margin-top: auto; display: flex; align-items: center; gap: 11px; padding: 12px 10px 0; border-top: 1px solid var(--border); }
  .av {
    width: 32px; height: 32px; border-radius: 9px; background: var(--surface3); display: grid; place-items: center;
    font-weight: 600; font-size: 13px; flex: none;
  }
  .nm { display: grid; min-width: 0; flex: 1; line-height: 1.3; font-weight: 550; }
  .nm .muted { font-size: 12px; font-weight: 400; }
  .acct .ibtn { border-color: transparent; }

  .top { display: flex; align-items: center; gap: 14px; margin-bottom: 22px; min-width: 0; }
  .titles { min-width: 0; flex: 1; }
  h1 { font-size: 21px; font-weight: 620; letter-spacing: -0.015em; }
  .sub { color: var(--muted); font-size: 13px; margin-top: 1px; }
  .live {
    display: inline-flex; align-items: center; gap: 4px; flex: none; padding: 5px 11px; border-radius: 99px;
    border: 1px solid var(--border); font-size: 12.5px; color: var(--text2); white-space: nowrap;
  }
  .live i { width: 7px; height: 7px; border-radius: 50%; background: var(--ok); margin-right: 4px; }
  .live.stale i { background: var(--warn); }

  /* Below 1200px the right column's content moves into the Overview. */
  @media (max-width: 1199px) {
    .app { grid-template-columns: 240px minmax(0, 1fr); }
    .right { display: none; }
  }

  /* Phones: the page scrolls as a whole, the views become a dock at the bottom, and the brand,
     account and search sit at the top of the page. */
  @media (max-width: 720px) {
    .app { display: block; height: auto; overflow: visible; }
    .app > * { height: auto; }
    .center { overflow: visible; scrollbar-gutter: auto; }
    .wrap { padding: 4px 16px 96px; }
    .left {
      display: grid; grid-template-columns: minmax(0, 1fr) auto; grid-template-areas: 'brand acct' 'search search';
      gap: 12px; padding: 14px 16px 10px; border: 0; background: transparent;
    }
    .brand { grid-area: brand; padding: 0; }
    .acct { grid-area: acct; margin: 0; padding: 0; border: 0; }
    .nm, .av { display: none; }
    .search { grid-area: search; margin: 0; }
    .search kbd { display: none; }
    nav {
      position: fixed; z-index: 40; left: 0; right: 0; bottom: 0; display: flex; gap: 4px;
      padding: 6px 8px calc(6px + env(safe-area-inset-bottom)); background: var(--rail); border-top: 1px solid var(--border);
    }
    nav .cap { display: none; }
    .vbtn { flex: 1 1 0; min-width: 0; flex-direction: column; gap: 3px; padding: 7px 2px; margin: 0; font-size: 11px; }
    .vbtn[aria-current='page'] { background: var(--accent-soft); color: var(--accent-text); }
    .vbtn[aria-current='page'] :global(.icon) { color: var(--accent-text); }
    .top { margin: 10px 0 18px; }
    h1 { font-size: 20px; }
  }
  @media (max-width: 400px) {
    .live .ago { display: none; }
  }
</style>
