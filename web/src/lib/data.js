import { writable, derived } from 'svelte/store';
import { api } from './api.js';
import { systemSnapshot } from './live.js';

// Data every view shares besides the services themselves (live.js): the adapters list and the
// system snapshot with its host history. Shell loads it once after sign-in and again after a
// reconnect; the live stream keeps the snapshot and the history current in between.

const HISTORY_WINDOW_MS = 5 * 60 * 1000;

export const adapters = writable([]);
export const adaptersError = writable('');
export const systemError = writable('');
// history holds system.HostView readings, oldest first, covering the last five minutes.
export const history = writable([]);

const initialSnapshot = writable(null);

// snapshot is the latest system.Snapshot (host, containers, notices), or null before the first.
export const snapshot = derived([systemSnapshot, initialSnapshot], ([live, initial]) => live ?? initial);

export async function loadAdapters() {
  try {
    adapters.set(await api('GET', '/api/adapters'));
    adaptersError.set('');
  } catch (err) {
    adaptersError.set(err.message);
  }
}

export async function loadSystem() {
  try {
    const [snap, hist] = await Promise.all([api('GET', '/api/system'), api('GET', '/api/system/history')]);
    initialSnapshot.set(snap);
    history.set(hist ?? []);
    systemError.set('');
  } catch (err) {
    systemError.set(err.message);
  }
}

// Each live host reading is appended once (by its "at" timestamp) and the history trimmed to the
// window the server's own history covers. The trim uses the newest sample's own time, not the
// wall clock, which can drift from it (a suspended tab, a slow clock). A null snapshot means the
// stream was closed on sign-out: everything known is dropped with it.
systemSnapshot.subscribe((snap) => {
  if (!snap) {
    initialSnapshot.set(null);
    history.set([]);
    return;
  }
  const h = snap.host;
  if (!h) return;
  history.update((list) => {
    const last = list[list.length - 1];
    if (last && last.at === h.at) return list;
    const cutoff = new Date(h.at).getTime() - HISTORY_WINDOW_MS;
    return [...list.filter((x) => new Date(x.at).getTime() >= cutoff), h];
  });
});

export function adapterFor(list, type) {
  return list.find((a) => a.type === type) ?? null;
}
