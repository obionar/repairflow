'use strict';

// Progressive enhancement only: the CSS drawer works without this script.
// This adds the accessibility and dismissal behaviour the reference shell has:
// focus trap, Escape, outside/backdrop close and focus return.
(() => {
  const menu = document.getElementById('navigation-menu');
  const toggle = menu?.querySelector('summary');
  const panel = document.getElementById('main-navigation');
  const main = document.querySelector('main');
  const brand = document.querySelector('.brand');
  if (!toggle || !panel) return;

  const desktop = matchMedia('(min-width: 768px)');
  const close = () => { menu.open = false; };
  const sync = () => {
    toggle.setAttribute('aria-expanded', String(menu.open));
    toggle.setAttribute('aria-label', menu.open ? 'Close navigation' : 'Open navigation');
    if (main) main.inert = menu.open;
    if (brand) brand.inert = menu.open;
    if (menu.open) {
      requestAnimationFrame(() => {
        if (menu.open) panel.querySelector('a')?.focus();
      });
    }
  };

  menu.addEventListener('toggle', sync);
  toggle.setAttribute('aria-expanded', 'false');

  document.addEventListener('keydown', event => {
    if (event.key === 'Escape' && menu.open) {
      event.preventDefault();
      close();
      toggle.focus();
      return;
    }
    if (event.key !== 'Tab' || !menu.open) return;
    const items = [toggle, ...panel.querySelectorAll('a, button')].filter(item => !item.disabled);
    const last = items.at(-1);
    if (event.shiftKey && document.activeElement === toggle) {
      event.preventDefault();
      last?.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      toggle.focus();
    } else if (!items.includes(document.activeElement)) {
      event.preventDefault();
      toggle.focus();
    }
  });

  document.addEventListener('click', event => {
    if (menu.open && !menu.contains(event.target) && !panel.contains(event.target)) close();
  });
  document.addEventListener('focusin', event => {
    if (menu.open && !menu.contains(event.target) && !panel.contains(event.target)) close();
  });
  desktop.addEventListener('change', () => {
    if (desktop.matches && menu.open) {
      const focused = document.activeElement === toggle;
      close();
      if (focused) {
        [...panel.querySelectorAll('a')].find(link => !link.classList.contains('nav-dashboard-link'))?.focus();
      }
    }
  });
})();
