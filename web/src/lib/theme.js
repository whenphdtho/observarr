import { get, writable } from 'svelte/store';

// The colour theme: 'system' follows the operating system, 'light' and 'dark' are fixed. The choice is kept per
// browser; without storage the theme still switches, for this page only. public/theme.js applies the stored
// choice before the first paint, from the same key.
const KEY = 'observarr.theme';
export const THEMES = ['system', 'light', 'dark'];
const light = matchMedia('(prefers-color-scheme: light)');

function stored() {
  try {
    const v = localStorage.getItem(KEY);
    return THEMES.includes(v) ? v : 'system';
  } catch {
    return 'system';
  }
}

export const theme = writable(stored());

// paint sets a resolved theme ('light' or 'dark') on the page and gives the browser's own bars the page's ground.
function paint(resolved) {
  const root = document.documentElement;
  root.dataset.theme = resolved;
  const ground = getComputedStyle(root).getPropertyValue('--bg').trim();
  if (ground) for (const m of document.querySelectorAll('meta[name="theme-color"]')) m.content = ground;
}

// apply paints the choice. A change after the first paint crossfades the whole page once (see app.css), so the
// panels, the text and the charts change together; without View Transitions, with reduced motion or in a hidden
// tab it is immediate.
const still = matchMedia('(prefers-reduced-motion: reduce)');
let first = true;
function apply(choice) {
  const resolved = choice === 'system' ? (light.matches ? 'light' : 'dark') : choice;
  const fade = !first && resolved !== document.documentElement.dataset.theme && document.startViewTransition &&
    !still.matches && !document.hidden;
  first = false;
  if (!fade) return paint(resolved);
  // A crossfade the browser gives up on (the window resized meanwhile) still paints the theme, just at once.
  const t = document.startViewTransition(() => paint(resolved));
  t.ready.catch(() => {});
  t.finished.catch(() => {});
}

theme.subscribe(apply);

// setTheme changes and remembers the choice.
export function setTheme(choice) {
  if (!THEMES.includes(choice)) return;
  try {
    if (choice === 'system') localStorage.removeItem(KEY);
    else localStorage.setItem(KEY, choice);
  } catch {
    // Not remembered; the theme still changes.
  }
  theme.set(choice);
}

light.addEventListener('change', () => {
  if (get(theme) === 'system') apply('system');
});
// A choice made in another tab of this browser applies here too.
window.addEventListener('storage', (e) => {
  if (e.key === KEY || e.key === null) theme.set(stored());
});
