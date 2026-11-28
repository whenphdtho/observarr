import { writable, derived, get } from 'svelte/store';
import { api } from './api.js';
import { change, mean } from './history.js';
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

// trend is the host's last hour for the key figures (GET /api/system's spark: a reading a minute):
// { cpu, mem } (percent and bytes, oldest first, null for a gap) and the averages of the hour before,
// previousCpu and previousMem (null when unknown). It is null while the server sends none.
export const trend = writable(null);

const initialSnapshot = writable(null);

function readTrend(snap) {
  const spark = snap?.spark;
  if (!spark || !Array.isArray(spark.cpu)) return null;
  const num = (v) => (Number.isFinite(v) ? v : null);
  return {
    cpu: spark.cpu.map(num),
    mem: Array.isArray(spark.memUsed) ? spark.memUsed.map(num) : [],
    previousCpu: num(snap.previousCpu ?? spark.previousCpu),
    previousMem: num(snap.previousMem ?? spark.previousMem),
  };
}

// hostLines is what the host's key figures draw and compare: the last hour of CPU (percent) and memory
// (bytes) from trend, or the live five minutes while the server sends no hour, and the change of the last
// hour's average against the hour before (null when unknown).
export const hostLines = derived([trend, history], ([t, h]) => ({
  cpu: t?.cpu.length > 1 ? t.cpu : h.map((x) => x.cpuPercent),
  mem: t?.mem.length > 1 ? t.mem : h.map((x) => x.memUsed),
  cpuChange: t ? change(mean(t.cpu), t.previousCpu) : null,
  memChange: t ? change(mean(t.mem), t.previousMem) : null,
}));

// watchTrend keeps trend current while a view that shows it is open: once now, then every minute.
// It returns the function that stops it.
export function watchTrend() {
  const load = async () => {
    try {
      const next = readTrend(await api('GET', '/api/system'));
      if (next) trend.set(next);
    } catch {
      // The figures keep their last line; the next minute tries again.
    }
  };
  if (!get(trend)) load();
  const id = setInterval(load, 60 * 1000);
  return () => clearInterval(id);
}

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
    trend.set(readTrend(snap));
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
    trend.set(null);
    return;
  }
  const t = readTrend(snap);
  if (t) trend.set(t);
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
