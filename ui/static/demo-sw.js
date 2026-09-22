const scope = new URL(self.registration.scope);
const DB_NAME = 'mylinks-demo-v1';
const STORE = 'links';
const samples = [
  {url:'https://go.dev/', title:'The Go Programming Language', description:'Documentation and resources for Go.'},
  {url:'https://developer.mozilla.org/', title:'MDN Web Docs', description:'Guides and references for the open web.'},
  {url:'https://www.wikipedia.org/', title:'Wikipedia', description:'A free encyclopedia.'},
  {url:'note:4', title:'Ideas to explore', description:'Try saving a link, editing it, and searching these notes.'}
];
self.addEventListener('install', event => event.waitUntil(self.skipWaiting()));
self.addEventListener('activate', event => event.waitUntil(self.clients.claim()));
self.addEventListener('message', event => { if (event.data === 'claim') event.waitUntil(self.clients.claim()); });
function openDB() {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(DB_NAME, 1);
    request.onupgradeneeded = () => {
      const store = request.result.createObjectStore(STORE, {keyPath:'id', autoIncrement:true});
      for (const [i, item] of samples.entries()) store.add({...item, id:i+1, addedAt:new Date(Date.now()-(samples.length-i)*86400000).toISOString()});
    };
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
}
async function transaction(mode, action) {
  const db = await openDB();
  try {
    return await new Promise((resolve, reject) => {
      const tx = db.transaction(STORE, mode), store = tx.objectStore(STORE);
      let result;
      let request;
      try { request = action(store); } catch (error) { reject(error); return; }
      if (request) request.onsuccess = () => { result = request.result; };
      tx.oncomplete = () => resolve(result);
      tx.onerror = () => reject(tx.error);
      tx.onabort = () => reject(tx.error);
    });
  } finally { db.close(); }
}
const all = () => transaction('readonly', store => store.getAll());
const get = id => transaction('readonly', store => store.get(id));
const put = item => transaction('readwrite', store => store.put(item));
const remove = id => transaction('readwrite', store => store.delete(id));
const escapeHTML = value => String(value).replace(/[&<>"']/g, char => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[char]));
const response = (body, status=200, headers={}) => new Response(body, {status, headers:{'Content-Type':'text/html; charset=utf-8', ...headers}});
const error = (message, status=400) => response(message, status);
function validURL(value) {
  try {
    const url = new URL(value);
    return ['http:','https:'].includes(url.protocol) && !!url.hostname && !url.username && !url.password && !/\s/.test(value);
  } catch { return false; }
}
function sampleMetadata(value) {
  const url = new URL(value);
  const host = url.hostname.replace(/^www\./, '');
  return {title:host.slice(0,250), description:`Sample link from ${host}. The demo did not fetch this page.`};
}
function renderItem(item, editing=false) {
  const id = item.id, title = escapeHTML(item.title), description = escapeHTML(item.description), url = escapeHTML(item.url);
  const note = item.url.startsWith('note:');
  const content = editing ? `<div class="link-edit"><form hx-patch="./${id}" hx-swap="outerHTML" hx-target="closest .link-item" hx-disabled-elt="find button"><div class="f-row"><input name="title" value="${title}" class="width:100%" maxlength="250" required><button class="iconbutton" type="submit" title="Save">&#x2714;</button><button class="iconbutton" type="button" title="Cancel" hx-get="./${id}" hx-swap="outerHTML" hx-target="closest .link-item">&#x274C;</button></div><textarea name="description" class="width:100%" maxlength="1020">${description}</textarea></form></div>` : `<h5 class="link-title list-of-links">${note ? title : `<a href="${url}" target="_blank" rel="noopener noreferrer" class="inline-block text-truncate width:100%" title="${title}">${title}</a>`}</h5><p class="link-description">${description}</p>`;
  return `<div class="link-item link-header box info bg f-col">${content}<div class="mt-auto list-of-links">${note ? '' : `<a href="${url}" target="_blank" rel="noopener noreferrer" class="inline-block text-truncate width:100%" title="${url}">${url}</a>`}</div><div><button hx-get="./${id}?edit=1" hx-swap="outerHTML" hx-target="closest .link-item">Edit</button> <button class="warn bg" hx-delete="./${id}" hx-swap="delete swap:1s" hx-target="closest .link-item">Delete</button> <span class="text-nowrap">${escapeHTML(new Date(item.addedAt).toLocaleString())}</span></div></div>`;
}
async function list(search='') {
  let items = (await all()).sort((a,b) => b.id-a.id);
  if (search) {
    const words = search.toLocaleLowerCase().split(/\s+/).filter(Boolean);
    items = items.filter(item => words.every(word => `${item.title} ${item.description} ${item.url}`.toLocaleLowerCase().includes(word)));
  }
  const heading = search ? `<h2>Search results for "${escapeHTML(search)}" <button hx-get="." hx-target="#links" hx-push-url="true">Clear</button></h2>` : '<h2>Saved Links</h2>';
  return response(heading + (items.length ? `<div class="links-grid">${items.map(item => renderItem(item)).join('')}</div>` : `<p>${search ? 'No results found' : 'No links saved yet. Add your first link!'}</p>`));
}
async function handle(request, path, url) {
  const method = request.method;
  if (path === '' || path === 'index.html') {
    if (method === 'GET') {
      if (request.headers.get('HX-Request') === 'true') return list(url.searchParams.get('s') || '');
      return fetch(new URL('./index.html', scope));
    }
    if (method === 'POST') {
      const form = await request.formData();
      let item;
      const raw = String(form.get('url') || '').trim();
      if (raw) {
        if (!validURL(raw)) return error('Invalid URL. Must be a valid HTTP/HTTPS URL');
        if ((await all()).some(row => row.url === raw)) return error('URL already exists', 409);
        item = {url:raw, ...sampleMetadata(raw)};
      } else {
        const title = String(form.get('note-title') || '').trim();
        const description = String(form.get('note-text') || '').trim();
        if (!title || !description || title.length > 250 || description.length > 1020) return error('A note needs a title and text within the size limits');
        item = {url:`note:${crypto.randomUUID()}`, title, description};
      }
      await put({...item, addedAt:new Date().toISOString()});
      return list();
    }
  }
  if (path === 'bookmarklet' && method === 'GET') {
    const raw = url.searchParams.get('url') || '';
    if (!validURL(raw)) return error('Invalid URL. Must be a valid HTTP/HTTPS URL');
    if ((await all()).some(row => row.url === raw)) return error('URL already exists', 409);
    await put({url:raw, ...sampleMetadata(raw), addedAt:new Date().toISOString()});
    return response('<p>Saved to MyLinks demo. You may close this window.</p>', 201);
  }
  if (/^\d+$/.test(path)) {
    const id = Number(path), item = await get(id);
    if (!item) return error('Not found', 404);
    if (method === 'GET') return response(renderItem(item, url.searchParams.get('edit') === '1'));
    if (method === 'PATCH') {
      const form = await request.formData();
      const title = String(form.get('title') || '').trim(), description = String(form.get('description') || '');
      if (!title || title.length > 250 || description.length > 1020) return error('Invalid title or description');
      await put({...item, title, description});
      return response(renderItem({...item, title, description}));
    }
    if (method === 'DELETE') { await remove(id); return response(''); }
  }
  return error('Not found', 404);
}
self.addEventListener('fetch', event => {
  const url = new URL(event.request.url);
  if (url.origin !== scope.origin || !url.pathname.startsWith(scope.pathname)) return;
  const path = decodeURIComponent(url.pathname.slice(scope.pathname.length));
  if (path.startsWith('static/') || path === 'demo-sw.js') return;
  event.respondWith(handle(event.request, path, url).catch(err => error(`Demo storage failed: ${err.message}`, 500)));
});
