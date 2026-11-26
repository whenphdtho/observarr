<script>
  import BrandMark from './BrandMark.svelte';
  import { api } from './api.js';

  // mode: 'setup' | 'login'. notice, when set, is shown above the fields (used to tell the user
  // why they landed back here, e.g. their session ended) — the caller clears it once no longer
  // relevant.
  let { mode, notice = '', ondone, onstale } = $props();
  let name = $state('');
  let password = $state('');
  let token = $state('');
  let error = $state('');
  let busy = $state(false);
  const setup = $derived(mode === 'setup');

  let nameEl;
  // Autofocus the user name field whenever this form appears or switches between setup and login
  // (mode is the only prop that changes which form is shown), not just on first mount.
  $effect(() => {
    mode;
    nameEl?.focus();
  });

  async function submit(event) {
    event.preventDefault();
    error = '';
    busy = true;
    try {
      const body = setup ? { name, password, token } : { name, password };
      ondone(await api('POST', setup ? '/api/setup' : '/api/login', body));
    } catch (err) {
      if (setup && err.status === 409) {
        // Setup already happened elsewhere: re-check the session so the login form appears.
        onstale();
        return;
      }
      error = err.message;
    } finally {
      busy = false;
    }
  }
</script>

<main>
  <form class="card" onsubmit={submit}>
    <div class="brand">
      <BrandMark size={40} />
      <h1>Observarr</h1>
    </div>
    <p class="lede">{setup ? 'Create the admin account.' : 'Sign in to continue.'}</p>
    {#if !setup && notice}<p class="muted notice" role="status">{notice}</p>{/if}
    <label>
      User name
      <input bind:this={nameEl} bind:value={name} autocomplete="username" required maxlength="64" />
    </label>
    {#if setup}
      <label>
        Setup token
        <input bind:value={token} required maxlength="64" autocomplete="off" spellcheck="false" />
        <span class="muted hint">
          Shown in the server log (docker logs observarr) and in the file setup-token in the data folder.
        </span>
      </label>
    {/if}
    <label>
      Password
      <input type="password" bind:value={password} required minlength={setup ? 12 : 1} maxlength="1024"
        autocomplete={setup ? 'new-password' : 'current-password'} />
      {#if setup}<span class="muted hint">At least 12 characters</span>{/if}
    </label>
    {#if error}<p class="error" role="alert">{error}</p>{/if}
    <button disabled={busy}>{setup ? 'Create account' : 'Sign in'}</button>
  </form>
</main>

<style>
  main { min-height: 100vh; min-height: 100dvh; display: grid; place-items: center; padding: 16px; }
  form {
    width: min(392px, 100%); display: grid; gap: 16px; padding: 30px 28px 28px; box-shadow: var(--lift);
    animation: arrive var(--dur-slow) var(--ease-out);
  }
  @keyframes arrive { from { opacity: 0; transform: translateY(8px); } }
  .brand { display: flex; align-items: center; gap: 14px; margin-bottom: 2px; }
  .lede { color: var(--text2); font-size: var(--t-body); margin-top: -6px; }
  @media (max-width: 420px) {
    form { padding: 24px 20px 22px; }
  }
  label { display: grid; gap: 6px; font-size: var(--t-body); color: var(--text2); }
  .hint { font-size: var(--t-small); }
  .notice { font-size: var(--t-body); }
  button { margin-top: 4px; padding: 9px 14px; }
</style>
