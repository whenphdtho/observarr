<script>
  import ServiceForm from './ServiceForm.svelte';

  // EditService is the dialog that changes a service (name, URL, key, linked container). Focus
  // returns to whatever opened it.
  let { open = $bindable(false), service = null, adapters = [], containers = [] } = $props();

  let dialogEl;
  let opener = null;

  function handleDialogClose() {
    open = false;
    opener?.focus();
    opener = null;
  }

  $effect(() => {
    if (!dialogEl) return;
    if (open && !dialogEl.open) {
      opener = document.activeElement;
      dialogEl.showModal();
    } else if (!open && dialogEl.open) {
      dialogEl.close();
    }
  });
</script>

<dialog bind:this={dialogEl} aria-labelledby="editServiceTitle" onclose={handleDialogClose}>
  <h2 id="editServiceTitle">Edit service</h2>
  {#if open && service}
    {#key service.id}
      <ServiceForm mode="edit" {service} {adapters} {containers}
        oncancel={() => { open = false; }} ondone={() => { open = false; }} />
    {/key}
  {/if}
</dialog>

<style>
  dialog { width: min(520px, calc(100vw - 32px)); }
  h2 { margin-bottom: 16px; }
</style>
