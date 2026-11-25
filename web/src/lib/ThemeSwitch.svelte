<script>
  import Icon from './icons/Icon.svelte';
  import { theme, setTheme } from './theme.js';

  // ThemeSwitch picks the colour theme: a segmented control of three icons, the current one a filled pill.
  const OPTIONS = [
    { id: 'system', label: 'System', title: 'Follow the system theme', icon: 'monitor' },
    { id: 'light', label: 'Light', title: 'Light theme', icon: 'sun' },
    { id: 'dark', label: 'Dark', title: 'Dark theme', icon: 'moon' },
  ];
</script>

<div class="seg theme" role="group" aria-label="Theme">
  {#each OPTIONS as o (o.id)}
    <button type="button" aria-pressed={$theme === o.id} aria-label={o.label} title={o.title} onclick={() => setTheme(o.id)}>
      <Icon name={o.icon} size={15} />
    </button>
  {/each}
</div>

<style>
  .theme button { padding: 4px 9px; }
</style>
