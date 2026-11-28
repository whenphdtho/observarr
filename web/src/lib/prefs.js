// What this browser remembers about how the Overview was left: a folded section, the apps column showing all, the
// chart's range. Storage can be missing or blocked (a private window, cleared site data); the page then starts
// from its defaults and still works.
const KEY = 'observarr.overview';

function read() {
  try {
    const v = JSON.parse(localStorage.getItem(KEY) ?? '{}');
    return v && typeof v === 'object' && !Array.isArray(v) ? v : {};
  } catch {
    return {};
  }
}

// recall gives what was remembered under name, or fallback when nothing of fallback's type was.
export function recall(name, fallback) {
  const v = read()[name];
  return typeof v === typeof fallback ? v : fallback;
}

export function remember(name, value) {
  try {
    localStorage.setItem(KEY, JSON.stringify({ ...read(), [name]: value }));
  } catch {
    // Not remembered this time; nothing else depends on it.
  }
}
