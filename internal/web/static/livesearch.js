// Live search for the customer and repair lists.
//
// The form keeps working without JavaScript (Enter submits it); with JavaScript
// it fetches the same URL with ?partial=1, which returns only the results
// fragment, and swaps it in while the operator types.
(() => {
  const form = document.querySelector('form[data-live-search]');
  const results = document.querySelector('[data-live-results]');
  if (!form || !results) return;

  const input = form.querySelector('input[type="search"]');
  if (!input) return;

  let timer = null;
  let seq = 0;

  const queryURL = () => {
    const url = new URL(form.getAttribute('action') || location.pathname, location.origin);
    const data = new FormData(form);
    for (const [key, value] of data) {
      if (String(value).trim() !== '') url.searchParams.set(key, value);
    }
    url.searchParams.delete('page');
    return url;
  };

  const load = async () => {
    const url = queryURL();
    const want = url.toString();
    if (want === load.last) return;
    const ticket = ++seq;
    let response;
    try {
      response = await fetch(url.pathname + '?partial=1&' + url.searchParams.toString(), {
        credentials: 'same-origin',
        headers: { Accept: 'text/html' },
      });
    } catch (err) {
      return; // offline: keep the current rows, the operator can still submit
    }
    if (!response.ok || ticket !== seq) return;
    const html = await response.text();
    results.innerHTML = html;
    load.last = want;
    const clean = url.pathname + (url.search ? url.search : '');
    history.replaceState(null, '', clean);
  };

  const schedule = () => {
    if (timer) clearTimeout(timer);
    timer = setTimeout(load, 180);
  };

  input.addEventListener('input', schedule);
  form.addEventListener('change', (event) => {
    if (event.target instanceof HTMLSelectElement) load();
  });
  form.addEventListener('submit', (event) => {
    event.preventDefault();
    if (timer) clearTimeout(timer);
    load();
  });
})();
