// Start the browser-local backend before asking htmx for the initial list.
(async () => {
  const showError = message => {
    document.getElementById('error-message').textContent = message;
    document.getElementById('error').hidden = false;
  };
  if (document.getElementById('demo-banner')) return;
  document.getElementById('bookmarklet-link').closest('p').hidden = true;
  const banner = document.createElement('p');
  banner.id = 'demo-banner';
  banner.textContent = 'Interactive demo · Data stays in this browser. Saving a URL adds sample metadata without fetching the page.';
  document.querySelector('.header-row').after(banner);
  if (!('serviceWorker' in navigator) || !('indexedDB' in window)) {
    showError('This demo requires service workers and IndexedDB. Open it over HTTPS or localhost.');
    return;
  }
  try {
    const registration = await navigator.serviceWorker.register('./demo-sw.js');
    if (!navigator.serviceWorker.controller) {
      if (registration.active) registration.active.postMessage('claim');
      await new Promise((resolve, reject) => {
        const timer = setTimeout(() => reject(Error('Demo backend did not start')), 10000);
        navigator.serviceWorker.addEventListener('controllerchange', () => { clearTimeout(timer); resolve(); }, {once:true});
      });
    }
    htmx.ajax('GET', `.${location.search}`, {target:'#links'});
  } catch (error) { showError(error.message); }
})();
