<script>
  import { onMount } from 'svelte';
  import Apps from './Apps.svelte';
  import System from './System.svelte';
  import { live } from './live.js';

  let { user, onlogout } = $props();

  // The tab is remembered in the URL hash (#/system), so a reload or a shared link keeps it.
  function tabFromHash() {
    return window.location.hash === '#/system' ? 'system' : 'apps';
  }

  let tab = $state(tabFromHash());

  function selectTab(next) {
    if (tab === next) return;
    tab = next;
    const hash = next === 'system' ? '#/system' : '';
    history.replaceState(null, '', window.location.pathname + window.location.search + hash);
  }

  function onHashChange() {
    tab = tabFromHash();
  }

  onMount(() => {
    // One stream for the whole app: the System view relies on it being open (the server samples
    // the host and containers faster while a stream is open), whether or not that tab is shown.
    live.open();
    window.addEventListener('hashchange', onHashChange);
    return () => window.removeEventListener('hashchange', onHashChange);
  });
</script>

<header>
  <h1>Observarr</h1>
  <nav class="tabs" aria-label="Sections">
    <button type="button" class="tab" class:active={tab === 'apps'}
      aria-current={tab === 'apps' ? 'page' : undefined} onclick={() => selectTab('apps')}>
      Apps
    </button>
    <button type="button" class="tab" class:active={tab === 'system'}
      aria-current={tab === 'system' ? 'page' : undefined} onclick={() => selectTab('system')}>
      System
    </button>
  </nav>
  <span class="muted user ellipsis" title={user}>{user}</span>
  <button class="quiet" onclick={onlogout} title={`Signed in as ${user}`}>Sign out</button>
</header>
<main>
  {#if tab === 'apps'}
    <Apps />
  {:else}
    <System />
  {/if}
</main>

<style>
  header {
    display: flex; align-items: center; gap: 8px; padding: 10px 12px; border-bottom: 1px solid var(--border);
    flex-wrap: nowrap;
  }
  header h1 { margin: 0; font: inherit; font-weight: 700; flex-shrink: 0; }
  header button.quiet { padding: 6px 10px; flex-shrink: 0; }
  .tabs { display: flex; gap: 2px; flex-shrink: 0; }
  .tab {
    font: inherit; color: var(--muted); background: transparent; border: 1px solid transparent;
    border-radius: 8px; padding: 6px 10px; cursor: pointer;
  }
  .tab.active { color: var(--text); background: var(--surface); border-color: var(--border); }
  .user { margin-left: auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

  /* Below ~420px there isn't room for the signed-in name next to the tabs and Sign out button;
     it stays reachable as the Sign out button's title instead of forcing the header to wrap. */
  @media (max-width: 419px) {
    .user { display: none; }
  }

  main { max-width: 1100px; margin: 0 auto; padding: 24px 16px; }
</style>
