<script>
  import Icon from './icons/Icon.svelte';

  // AppIcon is an app's own logo in one colour (the text colour, through a CSS mask), or the adapter's line icon
  // when the server has no logo for it. adapter is an entry of GET /api/adapters; it never names an app.
  let { adapter = null, size = 18 } = $props();

  // Only a same-origin path is used, quoted for CSS.
  const url = $derived(typeof adapter?.iconUrl === 'string' && adapter.iconUrl.startsWith('/api/icons/') ?
    `url("${adapter.iconUrl.replace(/["\\\n]/g, '')}")` : '');
</script>

{#if url}
  <span class="logo" aria-hidden="true" style:--logo={url} style:width={`${size}px`} style:height={`${size}px`}></span>
{:else}
  <Icon name={adapter?.icon} {size} />
{/if}

<style>
  .logo {
    display: block; flex: none; background: currentColor;
    -webkit-mask: var(--logo) center / contain no-repeat; mask: var(--logo) center / contain no-repeat;
  }
</style>
