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

// apply sets the resolved theme on the page and gives the browser's own bars the page's ground.
function apply(choice) {
  const root = document.documentElement;
  root.dataset.theme = choice === 'system' ? (light.matches ? 'light' : 'dark') : choice;
  const ground = getComputedStyle(root).getPropertyValue('--bg').trim();
  if (ground) for (const m of document.querySelectorAll('meta[name="theme-color"]')) m.content = ground;
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
