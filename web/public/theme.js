// Sets the colour theme before the first paint, so the page never flashes in the other one: the choice stored
// in this browser, else the system's. A classic script, loaded from the page's head (the CSP allows no inline
// script). lib/theme.js reads the same key and keeps the theme current afterwards.
(function () {
  var choice = null;
  try {
    choice = localStorage.getItem('observarr.theme');
  } catch (e) {
    // No storage: follow the system.
  }
  if (choice !== 'light' && choice !== 'dark') {
    choice = matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
  }
  document.documentElement.setAttribute('data-theme', choice);
})();
