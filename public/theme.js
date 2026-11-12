'use strict';

// Applies the saved appearance before first paint so the page never flashes
// the wrong theme. Light is opt-in; anything else falls back to dark.
(() => {
  const KEY = 'adroute.theme';
  const root = document.documentElement;
  let saved = '';
  try { saved = localStorage.getItem(KEY) || ''; } catch { /* storage may be blocked */ }
  if (saved === 'light') root.setAttribute('data-theme', 'light');
})();
