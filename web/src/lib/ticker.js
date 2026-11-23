import { readable } from 'svelte/store';

// now ticks once a second, shared by every widget that shows a reading's age. A `readable` store
// runs its start function only while it has at least one subscriber and only once no matter how
// many components subscribe, so every widget updates from one interval, not one each.
export const now = readable(Date.now(), (set) => {
  const id = setInterval(() => set(Date.now()), 1000);
  return () => clearInterval(id);
});
