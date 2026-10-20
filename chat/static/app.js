// Memia — chat UI (vanilla JS, no deps). Security: NO innerHTML anywhere; markdown is parsed
// into DOM nodes via createElement/textContent (raw HTML is never interpreted).
(function () {
  'use strict';
  const $ = (s) => document.querySelector(s);
  const el = (tag, cls, text) => { const e = document.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; };
  const SVG = 'http://www.w3.org/2000/svg';
  const icon = (name) => { const s = document.createElementNS(SVG, 'svg'); s.setAttribute('class', 'ic'); s.setAttribute('width', '16'); s.setAttribute('height', '16'); s.setAttribute('aria-hidden', 'true'); const u = document.createElementNS(SVG, 'use'); u.setAttribute('href', '#i-' + name); s.appendChild(u); return s; };

  // ---------- login page ----------
  const err = $('#err');
  if (err && /[?&]e=1/.test(location.search)) err.hidden = false;
  const thread = $('#thread');
  if (!thread) return;

  // ---------- safe markdown ----------
  function inline(text, parent) {
    const re = /(`[^`]+`)|(\*\*[^*]+\*\*)|(\*[^*\s][^*]*\*)|(\[[^\]]+\]\((https?:\/\/[^\s)]+)\))|(\[\d{1,2}\])/g;
    let last = 0, m;
    while ((m = re.exec(text))) {
      if (m.index > last) parent.appendChild(document.createTextNode(text.slice(last, m.index)));
      const t = m[0];
      if (m[1]) parent.appendChild(el('code', null, t.slice(1, -1)));
      else if (m[2]) { const b = el('strong'); inline(t.slice(2, -2), b); parent.appendChild(b); }
      else if (m[3]) { const i = el('em'); inline(t.slice(1, -1), i); parent.appendChild(i); }
      else if (m[4]) { const a = el('a', null, t.slice(1, t.indexOf(']('))); a.href = m[5]; a.target = '_blank'; a.rel = 'noopener noreferrer nofollow'; parent.appendChild(a); }
      else if (m[6]) parent.appendChild(el('span', 'cite', t.slice(1, -1)));
      last = re.lastIndex;
    }
    if (last < text.length) parent.appendChild(document.createTextNode(text.slice(last)));
  }
  const isRow = (l) => /^\s*\|.*\|\s*$/.test(l);
  const cells = (l) => l.trim().replace(/^\||\|$/g, '').split('|').map((c) => c.trim());
  function markdown(src) {
    const root = document.createDocumentFragment();
    const lines = String(src || '').replace(/\r\n/g, '\n').split('\n');
    let i = 0, para = [];
    const flush = () => { if (para.length) { const p = el('p'); inline(para.join(' '), p); root.appendChild(p); para = []; } };
    while (i < lines.length) {
      const l = lines[i];
      if (/^```/.test(l)) {
        flush(); const buf = []; i++;
        while (i < lines.length && !/^```/.test(lines[i])) buf.push(lines[i++]);
        i++; const pre = el('pre'); pre.appendChild(el('code', null, buf.join('\n'))); root.appendChild(pre); continue;
      }
      if (isRow(l) && i + 1 < lines.length && /^\s*\|?\s*:?-{2,}/.test(lines[i + 1])) { // table
        flush(); const wrap = el('div', 'tbl'), t = el('table'), th = el('thead'), tr = el('tr');
        cells(l).forEach((c) => { const h = el('th'); inline(c, h); tr.appendChild(h); }); th.appendChild(tr); t.appendChild(th);
        const tb = el('tbody'); i += 2;
        while (i < lines.length && isRow(lines[i])) { const r = el('tr'); cells(lines[i]).forEach((c) => { const d = el('td'); inline(c, d); r.appendChild(d); }); tb.appendChild(r); i++; }
        t.appendChild(tb); wrap.appendChild(t); root.appendChild(wrap); continue;
      }
      let m;
      if ((m = /^(#{1,4})\s+(.*)/.exec(l))) { flush(); const h = el(m[1].length <= 2 ? 'h3' : 'h4'); inline(m[2], h); root.appendChild(h); i++; continue; }
      if (/^\s*([-*•]|\d+[.)])\s+/.test(l)) {
        flush(); const ordered = /^\s*\d/.test(l); const list = el(ordered ? 'ol' : 'ul');
        while (i < lines.length && /^\s*([-*•]|\d+[.)])\s+/.test(lines[i])) { const li = el('li'); inline(lines[i].replace(/^\s*([-*•]|\d+[.)])\s+/, ''), li); list.appendChild(li); i++; }
        root.appendChild(list); continue;
      }
      if (!l.trim()) { flush(); i++; continue; }
      para.push(l.trim()); i++;
    }
    flush();
    return root;
  }

  // ---------- API / history (contracts unchanged) ----------
  const LAST = 'ja-ai-conv', PREF = 'ja-ai-sidebar';
  let msgs = [], convId = null, convs = [], saving = Promise.resolve();
  const api = (path, opt) => fetch('/chat/api/' + path, Object.assign({ credentials: 'same-origin' }, opt || {})).then((r) => {
    if (r.status === 401) { location.href = '/chat'; throw new Error('auth'); }
    return r.json().catch(() => ({})).then((d) => { if (!r.ok) throw new Error(d.error || 'error'); return d; });
  });
  const jpost = (path, body, method) => api(path, { method: method || 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body || {}) });
  const TZ = 'America/Lima';
  const dayKey = (d) => d.toLocaleDateString('en-CA', { timeZone: TZ }); // YYYY-MM-DD in Lima
  const dayNum = (d) => Date.parse(dayKey(d) + 'T00:00:00Z') / 864e5;
  function group(s) {
    const diff = dayNum(new Date()) - dayNum(new Date(s));
    return diff <= 0 ? 'Hoy' : diff === 1 ? 'Ayer' : diff < 7 ? 'Esta semana' : 'Anteriores';
  }
  function shortDate(s) {
    const d = new Date(s), g = group(s);
    if (g === 'Hoy') return d.toLocaleTimeString('es-PE', { timeZone: TZ, hour: '2-digit', minute: '2-digit' });
    if (g === 'Ayer') return 'ayer';
    if (g === 'Esta semana') return d.toLocaleDateString('es-PE', { timeZone: TZ, weekday: 'short' });
    return d.toLocaleDateString('es-PE', { timeZone: TZ, day: 'numeric', month: 'short', year: dayKey(d).slice(0, 4) === dayKey(new Date()).slice(0, 4) ? undefined : 'numeric' });
  }
  const fullDate = (s) => new Date(s).toLocaleString('es-PE', { timeZone: TZ, dateStyle: 'medium', timeStyle: 'short' });

  // ---------- ConversationList ----------
  const search = $('#conv-search');
  const norm = (s) => s.normalize('NFD').replace(/[\u0300-\u036f]/g, '').toLowerCase();
  async function loadList() { try { const d = await api('conversations'); convs = d.conversations || []; } catch (e) { return; } drawList(); }
  function drawList() {
    const box = $('#conv-groups'); box.replaceChildren();
    const q = norm(search.value.trim());
    const list = convs.filter((c) => !q || norm(c.title).includes(q));
    $('#conv-empty').hidden = convs.length > 0; $('#conv-nomatch').hidden = !convs.length || list.length > 0;
    let cur = null, ul = null;
    list.forEach((c) => {
      const g = group(c.updated_at);
      if (g !== cur) { cur = g; box.appendChild(el('p', 'group-label', g)); ul = el('ul', 'conv-list'); box.appendChild(ul); }
      const li = el('li', 'conv' + (c.id === convId ? ' active' : ''));
      const open = el('button', 'conv-open'); open.type = 'button'; open.title = c.title + ' · ' + fullDate(c.updated_at) + ' · ' + c.messages + ' mensajes';
      if (c.id === convId) open.setAttribute('aria-current', 'page');
      open.append(icon('chat'), el('span', 'conv-title', c.title), el('span', 'conv-date', shortDate(c.updated_at)));
      open.addEventListener('click', () => { openConv(c.id); if (overlayMode()) setCollapsed(true); });
      const more = el('button', 'icon-btn conv-more'); more.type = 'button'; more.setAttribute('aria-label', 'Acciones de «' + c.title + '»');
      more.setAttribute('aria-haspopup', 'menu'); more.setAttribute('aria-expanded', 'false'); more.appendChild(icon('more'));
      more.addEventListener('click', (e) => { e.stopPropagation(); showMenu(more, c); });
      li.append(open, more); ul.appendChild(li);
    });
  }
  search.addEventListener('input', drawList);
  search.addEventListener('keydown', (e) => { if (e.key === 'Escape') { search.value = ''; drawList(); input.focus(); } });

  // contextual menu
  const pop = $('#menu-pop'); let popFor = null, popBtn = null;
  function showMenu(btn, c) {
    if (popFor === c) return hideMenu();
    popFor = c; popBtn = btn; btn.setAttribute('aria-expanded', 'true');
    const r = btn.getBoundingClientRect(); pop.hidden = false;
    pop.style.top = Math.min(r.bottom + 4, innerHeight - pop.offsetHeight - 8) + 'px';
    pop.style.left = Math.max(8, Math.min(r.right - pop.offsetWidth, innerWidth - pop.offsetWidth - 8)) + 'px';
    pop.querySelector('button').focus();
  }
  function hideMenu(refocus) { if (popBtn) { popBtn.setAttribute('aria-expanded', 'false'); if (refocus) popBtn.focus(); } pop.hidden = true; popFor = popBtn = null; }
  pop.addEventListener('click', async (e) => {
    const act = e.target.closest('button') && e.target.closest('button').dataset.act, c = popFor; hideMenu(); if (!c) return;
    if (act === 'rename') { const t = prompt('Nuevo nombre de la conversación:', c.title); if (t && t.trim()) { try { await jpost('conversations/' + c.id, { title: t }, 'PATCH'); } catch (x) { toast('No se pudo renombrar.'); } loadList(); if (c.id === convId) setTitle(t.trim()); } }
    if (act === 'delete') { if (!confirm('¿Eliminar «' + c.title + '»? No se puede deshacer.')) return;
      try { await api('conversations/' + c.id, { method: 'DELETE' }); } catch (x) { toast('No se pudo eliminar.'); } if (c.id === convId) newChat(); loadList(); }
  });
  pop.addEventListener('keydown', (e) => {
    const items = [...pop.querySelectorAll('button')], i = items.indexOf(document.activeElement);
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); items[(i + (e.key === 'ArrowDown' ? 1 : items.length - 1)) % items.length].focus(); }
    if (e.key === 'Escape' || e.key === 'Tab') { e.preventDefault(); hideMenu(true); }
  });
  document.addEventListener('click', (e) => { if (!pop.hidden && !pop.contains(e.target)) hideMenu(); });

  // ---------- sidebar collapse (rail + panel) ----------
  const app = $('#app'), scrim = $('#scrim');
  const overlayMode = () => innerWidth < 1200;
  const mobile = () => innerWidth < 768;
  function setCollapsed(c, persist) {
    app.classList.toggle('collapsed', c);
    const open = !c;
    ['#rail-toggle', '#menu'].forEach((s) => $(s).setAttribute('aria-expanded', String(open)));
    $('#rail-toggle').setAttribute('aria-label', open ? 'Contraer barra lateral' : 'Expandir barra lateral');
    $('#rail-toggle').dataset.tip = open ? 'Contraer barra lateral' : 'Expandir barra lateral';
    scrim.hidden = !(open && overlayMode());
    document.body.classList.toggle('drawer-open', open && mobile());
    if (persist && !overlayMode()) try { localStorage.setItem(PREF, c ? 'collapsed' : 'expanded'); } catch (e) {}
  }
  const prefCollapsed = () => overlayMode() || (function () { try { return localStorage.getItem(PREF) === 'collapsed'; } catch (e) { return false; } })();
  setCollapsed(prefCollapsed());
  let lastW = innerWidth;
  addEventListener('resize', () => { if ((lastW < 1200) !== (innerWidth < 1200) || (lastW < 768) !== (innerWidth < 768)) setCollapsed(prefCollapsed()); lastW = innerWidth; });
  const toggle = () => setCollapsed(!app.classList.contains('collapsed'), true);
  $('#rail-toggle').addEventListener('click', toggle);
  $('#side-close').addEventListener('click', () => setCollapsed(true, true));
  $('#menu').addEventListener('click', () => setCollapsed(false));
  scrim.addEventListener('click', () => setCollapsed(true));
  function openSearch() { setCollapsed(false, !overlayMode()); search.focus(); search.select(); }
  $('#rail-search').addEventListener('click', openSearch);
  $('#head-search').addEventListener('click', openSearch);
  $('#rail-chats').addEventListener('click', () => { setCollapsed(false, !overlayMode()); const a = document.querySelector('.conv.active .conv-open') || document.querySelector('.conv-open'); (a || search).focus(); });
  $('#rail-new').addEventListener('click', () => { newChat(); setMode('answer'); });
  $('#rail-notes').addEventListener('click', () => setMode('search'));
  document.addEventListener('keydown', (e) => {
    if ((e.ctrlKey || e.metaKey) && !e.altKey && e.key.toLowerCase() === 'k') { e.preventDefault(); openSearch(); }
    else if (e.key === 'Escape' && pop.hidden && !app.classList.contains('collapsed') && overlayMode()) { setCollapsed(true); (mobile() ? $('#menu') : $('#rail-toggle')).focus(); }
  });

  // ---------- ChatMessage ----------
  const scroller = $('#scroller');
  const nearBottom = () => scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 120;
  function byLabel(by) { return by === 'memoria' ? 'Solo memoria' : by === 'modelo local' ? 'Modelo local' : 'IA externa + memoria'; }
  function srcMeta(x) { return [x.kind === 'note' ? 'nota' : x.kind === 'pending' ? 'pendiente' : x.kind, x.project || null, x.date || null, x.ref].filter(Boolean).join(' · '); }
  const KIND = { note: ['Nota', 'notes'], pending: ['Pendiente', 'todo'], doc: ['Documento', 'file'] };
  function sourceList(list) {
    const d = el('details', 'sources'); const sm = el('summary'); sm.append(icon('db'), el('span', null, 'Fuentes (' + list.length + ')'), icon('chev')); d.appendChild(sm);
    list.forEach((x) => {
      const k = KIND[x.kind] || [x.kind ? x.kind.charAt(0).toUpperCase() + x.kind.slice(1) : 'Fuente', 'file'];
      const it = el('div', 'source-item'); it.appendChild(icon(k[1]));
      const body = el('div', 'source-body');
      body.appendChild(el('div', 'source-title', (x.title || 'Sin título')));
      body.appendChild(el('div', 'source-meta', ['[' + x.n + ']', k[0], x.project, x.ref].filter(Boolean).join(' · ')));
      if (x.excerpt) body.appendChild(el('div', 'source-ex', x.excerpt));
      it.appendChild(body);
      if (x.date) it.appendChild(el('span', 'source-date', x.date));
      d.appendChild(it);
    });
    return d;
  }
  function copyBtn(text) {
    const b = el('button', 'act icon-act'); b.type = 'button'; b.title = 'Copiar respuesta'; b.setAttribute('aria-label', 'Copiar respuesta'); b.append(icon('copy'));
    b.addEventListener('click', async () => {
      try { await navigator.clipboard.writeText(text); b.replaceChildren(icon('check')); b.title = 'Copiado'; toast('Respuesta copiada'); setTimeout(() => { b.replaceChildren(icon('copy')); b.title = 'Copiar respuesta'; }, 1600); }
      catch (e) { toast('No se pudo copiar.'); }
    });
    return b;
  }
  function render(m, opts) {
    empty.hidden = true;
    const stick = nearBottom();
    const row = el('article', 'msg ' + (m.role === 'user' ? 'user' : 'bot') + (m.error ? ' error' : ''));
    row.setAttribute('aria-label', m.role === 'user' ? 'Tu mensaje' : 'Respuesta del asistente');
    const b = el('div', 'bubble');
    if (m.role === 'user') b.textContent = m.text;
    else if (m.kind === 'hits') {
      const top = el('div', 'bubble-top'); top.appendChild(el('span', 'by by-memoria', 'Búsqueda en notas')); b.appendChild(top);
      if (!m.hits.length) b.appendChild(el('p', 'muted', 'No encontré nada en tus notas.'));
      else b.appendChild(el('p', null, 'Encontré ' + m.hits.length + ' resultado' + (m.hits.length > 1 ? 's' : '') + ':'));
      m.hits.forEach((h, i) => { const d = el('div', 'hit'); d.appendChild(el('div', 'hit-title', '[' + (i + 1) + '] ' + (h.title || 'Sin título'))); d.appendChild(el('div', 'hit-meta', srcMeta(h))); d.appendChild(el('div', 'hit-body', h.content.length > 600 ? h.content.slice(0, 600) + '…' : h.content)); b.appendChild(d); });
      if (!m.hits.length && m.reason) b.appendChild(el('p', 'reason', 'Motivo: ' + m.reason));
    } else {
      const top = el('div', 'bubble-top');
      if (m.by) top.appendChild(el('span', 'by by-' + m.by.replace(/\s+/g, '-'), byLabel(m.by)));
      if (!m.error && m.text) top.appendChild(copyBtn(m.text));
      if (top.childNodes.length) b.appendChild(top);
      b.appendChild(markdown(m.text));
      if (m.sources && m.sources.length) b.appendChild(sourceList(m.sources));
      else if (m.reason) b.appendChild(el('p', 'reason', 'Motivo: ' + m.reason));
    }
    if (m.role === 'user') row.appendChild(b);
    else { const wrap = el('div', 'bot-col'); wrap.appendChild(b); const av = el('span', 'msg-avatar', 'M'); av.setAttribute('aria-hidden', 'true'); row.append(av, wrap); }
    if (m.role !== 'user') {
      const f = el('div', 'msg-foot');
      if (m.meta) f.appendChild(el('span', 'meta', m.meta));
      if (opts && opts.retry) { const r = el('button', 'act retry', 'Reintentar'); r.type = 'button'; r.addEventListener('click', () => { row.remove(); opts.retry(); }); f.appendChild(r); }
      if (f.childNodes.length) row.querySelector('.bot-col').appendChild(f);
    }
    thread.appendChild(row);
    if (stick || m.role === 'user') scroller.scrollTop = scroller.scrollHeight;
    return row;
  }
  function toast(t) { const x = $('#toast'); x.textContent = t; x.hidden = false; clearTimeout(toast.t); toast.t = setTimeout(() => (x.hidden = true), 2200); }
  let sessCost = 0, sessTok = 0;
  function addCost(u) {
    if (!u || !u.tokens) return; sessCost += u.cost_usd || 0; sessTok += u.tokens || 0;
    const c = $('#session-cost'); c.hidden = false; c.textContent = 'Esta sesión: ' + sessTok.toLocaleString('es-PE') + ' tokens · $' + sessCost.toFixed(5);
  }
  function typing() {
    const row = el('article', 'msg bot'); row.setAttribute('aria-label', 'El asistente está escribiendo');
    const b = el('div', 'bubble'); const t = el('div', 'typing'); t.append(el('i'), el('i'), el('i')); b.appendChild(t);
    const av = el('span', 'msg-avatar', 'M'); const w = el('div', 'bot-col'); w.appendChild(b); row.append(av, w); thread.appendChild(row); scroller.scrollTop = scroller.scrollHeight; return row;
  }

  // ---------- conversation state ----------
  const empty = $('#empty');
  function setTitle(t) { document.title = (t ? t + ' · ' : 'Chat · ') + 'Memia'; }
  function clearThread() { thread.querySelectorAll('.msg').forEach((n) => n.remove()); }
  async function openConv(id) {
    let d; try { d = await api('conversations/' + encodeURIComponent(id)); } catch (e) { try { localStorage.removeItem(LAST); } catch (x) {} return false; }
    convId = d.id; try { localStorage.setItem(LAST, convId); } catch (e) {} msgs = d.messages || [];
    clearThread(); empty.hidden = msgs.length > 0; msgs.forEach((m) => render(m)); scroller.scrollTop = scroller.scrollHeight;
    setTitle(d.title); convs.length ? drawList() : loadList(); return true;
  }
  function newChat() { convId = null; msgs = []; try { localStorage.removeItem(LAST); } catch (e) {} clearThread(); empty.hidden = false; setTitle(''); drawList(); input.focus(); }
  function persist(batch) {
    saving = saving.then(async () => {
      if (!convId) { const c = await jpost('conversations', {}); convId = c.id; try { localStorage.setItem(LAST, convId); } catch (e) {} }
      const r = await jpost('conversations/' + convId + '/messages', { messages: batch });
      if (r && r.title) setTitle(r.title);
      loadList();
    }).catch(() => toast('No se pudo guardar en el historial.'));
    return saving;
  }
  function push(m, opts) { msgs.push(m); render(m, opts); if (!(opts && opts.ephemeral)) persist([m]); }

  // ---------- ChatComposer ----------
  let mode = 'answer';
  const MODES = { answer: 'Escribe tu pregunta…', search: '¿Qué quieres buscar en tus notas?', ingest: 'Escribe la nota (la primera línea será el título)…' };
  const input = $('#input'), send = $('#send');
  const mem = $('#memory-only');
  function setMode(m) {
    mode = m;
    document.querySelectorAll('.mode').forEach((b) => { const on = b.dataset.mode === m; b.classList.toggle('active', on); b.setAttribute('aria-checked', String(on)); });
    $('#rail-notes').classList.toggle('active', m === 'search');
    input.placeholder = MODES[m];
    mem.closest('.toggle').hidden = m !== 'answer';
    syncBadge(); if (mobile()) setCollapsed(true); input.focus();
  }
  function syncBadge() { $('#memory-badge').hidden = !(mem.checked && mode === 'answer'); }
  mem.addEventListener('change', syncBadge);
  function autosize() { input.style.height = 'auto'; input.style.height = Math.min(input.scrollHeight, 220) + 'px'; send.disabled = busy || !input.value.trim(); }
  let busy = false;

  async function run(text, m) {
    busy = true; autosize();
    const t = typing();
    let body;
    if (m === 'ingest') { const first = text.split('\n')[0].slice(0, 80); body = { title: first, source: 'note', text, metadata: { kind: 'note', client: 'chat' } }; }
    else body = { query: text, k: 5, project: $('#project').value || undefined };
    if (m === 'answer' && mem.checked) body.mode = 'memory';
    if (m === 'ingest' && $('#project').value && $('#project').value !== '(sin proyecto)') body.project = $('#project').value;
    const retry = () => run(text, m);
    try {
      const r = await fetch('/chat/api/' + m, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body), credentials: 'same-origin' });
      if (r.status === 401) { location.href = '/chat'; return; }
      const d = await r.json().catch(() => ({}));
      t.remove();
      if (!r.ok) { push({ role: 'bot', error: true, text: d.error || 'Algo salió mal. Inténtalo de nuevo.' }, { retry, ephemeral: true }); return; }
      if (m === 'answer') {
        const u = d.usage || {}; addCost(u);
        const meta = (d.sources || []).length + ' fuente(s)' + (u.tokens ? ' · ' + u.tokens.toLocaleString('es-PE') + ' tokens · $' + (u.cost_usd || 0).toFixed(5) : ' · sin coste');
        push({ role: 'bot', text: d.answer || '', sources: d.sources || [], by: d.answered_by, reason: d.reason, meta });
      } else if (m === 'search') push({ role: 'bot', kind: 'hits', hits: d.hits || [], reason: d.reason, meta: (d.hits || []).length + ' resultado(s) · memoria · sin coste' });
      else if (!d.ref) push({ role: 'bot', error: true, text: 'No pude confirmar que la nota se guardara. Inténtalo de nuevo.' }, { retry, ephemeral: true });
      else {
        const when = d.created_at ? fullDate(d.created_at) : '';
        push({ role: 'bot', text: (d.duplicate ? 'Esa nota ya estaba guardada' : 'Nota guardada') + (d.indexed ? ' e indexada' : '') + ': «' + (d.title || '') + '»' + (when ? ' · ' + when : '') + '. Pregúntame «resume lo último que guardé» para verla.',
          meta: 'doc ' + d.ref + ' · ' + (d.chunks || 1) + ' fragmento(s)' });
      }
    } catch (e) { t.remove(); push({ role: 'bot', error: true, text: 'Sin conexión. Inténtalo de nuevo.' }, { retry, ephemeral: true }); }
    finally { busy = false; autosize(); }
  }
  function submit(text) {
    text = text.trim(); if (!text || busy) return;
    push({ role: 'user', text: (mode === 'ingest' ? '📝 ' : mode === 'search' ? '🔎 ' : '') + text });
    input.value = ''; autosize(); input.focus();
    run(text, mode);
  }
  input.addEventListener('input', autosize);
  input.addEventListener('keydown', (e) => { if (e.key === 'Enter' && !e.shiftKey && !e.isComposing && e.keyCode !== 229) { e.preventDefault(); submit(input.value); } });
  $('#composer').addEventListener('submit', (e) => { e.preventDefault(); submit(input.value); });
  document.querySelectorAll('.mode').forEach((b) => b.addEventListener('click', () => setMode(b.dataset.mode)));
  $('.chips').addEventListener('keydown', (e) => { // radiogroup arrow navigation
    if (e.key !== 'ArrowRight' && e.key !== 'ArrowLeft') return;
    const items = [...document.querySelectorAll('.mode')], i = items.indexOf(document.activeElement); if (i < 0) return;
    const n = items[(i + (e.key === 'ArrowRight' ? 1 : items.length - 1)) % items.length]; setMode(n.dataset.mode); n.focus();
  });
  document.querySelectorAll('.sugg').forEach((b) => b.addEventListener('click', () => {
    setMode(b.dataset.mode);
    submit(b.dataset.mode === 'search' ? b.textContent.replace(/^Buscar\s*«|»$/g, '') : b.textContent);
  }));
  $('#new-chat').addEventListener('click', () => { newChat(); setMode('answer'); if (overlayMode()) setCollapsed(true); });

  fetch('/chat/api/projects', { credentials: 'same-origin' }).then((r) => (r.ok ? r.json() : { projects: [] })).then((d) => {
    const sel = $('#project'); sel.value = '';
    (d.projects || []).filter((p) => p.documents > 0).forEach((p) => { const o = el('option', null, p.name + ' (' + p.documents + ')'); o.value = p.name; sel.appendChild(o); });
  }).catch(() => {});

  setMode('answer'); autosize();
  (async () => {
    let old = []; try { old = JSON.parse(sessionStorage.getItem('ja-ai-thread-v1') || '[]'); } catch (e) {}
    if (old.length) { sessionStorage.removeItem('ja-ai-thread-v1'); msgs = old; old.forEach((m) => render(m)); for (let i = 0; i < old.length; i += 10) persist(old.slice(i, i + 10)); await saving; return; }
    let last = null; try { last = localStorage.getItem(LAST); } catch (e) {}
    if (!(last && (await openConv(last)))) { empty.hidden = false; loadList(); }
  })();
})();
