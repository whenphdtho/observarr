<script>
  import { onMount, tick, untrack } from 'svelte';
  import Overview from './Overview.svelte';
  import Network from './Network.svelte';
  import Apps from './Apps.svelte';
  import AddApps from './AddApps.svelte';
  import System from './System.svelte';
  import Security from './Security.svelte';
  import Storage from './Storage.svelte';
  import Rail from './Rail.svelte';
  import Icon from './icons/Icon.svelte';
  import BrandMark from './BrandMark.svelte';
  import ThemeSwitch from './ThemeSwitch.svelte';
  import { arrive } from './motion.js';
  import { live, serviceList, reconnecting, offline, lastEvent } from './live.js';
  import { loadAdapters, loadSystem } from './data.js';
  import { matches } from './status.js';
  import { now } from './ticker.js';
  import { formatAge } from './format.js';

  let { user, onlogout } = $props();

  const VIEWS = [
    { id: 'overview', hash: '', label: 'Overview', icon: 'house' },
    { id: 'network', hash: '#/network', label: 'Network', icon: 'network' },
    { id: 'apps', hash: '#/apps', label: 'Apps', icon: 'layout-grid' },
    { id: 'system', hash: '#/system', label: 'System', icon: 'server' },
    { id: 'storage', hash: '#/storage', label: 'Storage', icon: 'hard-drive' },
    { id: 'security', hash: '#/security', label: 'Security', icon: 'shield' },
  ];

  // The view is remembered in the URL hash (#/network, #/apps, #/system), so a reload or a shared link keeps it.
  // A view's own pages extend its hash (#/apps/add).
  function viewFromHash() {
    const h = window.location.hash;
    return VIEWS.find((v) => v.hash && (h === v.hash || h.startsWith(v.hash + '/')))?.id ?? 'overview';
  }

  const ADD_APPS = '#/apps/add';
  let view = $state(viewFromHash());
  let hash = $state(window.location.hash);
  const addingApps = $derived(view === 'apps' && hash === ADD_APPS);
  let query = $state('');
  let searchEl;
  let mainEl;
  let navEl;

  const current = $derived(VIEWS.find((v) => v.id === view));
  const services = $derived($serviceList.filter((s) => matches(query, s.name, s.type, s.url)));
  const age = $derived($lastEvent ? ($now - $lastEvent < 10000 ? 'just now' : formatAge($lastEvent, $now)) : '');
  const down = $derived($offline || $reconnecting);

  function select(id) {
    const target = VIEWS.find((v) => v.id === id).hash;
    if (view === id && hash === target) return;
    view = id;
    hash = target;
    history.replaceState(null, '', window.location.pathname + window.location.search + target);
    mainEl?.scrollTo(0, 0);
    window.scrollTo(0, 0);
  }

  // openAddApps opens the Add apps page as a history entry of its own, so Back returns to the cards.
  function openAddApps() {
    history.pushState({ fromApps: true }, '', window.location.pathname + window.location.search + ADD_APPS);
    view = 'apps';
    hash = ADD_APPS;
    mainEl?.scrollTo(0, 0);
    window.scrollTo(0, 0);
  }

  // backToApps returns to the cards: by going back when the page was opened from them, so no second entry of
  // the cards is left in the history, else (e.g. after a reload) in place.
  function backToApps() {
    if (history.state?.fromApps) history.back();
    else select('apps');
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

  // The current view's pill is one element under the links that slides to the current one, in the side column and
  // in the phone dock alike. Until it is placed (and without script) the current link fills itself.
  let pill = $state(null);
  let pillReady = $state(false);
  function placePill() {
    const a = navEl?.querySelector('[aria-current="page"]');
    pill = a ? { x: a.offsetLeft, y: a.offsetTop, w: a.offsetWidth, h: a.offsetHeight } : null;
    // The first place is taken at once; only later moves slide.
    if (a) requestAnimationFrame(() => { pillReady = true; });
  }
  $effect(() => {
    void view;
    untrack(placePill);
  });

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
    const onHashChange = () => {
      view = viewFromHash();
      hash = window.location.hash;
    };
    window.addEventListener('hashchange', onHashChange);
    const ro = new ResizeObserver(placePill);
    ro.observe(navEl);
    return () => {
      window.removeEventListener('hashchange', onHashChange);
      ro.disconnect();
    };
  });
</script>

<svelte:window onkeydown={onKeydown} />

<div class="app">
  <aside class="left">
    <div class="brand">
      <BrandMark size={26} />
      Observarr
    </div>

    <div class="search">
      <Icon name="search" size={16} />
      <input bind:this={searchEl} bind:value={query} type="search" placeholder="Search…"
        aria-label="Search services and containers" aria-keyshortcuts="/" onkeydown={onSearchKeydown}
        autocomplete="off" spellcheck="false" />
      <kbd aria-hidden="true">/</kbd>
    </div>

    <nav aria-label="Views" bind:this={navEl} class:placed={pill !== null}>
      <p class="cap" aria-hidden="true">Views</p>
      {#if pill}
        <span class="pill" class:ready={pillReady} aria-hidden="true" style:width={`${pill.w}px`} style:height={`${pill.h}px`}
          style:transform={`translate(${pill.x}px, ${pill.y}px)`}></span>
      {/if}
      {#each VIEWS as v (v.id)}
        <a class="vbtn" href={v.hash || '#/'} aria-current={view === v.id ? 'page' : undefined} title={v.label}
          onclick={(e) => { e.preventDefault(); select(v.id); }}>
          <Icon name={v.icon} />
          <span class="vlabel">{v.label}</span>
        </a>
      {/each}
    </nav>

    <div class="foot">
      <div class="theme-row">
        <span class="muted" aria-hidden="true">Theme</span>
        <ThemeSwitch />
      </div>
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
    </div>
  </aside>

  <main class="center" bind:this={mainEl}>
    <div class="wrap">
      <header class="top">
        <h1 class="ellipsis">{addingApps ? 'Add apps' : current.label}</h1>
        <p class="live" class:stale={down} role="status">
          <!-- The dot beats once per update; it is rebuilt each time, so there is no running loop. -->
          {#key $lastEvent}<i aria-hidden="true" class:beat={!down && $lastEvent > 0}></i>{/key}
          <span>{$offline ? 'Offline' : $reconnecting ? 'Reconnecting…' : 'Live'}</span>
          <!-- While the stream is down, what is shown is as old as its last update: say so. -->
          {#if age}<span class="ago" class:keep={down} aria-hidden="true">· {#if down}<span class="word">{'updated '}</span>{/if}{age}</span>{/if}
        </p>
      </header>

      {#key addingApps ? 'add' : view}
      <div class="view" in:arrive>
      {#if view === 'overview'}
        <Overview {services} onjump={jumpTo} />
      {:else if view === 'network'}
        <Network />
      {:else if view === 'apps' && addingApps}
        <AddApps onback={backToApps} />
      {:else if view === 'apps'}
        <Apps {services} filtered={query.trim() !== ''} onadd={openAddApps} />
      {:else if view === 'security'}
        <Security />
      {:else if view === 'storage'}
        <Storage services={$serviceList} />
      {:else}
        <System {query} />
      {/if}
      </div>
      {/key}
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
    display: flex; align-items: center; gap: 10px; padding: 0 10px 22px;
    font-family: var(--font-display); font-size: var(--t-h2); font-weight: var(--w-display); letter-spacing: var(--track-heading);
  }

  .search {
    display: flex; align-items: center; gap: 9px; margin-bottom: 18px; padding: 0 10px; color: var(--muted);
    border: 1px solid var(--border2); border-radius: var(--r);
  }
  .search:focus-within { border-color: var(--accent); box-shadow: 0 0 0 1px var(--accent); }
  .search { background: var(--field); }
  .search input { flex: 1; min-width: 0; padding: 7px 0; border: 0; border-radius: 0; background: transparent; outline: none; }
  .search input::placeholder { color: var(--muted); }
  .search input:focus { box-shadow: none; }
  .search input::-webkit-search-cancel-button { display: none; }
  .search kbd {
    font: 500 var(--t-micro) var(--font-body); color: var(--muted); border: 1px solid var(--border2); border-radius: 4px; padding: 0 5px;
  }

  .cap { padding: 4px 10px 6px; font-size: var(--t-small); color: var(--muted); }
  .vbtn {
    display: flex; align-items: center; gap: 10px; padding: 8px 12px; margin-bottom: 2px; border-radius: var(--r);
    color: var(--text2); font-weight: 500; font-size: var(--t-body); text-decoration: none; white-space: nowrap;
    transition: background-color var(--dur-fast) var(--ease-out), color var(--dur-fast) var(--ease-out);
  }
  .vbtn :global(.icon) { color: var(--muted); }
  .vbtn:hover { background: var(--surface2); color: var(--text); }
  .vbtn:hover :global(.icon) { color: var(--text2); }
  /* The current view: a filled blue pill. */
  .vbtn[aria-current='page'], .vbtn[aria-current='page']:hover { background: var(--accent-fill); color: var(--accent-ink); font-weight: 600; }
  .vbtn[aria-current='page'] :global(.icon) { color: var(--accent-ink); }
  nav { position: relative; }
  .vbtn { position: relative; }
  .pill { position: absolute; left: 0; top: 0; border-radius: var(--r); background: var(--accent-fill); pointer-events: none; }
  .pill.ready {
    transition-property: transform, width, height; transition-duration: var(--dur-slow); transition-timing-function: var(--ease-emph);
  }
  .placed .vbtn[aria-current='page'], .placed .vbtn[aria-current='page']:hover { background: transparent; }

  /* The foot of the side column: the theme, then the account. */
  .foot { margin-top: auto; display: grid; gap: 12px; padding-top: 12px; border-top: 1px solid var(--border); }
  .theme-row { display: flex; align-items: center; justify-content: space-between; gap: 10px; padding: 0 10px; font-size: var(--t-small); }
  .acct { display: flex; align-items: center; gap: 10px; padding: 0 10px; }
  .av {
    width: 28px; height: 28px; border-radius: 50%; background: var(--surface3); display: grid; place-items: center;
    font-weight: 600; font-size: var(--t-small); flex: none;
  }
  .nm { display: grid; min-width: 0; flex: 1; line-height: 1.3; font-weight: 500; font-size: var(--t-body); }
  .nm .muted { font-size: var(--t-small); font-weight: 400; }
  .acct .ibtn { border-color: transparent; }

  .top { display: flex; align-items: center; gap: 14px; margin-bottom: 20px; min-width: 0; }
  h1 { flex: 1; }
  .live { display: inline-flex; align-items: center; gap: 4px; flex: none; font-size: var(--t-small); color: var(--muted); white-space: nowrap; }
  .live i { position: relative; width: 7px; height: 7px; border-radius: 50%; background: var(--ok); margin-right: 4px; }
  /* One soft ring leaves the dot when a reading arrives. */
  .live i.beat::after {
    content: ''; position: absolute; inset: 0; border-radius: 50%; border: 1px solid var(--ok);
    animation: beat var(--dur-glow) var(--ease-out) both;
  }
  @keyframes beat {
    from { transform: scale(1); opacity: 0.8; }
    to { transform: scale(2.6); opacity: 0; }
  }
  .live.stale i { background: var(--warn); }
  .live.stale { color: var(--warn); }

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
    .foot { grid-area: acct; display: flex; align-items: center; gap: 8px; margin: 0; padding: 0; border: 0; }
    .theme-row, .acct { padding: 0; }
    .theme-row > span, .nm, .av { display: none; }
    .search { grid-area: search; margin: 0; }
    .search kbd { display: none; }
    nav {
      position: fixed; z-index: 40; left: 0; right: 0; bottom: 0; display: flex; gap: 4px;
      padding: 6px 8px calc(6px + env(safe-area-inset-bottom)); background: var(--rail); border-top: 1px solid var(--border);
    }
    nav .cap { display: none; }
    .vbtn {
      flex: 1 1 0; min-width: 0; min-height: 48px; flex-direction: column; justify-content: center; gap: 3px;
      padding: 7px 2px; margin: 0; font-size: var(--t-micro);
    }
    .vlabel { max-width: 100%; overflow: hidden; text-overflow: ellipsis; }
    .vbtn[aria-current='page'], .vbtn[aria-current='page']:hover { background: var(--accent-fill); color: var(--accent-ink); }
    .placed .vbtn[aria-current='page'], .placed .vbtn[aria-current='page']:hover { background: transparent; }
    .top { margin: 10px 0 16px; }
  }
  @media (max-width: 400px) {
    .live .ago:not(.keep), .live .word { display: none; }
  }
  /* Six labels no longer fit below 390px: the dock shows icons only, the label stays the link's name. */
  @media (max-width: 389px) {
    .vlabel {
      position: absolute; width: 1px; height: 1px; margin: -1px; overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap;
    }
  }
</style>
