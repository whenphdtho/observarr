<script>
  import { onMount } from 'svelte';
  import { api, setCsrf, setUnauthorizedHandler } from './lib/api.js';
  import { live } from './lib/live.js';
  import Credentials from './lib/Credentials.svelte';
  import Shell from './lib/Shell.svelte';

  let view = $state('loading'); // loading | setup | login | ready | error
  let user = $state('');
  let error = $state('');
  let sessionNotice = $state('');

  function signedIn(session) {
    setCsrf(session.csrf);
    user = session.user;
    sessionNotice = '';
    view = 'ready';
  }

  // forgetSession resets everything a signed-in view holds, whether sign-out was the user's own
  // choice or the live stream just discovered the session had already expired.
  function forgetSession() {
    live.close();
    setCsrf('');
    user = '';
    view = 'login';
  }

  // handleSessionEnded runs when the live stream or any other API call discovers the session is
  // gone without the user having clicked Sign out themselves — the login form then explains why
  // it reappeared instead of looking like sign-out silently failed.
  function handleSessionEnded() {
    sessionNotice = 'Your session ended — sign in again.';
    forgetSession();
  }

  async function load() {
    try {
      const session = await api('GET', '/api/session');
      if (session.setup) view = 'setup';
      else signedIn(session);
    } catch (err) {
      if (err.status === 401) view = 'login';
      else { error = err.message; view = 'error'; }
    }
  }

  async function logout() {
    try {
      await api('POST', '/api/logout');
    } catch {
      // Session already gone or server unreachable: show the login form anyway.
    } finally {
      forgetSession();
    }
  }

  live.setOnSignedOut(handleSessionEnded);
  setUnauthorizedHandler(handleSessionEnded);
  onMount(load);
</script>

{#if view === 'loading'}
  <p class="status muted">Loading…</p>
{:else if view === 'error'}
  <p class="status error" role="alert">{error}</p>
{:else if view === 'ready'}
  <Shell {user} onlogout={logout} />
{:else}
  <Credentials mode={view} notice={sessionNotice} ondone={signedIn} onstale={load} />
{/if}

<style>
  .status { text-align: center; margin-top: 30vh; }
</style>
