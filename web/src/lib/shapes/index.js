import Health from './Health.svelte';
import Queue from './Queue.svelte';
import Security from './Security.svelte';
import Mounts from './Mounts.svelte';
import Requests from './Requests.svelte';

// widgets maps a shape kind to the component that renders it. This is the only place in the UI
// that does: adding a shape means adding one line here and a component beside it, without
// touching ServiceCard, Apps, or anything else. A kind with no entry here renders nothing.
export const widgets = {
  health: Health,
  queue: Queue,
  security: Security,
  mounts: Mounts,
  requests: Requests,
};
