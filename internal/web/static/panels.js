'use strict';

// Remember each fold panel's state locally. This is progressive enhancement:
// panels work as ordinary <details> elements without JavaScript, and a direct
// hash target always opens its containing panel.
(() => {
  const prefix = 'repairflow.panel.';
  const storage = (() => {
    try { return globalThis.localStorage; } catch { return null; }
  })();
  const keyFor = panel => panel.getAttribute('data-fold-key') || panel.id;
  const read = key => {
    try { return storage?.getItem(prefix + key) ?? null; } catch { return null; }
  };
  const write = (key, open) => {
    try { storage?.setItem(prefix + key, open ? 'open' : 'closed'); } catch {}
  };

  for (const panel of document.querySelectorAll('details.fold-panel')) {
    const key = keyFor(panel);
    if (!key) continue;
    const defaultClosed = panel.getAttribute('data-fold-default') === 'closed';
    const saved = defaultClosed ? null : read(key);
    if (saved === 'closed') panel.open = false;
    else if (saved === 'open') panel.open = true;
    if (!defaultClosed) panel.addEventListener('toggle', () => write(key, panel.open));
  }

  const openTarget = () => {
    if (!location.hash) return;
    let id = location.hash.slice(1);
    try { id = decodeURIComponent(id); } catch {}
    const target = document.getElementById(id);
    const panel = target?.closest('details.fold-panel');
    if (panel && !panel.open) panel.open = true;
  };
  window.addEventListener('hashchange', openTarget);
  openTarget();
})();
