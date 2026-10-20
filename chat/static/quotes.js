// Cotizador IA (Fase B) — vanilla JS, no deps, NO innerHTML. Loaded only when CHAT_COMMERCIAL_QUOTES_AI=1.
// All amounts come from the backend calculator (/calculate, quote.totals); the UI never computes money.
(function () {
  'use strict';
  const $ = (s, r) => (r || document).querySelector(s);
  const el = (tag, cls, text) => { const e = document.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; };
  const SVG = 'http://www.w3.org/2000/svg';
  const icon = (name, sz) => { const s = document.createElementNS(SVG, 'svg'); s.setAttribute('class', 'ic'); s.setAttribute('width', sz || 16); s.setAttribute('height', sz || 16); s.setAttribute('aria-hidden', 'true'); const u = document.createElementNS(SVG, 'use'); u.setAttribute('href', '#i-' + name); s.appendChild(u); return s; };
  const app = $('#app'), main = $('#main');
  if (!app || !main) return;

  // ---------- API ----------
  async function api(path, method, body) {
    const opt = { method: method || 'GET', credentials: 'same-origin', headers: {} };
    if (body !== undefined) { opt.headers['Content-Type'] = 'application/json'; opt.body = JSON.stringify(body); }
    let r;
    try { r = await fetch('/chat/api/commercial/' + path, opt); } catch (e) { throw new Error('Sin conexión con el servidor.'); }
    let d = null; try { d = await r.json(); } catch (e) {}
    if (!r.ok) { const er = new Error((d && d.error) || 'Algo salió mal. Inténtalo de nuevo.'); er.status = r.status; throw er; }
    return d;
  }

  // ---------- labels / formatting ----------
  const STATUS = { draft: 'Borrador', pending_approval: 'Pendiente de aprobación', approved: 'Aprobada', sent: 'Enviada', accepted: 'Aceptada', rejected: 'Rechazada', expired: 'Vencida', cancelled: 'Anulada' };
  const CYCLES = [['one_time', 'Pago único'], ['monthly', 'Mensual'], ['annual', 'Anual'], ['usage', 'Variable / por uso']];
  const CYC = Object.fromEntries(CYCLES); const CYC_SHORT = { one_time: 'único', monthly: '/mes', annual: '/año', usage: 'variable' };
  const TAX = [['gravado', 'Gravado'], ['exonerado', 'Exonerado'], ['inafecto', 'Inafecto']];
  const DOCS = ['RUC', 'DNI', 'CE', 'PAS', 'OTRO'];
  const sym = (c) => (c === 'USD' ? 'US$' : 'S/');
  function money(cur, v) { // string formatting only (no floats)
    if (v == null || v === '') return '—';
    const neg = String(v).startsWith('-'); const [i, d] = String(v).replace('-', '').split('.');
    return (neg ? '-' : '') + sym(cur) + ' ' + i.replace(/\B(?=(\d{3})+(?!\d))/g, ',') + '.' + ((d || '') + '00').slice(0, 2);
  }
  const fdate = (s) => { if (!s) return ''; const d = new Date(/^\d{4}-\d{2}-\d{2}$/.test(s) ? s + 'T12:00:00-05:00' : s); return isNaN(d) ? s : d.toLocaleDateString('es-PE', { day: '2-digit', month: 'short', year: 'numeric', timeZone: 'America/Lima' }); };
  const toast = (t) => { const x = $('#toast'); if (!x) return; x.textContent = t; x.hidden = false; clearTimeout(toast.t); toast.t = setTimeout(() => (x.hidden = true), 2400); };
  const badge = (s) => el('span', 'q-status q-st-' + s, STATUS[s] || s);
  function field(label, input, cls) { const l = el('label', 'q-field' + (cls ? ' ' + cls : '')); l.appendChild(el('span', 'q-label', label)); l.appendChild(input); return l; }
  function inp(val, attrs) { const i = el('input', 'q-in'); i.value = val == null ? '' : val; Object.entries(attrs || {}).forEach(([k, v]) => i.setAttribute(k, v)); return i; }
  function sel(opts, val, cls) { const s = el('select', 'q-in' + (cls ? ' ' + cls : '')); opts.forEach(([v, t]) => { const o = el('option', null, t); o.value = v; s.appendChild(o); }); s.value = val; return s; }
  function btn(text, cls, ic) { const b = el('button', 'q-btn ' + (cls || '')); b.type = 'button'; if (ic) b.appendChild(icon(ic, 15)); b.appendChild(document.createTextNode(text)); return b; }
  function busyBtn(b, on, text) { b.disabled = on; if (on) { b.dataset.t = b.lastChild.textContent; b.lastChild.textContent = text; b.classList.add('busy'); } else if (b.dataset.t) { b.lastChild.textContent = b.dataset.t; b.classList.remove('busy'); } }

  // ---------- shell: rail + sidebar entries, view, panel ----------
  const railBtn = el('button', 'rail-btn'); railBtn.id = 'rail-quotes'; railBtn.type = 'button';
  railBtn.setAttribute('aria-label', 'Cotizador IA'); railBtn.dataset.tip = 'Cotizador IA'; railBtn.appendChild(icon('quote', 18));
  const notes = $('#rail-notes'); notes ? notes.after(railBtn) : $('.rail').appendChild(railBtn);
  const sideLink = el('button', 'side-link'); sideLink.type = 'button'; sideLink.id = 'side-quotes';
  sideLink.appendChild(icon('quote', 16)); sideLink.appendChild(document.createTextNode('Cotizaciones'));
  $('#new-chat').after(sideLink);

  const view = el('section', 'q-view'); view.id = 'q-view'; view.hidden = true; view.setAttribute('aria-label', 'Cotizaciones');
  main.appendChild(view);
  const panel = el('aside', 'q-panel'); panel.id = 'q-panel'; panel.hidden = true; panel.setAttribute('aria-label', 'Editor de cotización');
  app.appendChild(panel);
  const head = { title: $('#head-title'), sub: $('.head-sub'), ic: $('.head-ic use') };
  const chatHead = { title: head.title && head.title.textContent, sub: head.sub && head.sub.textContent };

  let me = null, active = false, list = [], filter = { q: '', status: '' };
  function enter(push) {
    active = true; app.classList.add('quotes-mode'); railBtn.classList.add('active'); sideLink.classList.add('active');
    const rc = $('#rail-chats'); if (rc) rc.classList.remove('active');
    view.hidden = false; $('#scroller').hidden = true; $('#composer').hidden = true;
    if (head.title) head.title.textContent = 'Cotizaciones'; if (head.sub) head.sub.textContent = 'Cotizador IA · borradores, aprobación y versiones';
    if (head.ic) head.ic.setAttribute('href', '#i-quote');
    document.title = 'Cotizaciones · Memia';
    if (push && location.hash !== '#cotizaciones') history.replaceState(null, '', '#cotizaciones');
    if (window.matchMedia('(max-width:1199px)').matches) { app.classList.add('collapsed'); const s = $('#scrim'); if (s) s.hidden = true; document.body.classList.remove('drawer-open'); }
    loadList();
  }
  function leave() {
    if (!active) return; active = false; app.classList.remove('quotes-mode'); railBtn.classList.remove('active'); sideLink.classList.remove('active');
    const rc = $('#rail-chats'); if (rc) rc.classList.add('active');
    view.hidden = true; closePanel(true); $('#scroller').hidden = false; $('#composer').hidden = false;
    if (head.title) head.title.textContent = chatHead.title; if (head.sub) head.sub.textContent = chatHead.sub; if (head.ic) head.ic.setAttribute('href', '#i-chat');
    if (/^#cotizaci/.test(location.hash)) history.replaceState(null, '', location.pathname);
  }
  railBtn.addEventListener('click', () => (active ? loadList() : enter(true)));
  sideLink.addEventListener('click', () => enter(true));
  document.addEventListener('click', (e) => { if (active && e.target.closest('#rail-chats,#rail-new,#new-chat,.conv-open,#rail-notes,.sugg')) leave(); }, true);

  // ---------- list ----------
  function viewShell() {
    view.replaceChildren();
    const bar = el('div', 'q-toolbar');
    const s = inp(filter.q, { type: 'search', placeholder: 'Buscar por número, cliente o título', 'aria-label': 'Buscar cotizaciones' }); s.classList.add('q-search');
    let t; s.addEventListener('input', () => { clearTimeout(t); t = setTimeout(() => { filter.q = s.value.trim(); loadList(); }, 300); });
    const st = sel([['', 'Todos los estados']].concat(Object.entries(STATUS)), filter.status, 'q-filter'); st.setAttribute('aria-label', 'Filtrar por estado');
    st.addEventListener('change', () => { filter.status = st.value; loadList(); });
    const nb = btn('Nueva cotización', 'primary', 'plus'); nb.id = 'q-new'; nb.addEventListener('click', () => openEditor(null));
    if (me && !me.permissions.includes('quotes.edit')) nb.hidden = true;
    const cfg = btn('Configuración', 'ghost', 'more'); cfg.id = 'q-cfg'; cfg.addEventListener('click', openSettings);
    if (!me || !me.permissions.includes('settings.manage')) cfg.hidden = true;
    bar.append(s, st, cfg, nb);
    const body = el('div', 'q-list'); body.id = 'q-list';
    view.append(bar, body); return body;
  }
  function skeleton(n) { const w = el('div', 'q-skel'); w.setAttribute('aria-busy', 'true'); w.appendChild(el('span', 'sr', 'Cargando…')); for (let i = 0; i < n; i++) w.appendChild(el('div', 'q-skel-row')); return w; }
  function stateBox(kind, title, text, action) {
    const b = el('div', 'q-state q-state-' + kind); b.appendChild(icon(kind === 'error' ? 'x' : 'quote', 22)); b.appendChild(el('h3', null, title)); if (text) b.appendChild(el('p', null, text));
    if (action) b.appendChild(action); return b;
  }
  function cycleSummary(q) {
    const c = (q.totals && q.totals.cycles) || {}; const parts = [];
    CYCLES.forEach(([k]) => { if (c[k] && c[k].lines) parts.push([money(q.currency, c[k].total), CYC_SHORT[k]]); });
    return parts;
  }
  async function loadList() {
    if (!active) return;
    const body = viewShell(); body.appendChild(skeleton(5));
    try {
      if (!me) me = await api('me');
      $('#q-new').hidden = !me.permissions.includes('quotes.edit'); $('#q-cfg').hidden = !me.permissions.includes('settings.manage');
      const qs = new URLSearchParams(); if (filter.q) qs.set('q', filter.q); if (filter.status) qs.set('status', filter.status);
      const d = await api('quotes' + (qs.toString() ? '?' + qs : '')); list = d.quotes || [];
      drawList(body);
    } catch (e) {
      const retry = btn('Reintentar', '', null); retry.addEventListener('click', loadList);
      body.replaceChildren(stateBox('error', e.status === 403 ? 'Sin acceso al Cotizador' : 'No se pudo cargar', e.status === 403 ? 'Tu cuenta no tiene un rol comercial asignado.' : e.message, e.status === 403 ? null : retry));
    }
  }
  function drawList(body) {
    body.replaceChildren();
    if (!list.length) {
      const filtered = filter.q || filter.status;
      const nb = btn('Crear la primera cotización', 'primary', 'plus'); nb.addEventListener('click', () => openEditor(null));
      body.appendChild(stateBox('empty', filtered ? 'Sin resultados' : 'Aún no hay cotizaciones', filtered ? 'Prueba con otro texto o estado.' : 'Crea un borrador, agrega conceptos y el sistema calcula los totales por ciclo.', filtered || (me && !me.permissions.includes('quotes.edit')) ? null : nb));
      return;
    }
    const tbl = el('div', 'q-table'); tbl.setAttribute('role', 'table');
    const hr = el('div', 'q-tr q-th'); hr.setAttribute('role', 'row');
    ['Número', 'Cliente / título', 'Estado', 'Totales', 'Actualizada', 'PDF'].forEach((h) => { const c = el('span', null, h); c.setAttribute('role', 'columnheader'); hr.appendChild(c); });
    tbl.appendChild(hr);
    list.forEach((q) => {
      const r = el('div', 'q-tr'); r.tabIndex = 0; r.setAttribute('role', 'row'); r.dataset.id = q.id;
      r.addEventListener('keydown', (e) => { if ((e.key === 'Enter' || e.key === ' ') && e.target === r) { e.preventDefault(); openEditor(q.id); } });
      const n = el('span', 'q-num', q.number || 'Borrador'); if (!q.number) n.classList.add('muted');
      const who = el('span', 'q-who'); who.appendChild(el('strong', null, (q.customer && q.customer.display_name) || '—')); who.appendChild(el('small', null, q.title || 'Sin título'));
      const st = el('span', 'q-cell-st'); st.appendChild(badge(q.status)); if (q.version > 1) st.appendChild(el('small', 'q-ver', 'v' + q.version));
      const tot = el('span', 'q-tot'); const cs = cycleSummary(q);
      if (!cs.length) tot.appendChild(el('small', 'muted', 'Sin conceptos'));
      cs.forEach(([a, b]) => { const x = el('span', 'q-tot-i'); x.appendChild(el('b', null, a)); x.appendChild(el('small', null, ' ' + b)); tot.appendChild(x); });
      const dl = el('span', 'q-row-dl');
      if (q.approved_version && me.permissions.includes('quotes.edit')) {
        const old = !EXPORTABLE.includes(q.status);
        const b = el('button', 'icon-btn q-pdf-btn'); b.type = 'button'; b.appendChild(icon('file', 16)); b.appendChild(el('span', null, 'PDF'));
        const lab = 'Descargar PDF' + (old ? ' de la última versión aprobada (v' + q.approved_version + ')' : '') + ' · ' + (q.number || '');
        b.title = lab; b.setAttribute('aria-label', lab); if (old) b.appendChild(el('small', null, 'v' + q.approved_version));
        b.addEventListener('click', (e) => { e.stopPropagation(); download(q.id, 'pdf', old ? q.approved_version : 0, b); });
        dl.appendChild(b);
      }
      r.append(n, who, st, tot, el('span', 'q-date', fdate(q.updated_at)), dl);
      r.addEventListener('click', () => openEditor(q.id));
      tbl.appendChild(r);
    });
    body.appendChild(tbl);
  }

  // ---------- editor (artifact panel) ----------
  let ed = null; // {q, d (draft), totals, dirty, calcSeq}
  const blankItem = () => ({ description: '', billing_mode: 'one_time', unit: 'und', qty: '1', unit_price: '', discount_pct: '', tax_rule: 'gravado' });
  function closePanel(silent) {
    if (!silent && ed && ed.dirty && !confirm('Hay cambios sin guardar. ¿Cerrar de todos modos?')) return;
    panel.hidden = true; app.classList.remove('q-panel-open'); ed = null;
    if (app.classList.contains('q-autocollapsed')) app.classList.remove('collapsed', 'q-autocollapsed');
    if (active) history.replaceState(null, '', '#cotizaciones');
    view.querySelectorAll('.q-tr.sel').forEach((n) => n.classList.remove('sel'));
  }
  async function openEditor(id) {
    if (ed && ed.dirty && !confirm('Hay cambios sin guardar. ¿Descartarlos?')) return;
    panel.hidden = false; app.classList.add('q-panel-open');
    if (window.matchMedia('(max-width:1599px)').matches && !app.classList.contains('collapsed')) { app.classList.add('collapsed', 'q-autocollapsed'); } panel.replaceChildren(panelHead('Cargando…')); panel.appendChild(skeleton(6));
    view.querySelectorAll('.q-tr').forEach((n) => n.classList.toggle('sel', n.dataset.id === id));
    try {
      if (!me) me = await api('me');
      if (id) { ed = fromQuote(await api('quotes/' + id)); history.replaceState(null, '', '#cotizacion/' + id); }
      else {
        const s = await api('settings');
        ed = { q: null, dirty: false, d: { title: '', customer: { kind: 'free', id: '', data: { display_name: '', document_type: 'RUC', tax_id: '' } }, currency: s.default_currency, prices_include_igv: s.prices_include_igv, igv_rate: s.igv_rate, global_discount_pct: '0', valid_until: '', terms: '', items: [blankItem()] }, totals: null };
        history.replaceState(null, '', '#cotizaciones');
      }
      drawEditor(); if (!id) recalc(); else ed.totals = ed.q.totals;
      drawTotals();
    } catch (e) {
      const retry = btn('Reintentar', '', null); retry.addEventListener('click', () => openEditor(id));
      panel.replaceChildren(panelHead('Cotización'), stateBox('error', e.status === 404 ? 'No encontrada' : 'No se pudo abrir', e.message, e.status === 404 ? null : retry));
    }
  }
  function fromQuote(q) {
    const c = q.customer || {}; const data = Object.assign({ display_name: '', document_type: 'RUC', tax_id: '' }, c.data || {}, { display_name: c.display_name || (c.data && c.data.display_name) || '' });
    return { q, dirty: false, totals: q.totals, d: { title: q.title, customer: { kind: q.customer_kind, id: q.customer_id || '', data }, currency: q.currency, prices_include_igv: q.prices_include_igv,
      igv_rate: String(q.igv_rate).replace(/0+$/, '').replace(/\.$/, ''), global_discount_pct: String(q.global_discount_pct).replace(/\.?0+$/, '') || '0', valid_until: q.valid_until || '', terms: q.terms || '',
      items: (q.items || []).map((i) => ({ description: i.description, billing_mode: i.billing_mode, unit: i.unit, qty: i.qty, unit_price: i.unit_price, discount_pct: i.discount_pct === '0' ? '' : i.discount_pct, tax_rule: i.tax_rule })) } };
  }
  const editable = () => (!ed.q || ed.q.status === 'draft') && me.permissions.includes('quotes.edit');
  function panelHead(title, q) {
    const h = el('header', 'q-phead');
    const t = el('div', 'q-ptitle'); t.appendChild(el('h2', null, title)); if (q) { t.appendChild(badge(q.status)); if (q.version > 1) t.appendChild(el('small', 'q-ver', 'Versión ' + q.version)); }
    const x = el('button', 'icon-btn'); x.type = 'button'; x.setAttribute('aria-label', 'Cerrar editor'); x.appendChild(icon('x', 18)); x.addEventListener('click', () => closePanel(false));
    h.append(t, x); return h;
  }
  function touch() { if (ed) { ed.dirty = true; const s = $('#q-dirty'); if (s) s.hidden = false; } }
  function section(title, extra) { const s = el('section', 'q-sec'); const h = el('div', 'q-sec-h'); h.appendChild(el('h3', null, title)); if (extra) h.appendChild(extra); s.appendChild(h); return s; }

  function drawEditor() {
    const q = ed.q, d = ed.d, ro = !editable();
    panel.replaceChildren(panelHead(q ? (q.number || 'Borrador') : 'Nueva cotización', q));
    const hdl = downloadBar(q); if (hdl) panel.appendChild(hdl);
    const body = el('div', 'q-pbody'); panel.appendChild(body);
    if (q && q.status === 'pending_approval') body.appendChild(el('p', 'q-note warn', 'En espera de aprobación. No se puede editar hasta que se apruebe o se devuelva.'));
    else if (q && q.status !== 'draft') body.appendChild(el('p', 'q-note', 'Solo lectura. Para modificarla crea una revisión (se conserva el número y la versión aprobada).'));
    if (!me.permissions.includes('quotes.edit')) body.appendChild(el('p', 'q-note', 'Tu rol solo permite ver cotizaciones.'));

    // título
    const ti = inp(d.title, { maxlength: '200', placeholder: 'Ej. Implementación PBX sede Lima' }); ti.disabled = ro;
    ti.addEventListener('input', () => { d.title = ti.value; touch(); });
    body.appendChild(field('Título', ti, 'wide'));

    // cliente
    const cs = section('Cliente'); body.appendChild(cs); drawCustomer(cs, ro);

    // conceptos
    const add = btn('Agregar concepto', 'ghost sm', 'plus'); add.hidden = ro;
    const is = section('Conceptos', add); body.appendChild(is);
    const rows = el('div', 'q-items'); is.appendChild(rows);
    const drawItems = () => {
      rows.replaceChildren();
      if (!d.items.length) rows.appendChild(el('p', 'q-empty-items', ro ? 'Sin conceptos.' : 'Agrega al menos un concepto para calcular totales.'));
      d.items.forEach((it, idx) => rows.appendChild(itemRow(it, idx, ro, drawItems)));
    };
    add.addEventListener('click', () => { d.items.push(blankItem()); touch(); drawItems(); const last = rows.lastChild && $('.q-desc', rows.lastChild); if (last) last.focus(); });
    drawItems();

    // condiciones
    const co = section('Condiciones'); body.appendChild(co);
    const grid = el('div', 'q-grid');
    const cur = sel([['PEN', 'PEN · Soles'], ['USD', 'USD · Dólares']], d.currency); cur.addEventListener('change', () => { d.currency = cur.value; touch(); recalc(); });
    const inc = sel([['false', 'Precios sin IGV'], ['true', 'Precios incluyen IGV']], String(d.prices_include_igv)); inc.addEventListener('change', () => { d.prices_include_igv = inc.value === 'true'; touch(); recalc(); });
    const igv = inp(d.igv_rate, { inputmode: 'decimal', 'aria-describedby': 'q-igv-h' }); igv.addEventListener('input', () => { d.igv_rate = igv.value.trim(); touch(); recalc(); });
    const gd = inp(d.global_discount_pct, { inputmode: 'decimal' }); gd.addEventListener('input', () => { d.global_discount_pct = gd.value.trim() || '0'; touch(); recalc(); });
    const vu = inp(d.valid_until, { type: 'date' }); vu.addEventListener('change', () => { d.valid_until = vu.value; touch(); });
    [cur, inc, igv, gd, vu].forEach((x) => (x.disabled = ro));
    grid.append(field('Moneda', cur), field('IGV', inc), field('Tasa IGV (0.18 = 18 %)', igv), field('Descuento global %', gd), field(q ? 'Vigente hasta' : 'Vigente hasta (vacío = según configuración)', vu));
    co.appendChild(grid);
    const te = el('textarea', 'q-in q-terms'); te.rows = 3; te.value = d.terms; te.placeholder = 'Forma de pago, plazos de entrega, garantía…'; te.disabled = ro; te.maxLength = 4000;
    te.addEventListener('input', () => { d.terms = te.value; touch(); });
    co.appendChild(field('Términos y condiciones', te, 'wide'));

    // totales
    const ts = section('Totales', el('small', 'muted', 'Calculados por el servidor')); ts.id = 'q-totals'; body.appendChild(ts);

    // footer / actions
    const foot = el('footer', 'q-pfoot');
    const dirty = el('span', 'q-dirty', 'Cambios sin guardar'); dirty.id = 'q-dirty'; dirty.hidden = !ed.dirty;
    const acts = el('div', 'q-acts');
    const exp = exportBar(q);
    const st = q ? q.status : 'draft', can = (p) => me.permissions.includes(p);
    if (st === 'draft' && can('quotes.edit')) {
      const sv = btn('Guardar borrador', 'ghost', 'check'); sv.id = 'q-save'; sv.addEventListener('click', () => save(sv));
      const fast = can('quotes.approve');
      const ra = btn('Solicitar aprobación', fast ? 'ghost' : 'primary', 'send'); ra.id = 'q-req'; ra.addEventListener('click', () => act('request-approval', ra, true));
      acts.append(sv, ra);
      if (fast) { const ag = btn('Aprobar y generar PDF', 'primary', 'file'); ag.id = 'q-approve-pdf'; ag.addEventListener('click', () => approveAndPdf(ag)); acts.append(ag); }
      foot.appendChild(el('p', 'q-hint', 'El PDF se genera al aprobar: la versión aprobada queda congelada y el PDF refleja exactamente esos importes.'));
    }
    if (st === 'pending_approval' && can('quotes.approve')) {
      const rt = btn('Devolver', 'ghost', 'x'); rt.addEventListener('click', () => { const why = prompt('Motivo de la devolución:'); if (why && why.trim()) act('return', rt, false, why.trim()); });
      const ap = btn('Aprobar', 'primary', 'check'); ap.id = 'q-approve'; ap.addEventListener('click', () => act('approve', ap)); acts.append(rt, ap); }
    if (st === 'approved' && can('quotes.edit')) { const sd = btn('Marcar enviada', 'ghost', 'send'); sd.addEventListener('click', () => act('send', sd)); acts.append(sd); }
    if (st === 'sent' && can('quotes.edit')) { const ac = btn('Aceptada', 'ghost', 'check'); ac.addEventListener('click', () => act('accept', ac)); const rj = btn('Rechazada', 'ghost', 'x'); rj.addEventListener('click', () => { const why = prompt('Motivo del rechazo:'); if (why && why.trim()) act('reject', rj, false, why.trim()); }); acts.append(ac, rj); }
    if (st === 'pending_approval' && !can('quotes.approve')) acts.append(el('span', 'muted', 'Esperando a un aprobador.'));
    if (['approved', 'sent', 'rejected', 'expired'].includes(st) && can('quotes.edit')) { const rv = btn('Revisar', 'ghost', 'notes'); rv.id = 'q-revise'; rv.addEventListener('click', () => act('revise', rv)); acts.append(rv); }
    foot.prepend(dirty, exp); foot.appendChild(acts); panel.appendChild(foot);
    const err = el('p', 'q-err'); err.id = 'q-err'; err.setAttribute('role', 'alert'); err.hidden = true; foot.before(err);
  }

  function itemRow(it, idx, ro, redraw) {
    const r = el('div', 'q-item'); const d = ed.d;
    const num = el('span', 'q-item-n', String(idx + 1));
    const desc = inp(it.description, { placeholder: 'Descripción del concepto', maxlength: '500', 'aria-label': 'Descripción concepto ' + (idx + 1) }); desc.classList.add('q-desc');
    const mode = sel(CYCLES, it.billing_mode); mode.setAttribute('aria-label', 'Ciclo');
    const unit = inp(it.unit, { maxlength: '20', 'aria-label': 'Unidad' }); unit.classList.add('q-unit');
    const qty = inp(it.qty, { inputmode: 'decimal', 'aria-label': 'Cantidad' }); qty.classList.add('q-num-in');
    const price = inp(it.unit_price, { inputmode: 'decimal', placeholder: '0.00', 'aria-label': 'Precio unitario' }); price.classList.add('q-num-in');
    const disc = inp(it.discount_pct, { inputmode: 'decimal', placeholder: '0', 'aria-label': 'Descuento %' }); disc.classList.add('q-num-in');
    const tax = sel(TAX, it.tax_rule); tax.setAttribute('aria-label', 'Tipo de afectación');
    const del = el('button', 'icon-btn q-del'); del.type = 'button'; del.setAttribute('aria-label', 'Quitar concepto ' + (idx + 1)); del.appendChild(icon('x', 15)); del.hidden = ro;
    const line = el('span', 'q-line-total'); line.dataset.n = idx + 1;
    const bind = (x, k, calc) => x.addEventListener(x.tagName === 'SELECT' ? 'change' : 'input', () => { it[k] = x.value.trim ? x.value.trim() : x.value; if (k === 'description') it[k] = x.value; touch(); if (calc) recalc(); });
    bind(desc, 'description', false); bind(mode, 'billing_mode', true); bind(unit, 'unit', false); bind(qty, 'qty', true); bind(price, 'unit_price', true); bind(disc, 'discount_pct', true); bind(tax, 'tax_rule', true);
    [desc, mode, unit, qty, price, disc, tax].forEach((x) => (x.disabled = ro));
    del.addEventListener('click', () => { d.items.splice(idx, 1); touch(); redraw(); recalc(); });
    const hint = el('div', 'q-cat-hint'); hint.hidden = true;
    if (!ro) desc.addEventListener('blur', async () => {
      const t = (it.description || '').trim(); if (t.length < 4) { hint.hidden = true; return; }
      try {
        const m = ((await api('catalog?q=' + encodeURIComponent(t))).services || [])[0]; const p = m && m.current_price;
        if (!m || !p || p.currency !== d.currency) { hint.hidden = true; return; }
        const pr = String(p.unit_price).replace(/\.?0+$/, '');
        if (it.billing_mode === m.billing_mode && it.unit_price === pr) { hint.hidden = true; return; }
        hint.replaceChildren(el('span', null, 'Catálogo: ' + m.name + ' · ' + money(p.currency, pr) + ' ' + (CYC_SHORT[m.billing_mode] || '') + (p.price_includes_igv ? ' (IGV incl.)' : ' + IGV') + (p.verified ? '' : ' · por confirmar')));
        const ap = btn('Aplicar', 'ghost sm', 'check'); ap.addEventListener('click', () => { Object.assign(it, { billing_mode: m.billing_mode, unit: m.unit || it.unit, unit_price: pr, tax_rule: m.tax_rule || it.tax_rule }); touch(); redraw(); recalc(); });
        hint.appendChild(ap); hint.hidden = false;
      } catch (e) { hint.hidden = true; }
    });
    const top = el('div', 'q-item-top'); top.append(num, desc, del);
    const g = el('div', 'q-item-grid');
    g.append(field('Ciclo', mode), field('Unidad', unit), field('Cant.', qty), field('Precio unit. ' + sym(d.currency), price), field('Desc. %', disc), field('Afectación', tax), line);
    r.append(top, hint, g); return r;
  }

  function drawCustomer(cs, ro) {
    const c = ed.d.customer;
    const box = el('div', 'q-cust'); cs.appendChild(box);
    const seg = el('div', 'q-seg'); seg.setAttribute('role', 'radiogroup'); seg.setAttribute('aria-label', 'Tipo de cliente');
    [['local', 'Cliente registrado'], ['free', 'Cliente libre']].forEach(([k, t]) => {
      const b = el('button', 'q-seg-b' + (c.kind === k ? ' on' : ''), t); b.type = 'button'; b.setAttribute('role', 'radio'); b.setAttribute('aria-checked', String(c.kind === k)); b.disabled = ro;
      b.addEventListener('click', () => { if (c.kind === k) return; c.kind = k; if (k === 'local') { c.id = ''; } touch(); box.remove(); drawCustomer(cs, ro); });
      seg.appendChild(b);
    });
    if (ro) { // read-only: compact snapshot card
      const card = el('div', 'q-cust-card'); card.appendChild(icon('user', 16)); const t = el('div');
      t.appendChild(el('strong', null, c.data.display_name || '—'));
      t.appendChild(el('small', null, [c.kind === 'local' ? 'Cliente registrado' : 'Cliente libre', [c.data.document_type, c.data.tax_id].filter(Boolean).join(' '), c.data.email].filter(Boolean).join(' · ')));
      card.appendChild(t); box.appendChild(card); return;
    }
    box.appendChild(seg);
    if (c.kind === 'local') {
      if (c.id) {
        const card = el('div', 'q-cust-card'); card.appendChild(icon('user', 16));
        const t = el('div'); t.appendChild(el('strong', null, c.data.display_name || 'Cliente')); t.appendChild(el('small', null, [c.data.document_type, c.data.tax_id].filter(Boolean).join(' ') || 'Sin documento')); card.appendChild(t);
        if (!ro) { const ch = btn('Cambiar', 'ghost sm'); ch.addEventListener('click', () => { c.id = ''; touch(); box.remove(); drawCustomer(cs, ro); }); card.appendChild(ch); }
        box.appendChild(card); return;
      }
      const s = inp('', { type: 'search', placeholder: 'Buscar por nombre o RUC/DNI', 'aria-label': 'Buscar cliente' });
      const res = el('div', 'q-cust-res'); res.setAttribute('role', 'listbox');
      const nc = btn('Crear cliente', 'ghost sm', 'plus'); nc.hidden = !me.permissions.includes('customers.manage');
      const row = el('div', 'q-cust-search'); row.append(s, nc); box.append(row, res);
      let t, seq = 0;
      const search = async () => {
        const my = ++seq; res.replaceChildren(el('p', 'muted q-sm', 'Buscando…'));
        try {
          const d = await api('customers' + (s.value.trim() ? '/search?q=' + encodeURIComponent(s.value.trim()) : ''));
          if (my !== seq) return; res.replaceChildren();
          const arr = d.customers || [];
          if (!arr.length) res.appendChild(el('p', 'muted q-sm', s.value.trim() ? 'Sin coincidencias. Puedes crearlo o usar «Cliente libre».' : 'Aún no hay clientes registrados.'));
          arr.slice(0, 8).forEach((cu) => {
            const o = el('button', 'q-cust-opt'); o.type = 'button'; o.setAttribute('role', 'option');
            o.appendChild(el('strong', null, cu.display_name)); o.appendChild(el('small', null, [cu.document_type, cu.tax_id].filter(Boolean).join(' ')));
            o.addEventListener('click', () => { c.id = cu.id; c.data = { display_name: cu.display_name, document_type: cu.document_type, tax_id: cu.tax_id }; touch(); box.remove(); drawCustomer(cs, ro); });
            res.appendChild(o);
          });
        } catch (e) { if (my === seq) res.replaceChildren(el('p', 'q-err-inline', e.message)); }
      };
      s.addEventListener('input', () => { clearTimeout(t); t = setTimeout(search, 300); });
      nc.addEventListener('click', () => { row.hidden = true; res.replaceChildren(); res.appendChild(customerForm({ display_name: s.value.trim(), document_type: 'RUC', tax_id: '' }, async (data, b, err) => {
        busyBtn(b, true, 'Creando…');
        try { const cu = await api('customers', 'POST', data); c.id = cu.id; c.data = data; touch(); toast('Cliente creado'); box.remove(); drawCustomer(cs, ro); }
        catch (e) { busyBtn(b, false); err.textContent = e.message; err.hidden = false; }
      }, () => { row.hidden = false; search(); })); });
      if (!ro) search();
    } else {
      box.appendChild(el('p', 'muted q-sm', 'Datos solo para esta cotización (no se guarda como cliente).'));
      box.appendChild(customerFields(c.data, ro, touch));
    }
  }
  function customerFields(data, ro, onChange) {
    const g = el('div', 'q-grid');
    const f = (k, label, attrs, cls) => { const i = inp(data[k], attrs); i.disabled = ro; i.addEventListener('input', () => { data[k] = i.value; onChange(); }); g.appendChild(field(label, i, cls)); return i; };
    f('display_name', 'Nombre o razón social *', { maxlength: '200' }, 'wide');
    const dt = sel(DOCS.map((x) => [x, x]), data.document_type || 'RUC'); dt.disabled = ro; dt.addEventListener('change', () => { data.document_type = dt.value; onChange(); }); g.appendChild(field('Documento', dt));
    f('tax_id', 'Número', { inputmode: 'numeric', maxlength: '20' });
    f('email', 'Correo', { type: 'email', maxlength: '200' }); f('phone', 'Teléfono', { type: 'tel', maxlength: '40' });
    f('address', 'Dirección', { maxlength: '300' }, 'wide');
    return g;
  }
  function customerForm(data, onSave, onCancel) {
    const w = el('div', 'q-newcust'); w.appendChild(el('h4', null, 'Nuevo cliente'));
    w.appendChild(customerFields(data, false, () => {}));
    const err = el('p', 'q-err-inline'); err.hidden = true; err.setAttribute('role', 'alert');
    const a = el('div', 'q-row-end'); const cancel = btn('Cancelar', 'ghost sm'); const ok = btn('Guardar cliente', 'primary sm', 'check');
    cancel.addEventListener('click', () => { w.remove(); onCancel(); });
    ok.addEventListener('click', () => { err.hidden = true; if (!data.display_name || !data.display_name.trim()) { err.textContent = 'Indica el nombre o razón social.'; err.hidden = false; return; } onSave(clean(data), ok, err); });
    a.append(cancel, ok); w.append(err, a); return w;
  }
  const clean = (o) => Object.fromEntries(Object.entries(o).filter(([, v]) => v != null && String(v).trim() !== '').map(([k, v]) => [k, String(v).trim()]));

  // ---------- totals (backend) ----------
  const itemsPayload = () => ed.d.items.map((i) => clean({ description: i.description, billing_mode: i.billing_mode, unit: i.unit || 'und', qty: i.qty, unit_price: i.unit_price, discount_pct: i.discount_pct, tax_rule: i.tax_rule }));
  let calcT;
  function recalc() {
    if (!ed) return; clearTimeout(calcT); const box = $('#q-totals'); if (box) box.classList.add('stale');
    calcT = setTimeout(async () => {
      if (!ed) return; const seq = (ed.calcSeq = (ed.calcSeq || 0) + 1);
      const lines = itemsPayload().filter((l) => l.qty && l.unit_price);
      if (!lines.length) { ed.totals = null; ed.calcErr = null; drawTotals(); return; }
      try { const t = await api('calculate', 'POST', { currency: ed.d.currency, prices_include_igv: ed.d.prices_include_igv, igv_rate: ed.d.igv_rate || '0', global_discount_pct: ed.d.global_discount_pct || '0', lines });
        if (ed && seq === ed.calcSeq) { ed.totals = t; ed.calcErr = null; drawTotals(); } }
      catch (e) { if (ed && seq === ed.calcSeq) { ed.calcErr = e.message; drawTotals(); } }
    }, 350);
  }
  function drawTotals() {
    const box = $('#q-totals'); if (!box || !ed) return; box.classList.remove('stale');
    box.querySelectorAll('.q-cycles,.q-err-inline,.q-notes,.q-sm').forEach((n) => n.remove());
    if (ed.calcErr) { box.appendChild(el('p', 'q-err-inline', ed.calcErr)); return; }
    const t = ed.totals, cur = ed.d.currency;
    if (!t || !t.cycles || !Object.keys(t.cycles).length) { box.appendChild(el('p', 'muted q-sm', 'Ingresa cantidad y precio para ver los totales.')); return; }
    (t.lines || []).forEach((l) => { const s = panel.querySelector('.q-line-total[data-n="' + l.n + '"]'); if (s) s.textContent = money(cur, l.total); });
    const wrap = el('div', 'q-cycles');
    CYCLES.forEach(([k, label]) => {
      const c = t.cycles[k]; if (!c) return;
      const card = el('div', 'q-cycle'); const h = el('div', 'q-cycle-h'); h.appendChild(el('strong', null, label)); h.appendChild(el('small', 'muted', c.lines + (c.lines === 1 ? ' concepto' : ' conceptos'))); card.appendChild(h);
      const dl = el('dl');
      const row = (a, b, cls) => { const dt = el('dt', cls, a), dd = el('dd', cls, money(cur, b)); dl.append(dt, dd); };
      if (c.discount !== '0.00') row('Descuentos', '-' + c.discount);
      if (c.op_gravada !== '0.00') row('Op. gravada', c.op_gravada);
      if (c.op_exonerada !== '0.00') row('Op. exonerada', c.op_exonerada);
      if (c.op_inafecta !== '0.00') row('Op. inafecta', c.op_inafecta);
      row('IGV', c.igv); row('Total ' + CYC_SHORT[k], c.total, 'q-total');
      card.appendChild(dl); wrap.appendChild(card);
    });
    box.appendChild(wrap);
    if (Object.keys(t.cycles).length > 1) box.appendChild(el('p', 'q-notes', 'Los ciclos se muestran por separado: no se suman pagos únicos con recurrentes.'));
    (t.notes || []).forEach((n) => box.appendChild(el('p', 'q-notes', n)));
  }

  // ---------- save / transitions ----------
  function payload() {
    const d = ed.d, c = d.customer;
    const cust = c.kind === 'local' ? { kind: 'local', id: c.id } : { kind: 'free', data: clean(c.data) };
    const p = { title: d.title.trim(), customer: cust, currency: d.currency, prices_include_igv: d.prices_include_igv, igv_rate: d.igv_rate, global_discount_pct: d.global_discount_pct || '0', terms: d.terms, items: itemsPayload() };
    if (d.valid_until) p.valid_until = d.valid_until; else if (ed.q) p.valid_until = '';
    return p;
  }
  function showErr(m) { const e = $('#q-err'); if (e) { e.textContent = m; e.hidden = !m; } }
  function validate() {
    const c = ed.d.customer;
    if (c.kind === 'local' && !c.id) return 'Selecciona un cliente registrado o usa «Cliente libre».';
    if (c.kind === 'free' && !(c.data.display_name || '').trim()) return 'Indica el nombre del cliente.';
    const bad = ed.d.items.findIndex((i) => !i.description.trim() || !i.qty || !i.unit_price);
    if (bad >= 0) return 'Completa descripción, cantidad y precio del concepto ' + (bad + 1) + '.';
    return '';
  }
  async function save(b, quiet) {
    showErr(''); const v = validate(); if (v) { showErr(v); return null; }
    if (b) busyBtn(b, true, 'Guardando…');
    try {
      const q = ed.q ? await api('quotes/' + ed.q.id, 'PATCH', payload()) : await api('quotes', 'POST', payload());
      ed = fromQuote(q); history.replaceState(null, '', '#cotizacion/' + q.id);
      if (!quiet) { drawEditor(); drawTotals(); toast('Borrador guardado'); }
      refreshRow(); return q;
    } catch (e) { if (b) busyBtn(b, false); showErr(e.message); return null; }
  }
  async function act(action, b, saveFirst, reason) {
    showErr('');
    if (action === 'approve' && !confirm('¿Aprobar esta cotización? Se asignará el número definitivo y se congelará la versión.')) return;
    busyBtn(b, true, action === 'approve' ? 'Aprobando…' : action === 'revise' ? 'Creando revisión…' : 'Enviando…');
    try {
      if (saveFirst && (ed.dirty || !ed.q)) { if (!(await save(null, true))) { busyBtn(b, false); return; } }
      const q = await api('quotes/' + ed.q.id + '/' + action, 'POST', reason ? { reason } : {});
      ed = fromQuote(q); drawEditor(); drawTotals(); refreshRow();
      toast(action === 'approve' ? 'Aprobada · ' + q.number : action === 'revise' ? 'Revisión v' + q.version + ' creada' : action === 'request-approval' ? 'Enviada a aprobación' : 'Estado: ' + (STATUS[q.status] || q.status));
    } catch (e) { busyBtn(b, false); showErr(e.message); }
  }
  function refreshRow() { if (active) { const keep = ed && ed.q && ed.q.id; loadList().then(() => { if (keep) view.querySelectorAll('.q-tr').forEach((n) => n.classList.toggle('sel', n.dataset.id === keep)); }); } }


  // ---------- export buttons (approved+) ----------
  const EXPORTABLE = ['approved', 'sent', 'accepted'];
  async function download(id, fmt, version, b) {
    if (b) busyBtn(b, true, 'Preparando…'); showErr('');
    try {
      const m = await api('quotes/' + id + '/export', 'POST', version ? { format: fmt, version } : { format: fmt });
      if (b) busyBtn(b, false); location.assign('/chat/api/commercial/' + m.url); return m;
    } catch (e) { if (b) busyBtn(b, false); showErr(e.message); toast(e.message); }
  }
  function exportBar() { return el('span', 'q-exp-row'); }
  function downloadBar(q) {
    if (!q || !me.permissions.includes('quotes.edit')) return null;
    const cur = EXPORTABLE.includes(q.status); const v = cur ? 0 : q.approved_version;
    if (!cur && !v) return null;
    const w = el('div', 'q-hdl');
    const lab = el('div', 'q-hdl-t');
    lab.appendChild(el('strong', null, cur ? 'Documento aprobado' : 'Última versión aprobada (v' + v + ')'));
    lab.appendChild(el('small', null, cur ? 'Importes verificados contra la versión ' + q.version + '.' : 'Esta revisión (v' + q.version + ') aún no está aprobada; se descarga la v' + v + '.'));
    const pdf = btn(cur ? 'Descargar PDF' : 'PDF v' + v, 'primary sm', 'file'); pdf.id = 'q-dl-pdf';
    const doc = btn(cur ? 'Descargar Word' : 'Word v' + v, 'ghost sm', 'file'); doc.id = 'q-dl-docx';
    pdf.addEventListener('click', () => download(q.id, 'pdf', v, pdf)); doc.addEventListener('click', () => download(q.id, 'docx', v, doc));
    const bs = el('div', 'q-hdl-b'); bs.append(pdf, doc); w.append(lab, bs); return w;
  }
  async function approveAndPdf(b) {
    showErr(''); const bad = validate(); if (bad) { showErr(bad); return; }
    const t = ed.totals || (ed.q && ed.q.totals); const cs = t ? cycleSummary({ totals: t, currency: ed.d.currency }).map(([x, y]) => x + ' ' + y).join(' + ') : '';
    const who = (ed.d.customer && ed.d.customer.data && ed.d.customer.data.display_name) || 'sin cliente';
    if (!confirm('¿Aprobar y generar el PDF?\n\nCliente: ' + who + '\nTotal: ' + (cs || '—') + ' (calculado por el servidor)\n\nSe asigna el número definitivo y la versión queda congelada.')) return;
    busyBtn(b, true, 'Aprobando…');
    try {
      if (ed.dirty || !ed.q) { if (!(await save(null, true))) { busyBtn(b, false); return; } }
      let q = await api('quotes/' + ed.q.id + '/request-approval', 'POST', {});
      q = await api('quotes/' + q.id + '/approve', 'POST', {});
      ed = fromQuote(q); drawEditor(); drawTotals(); refreshRow(); toast('Aprobada · ' + q.number + ' · generando PDF…');
      await download(q.id, 'pdf', 0, $('#q-dl-pdf'));
    } catch (e) { busyBtn(b, false); showErr(e.message); }
  }
  function dlLink(m) {
    const a = el('a', 'q-dl'); a.href = '/chat/api/commercial/' + m.url; a.setAttribute('download', m.filename);
    a.appendChild(icon('file', 14)); a.appendChild(document.createTextNode(m.filename)); a.title = 'Plantilla ' + m.template_version + ' · sha256 ' + m.sha256.slice(0, 12) + ' · importes verificados contra la versión aprobada';
    return a;
  }

  // ---------- conversational quote (chat intent) ----------
  const INTENT = /\b(cotiza\w*|cotizaci[oó]n\w*|presupuesto|proforma)\b/i;
  const thread = $('#thread'), input = $('#input');
  function intercept(e) {
    if (!input || !input.value.trim() || !INTENT.test(input.value)) return false;
    const modeBtn = document.querySelector('.mode.active'); if (modeBtn && modeBtn.dataset.mode !== 'answer') return false;
    e.preventDefault(); e.stopImmediatePropagation();
    const text = input.value.trim(); input.value = ''; input.dispatchEvent(new Event('input'));
    chatQuote(text); return true;
  }
  document.addEventListener('keydown', (e) => { if (e.target === input && e.key === 'Enter' && !e.shiftKey && !e.isComposing && e.keyCode !== 229) intercept(e); }, true);
  document.addEventListener('submit', (e) => { if (e.target && e.target.id === 'composer') intercept(e); }, true);

  function botShell() {
    const emp = $('#empty'); if (emp) emp.hidden = true;
    const m = el('div', 'msg bot'); m.appendChild(el('div', 'msg-avatar', 'M')); const col = el('div', 'bot-col'); const b = el('div', 'bubble qi'); col.appendChild(b); m.appendChild(col);
    thread.appendChild(m); const sc = $('#scroller'); if (sc) sc.scrollTop = sc.scrollHeight; return b;
  }
  async function chatQuote(text) {
    const u = el('div', 'msg user'); u.appendChild(el('div', 'bubble', text)); const emp = $('#empty'); if (emp) emp.hidden = true; thread.appendChild(u);
    const b = botShell(); b.appendChild(el('p', 'muted', 'Preparando el borrador de cotización…'));
    try {
      if (!me) me = await api('me');
      const r = await api('intent', 'POST', { text });
      drawCard(b, r);
    } catch (e) { b.replaceChildren(el('p', 'q-err-inline', e.status === 403 ? 'Tu cuenta no tiene acceso al Cotizador.' : e.message)); }
    const sc = $('#scroller'); if (sc) sc.scrollTop = sc.scrollHeight;
  }
  const pct = (c) => Math.round(c * 100) + ' %';
  function drawCard(b, r) {
    b.replaceChildren();
    const d = r.draft; d.items = d.items || [];
    const top = el('div', 'bubble-top'); top.appendChild(el('span', 'by', 'Cotizador IA')); top.appendChild(el('span', 'q-status q-st-draft', 'Borrador sin guardar')); b.appendChild(top);
    b.appendChild(el('h3', 'qi-h', 'Borrador de cotización'));
    const grid = el('div', 'qi-grid');
    // customer
    const cust = el('div', 'qi-sec'); cust.appendChild(el('span', 'q-label', 'Cliente'));
    const c = r.customer || {}; let legal = null;
    if (c.status === 'missing' || c.status === 'invalid') {
      cust.appendChild(el('p', 'q-err-inline', c.status === 'invalid' ? 'El documento indicado no es válido.' : 'Falta el cliente: indica su RUC o DNI.'));
      const ti = inp('', { placeholder: 'RUC o DNI del cliente', inputmode: 'numeric', maxlength: '11' }); cust.appendChild(ti); legal = { doc: ti };
    } else {
      cust.appendChild(el('strong', null, c.display_name)); cust.appendChild(el('small', 'muted', [c.document_type + ' ' + c.tax_id, c.source].filter(Boolean).join(' · ')));
      if (c.status === 'unverified') { const li = inp('', { placeholder: 'Razón social (opcional, si la conoces)', maxlength: '200' }); cust.appendChild(li); legal = { name: li }; }
    }
    grid.appendChild(cust);
    // items
    const its = el('div', 'qi-sec'); its.appendChild(el('span', 'q-label', 'Concepto'));
    const m0 = (r.matches || [])[0];
    let row = d.items[0];
    let saveCat = null, priceIn = null, descIn = null, modeSel = null;
    if (!row) { row = { description: '', unit: 'und', qty: '1', unit_price: '', billing_mode: 'monthly', tax_rule: 'gravado' }; d.items.push(row);
      descIn = inp('', { placeholder: 'Descripción del servicio', maxlength: '300' }); its.appendChild(descIn);
      modeSel = sel(CYCLES, 'monthly'); its.appendChild(field('Ciclo', modeSel)); }
    else { its.appendChild(el('strong', null, row.description)); if (m0 && m0.description) its.appendChild(el('small', 'muted', m0.description)); }
    const qty = inp(row.qty, { inputmode: 'decimal', 'aria-label': 'Cantidad' }); qty.classList.add('q-num-in');
    const line = el('div', 'qi-line'); line.appendChild(field('Cantidad', qty));
    if (row.unit_price) {
      const p = m0 && m0.price; const pv = el('div', 'qi-price'); pv.appendChild(el('strong', null, money(d.currency, row.unit_price) + ' ' + CYC_SHORT[row.billing_mode] + (d.prices_include_igv ? ' (inc. IGV)' : ' + IGV')));
      const src = el('small', 'qi-src');
      src.textContent = 'Origen: catálogo' + (p ? ' · vigente desde ' + fdate(p.valid_from) + (p.valid_to ? ' hasta ' + fdate(p.valid_to) : '') : '') + ' · confianza ' + pct(m0.confidence) + (p && !p.verified ? ' · por confirmar' : '');
      pv.appendChild(src);
      const docs = p && p.source && p.source.documents; if (docs && docs.length) pv.appendChild(el('small', 'qi-src', 'Fuente: ' + docs.map((x) => x.title).join('; ')));
      line.appendChild(pv);
    } else {
      priceIn = inp('', { inputmode: 'decimal', placeholder: '0.00', 'aria-label': 'Precio unitario' }); priceIn.classList.add('q-num-in');
      line.appendChild(field('Precio unit. ' + sym(d.currency) + (d.prices_include_igv ? ' (inc. IGV)' : ' (sin IGV)'), priceIn));
      const lab = el('label', 'qi-check'); saveCat = el('input'); saveCat.type = 'checkbox'; saveCat.checked = true; lab.append(saveCat, document.createTextNode(m0 ? ' Guardar este precio en el catálogo' : ' Crear este servicio en el catálogo'));
      if (!me.permissions.includes('settings.manage')) { saveCat.checked = false; lab.hidden = true; }
      its.appendChild(el('p', 'q-note warn', 'No tengo un precio vigente' + (m0 ? ' para «' + m0.name + '»' : '') + '. Indícalo una vez; no invento precios.'));
      line.appendChild(lab);
    }
    its.appendChild(line); grid.appendChild(its);
    b.appendChild(grid);
    const tot = el('div', 'qi-tot'); b.appendChild(tot);
    (r.warnings || []).forEach((w) => b.appendChild(el('p', 'qi-warn', w)));
    if (r.send && r.send.requested) b.appendChild(el('p', 'q-note', 'Envío por correo' + (r.send.to ? ' a ' + r.send.to : '') + ': ' + (r.send.available ? 'se pedirá confirmación antes de enviar.' : 'no configurado (falta SMTP). Generaré el PDF para descarga.')));
    const err = el('p', 'q-err-inline'); err.hidden = true; err.setAttribute('role', 'alert'); b.appendChild(err);
    const acts = el('div', 'qi-acts'); b.appendChild(acts);
    const sync = () => { row.qty = qty.value.trim(); if (priceIn) row.unit_price = priceIn.value.trim(); if (descIn) row.description = descIn.value; if (modeSel) row.billing_mode = modeSel.value; };
    let ct; const recalcCard = () => { clearTimeout(ct); ct = setTimeout(async () => { sync(); if (!row.qty || !row.unit_price) { tot.replaceChildren(el('small', 'muted', 'Totales: indica cantidad y precio.')); return; }
      try { const t = await api('calculate', 'POST', { currency: d.currency, prices_include_igv: d.prices_include_igv, igv_rate: d.igv_rate, lines: [clean({ description: row.description || 'x', qty: row.qty, unit_price: row.unit_price, billing_mode: row.billing_mode, tax_rule: row.tax_rule, unit: row.unit })] });
        tot.replaceChildren(); Object.entries(t.cycles).forEach(([k, v]) => { const x = el('div', 'qi-tot-c'); x.appendChild(el('span', null, (CYC[k] || k) + ': base ' + money(d.currency, (MustAdd(v)))+ ' · IGV ' + money(d.currency, v.igv))); x.appendChild(el('strong', null, 'Total ' + CYC_SHORT[k] + ' ' + money(d.currency, v.total))); tot.appendChild(x); });
        tot.appendChild(el('small', 'muted', 'Calculado por el servidor.')); }
      catch (e) { tot.replaceChildren(el('small', 'q-err-inline', e.message)); } }, 300); };
    [qty, priceIn, descIn, modeSel].forEach((x) => x && x.addEventListener(x.tagName === 'SELECT' ? 'change' : 'input', recalcCard)); recalcCard();

    async function prepare() {
      sync(); err.hidden = true;
      if (legal && legal.doc) { const v = legal.doc.value.replace(/\D/g, ''); if (!v) throw new Error('Indica el RUC o DNI del cliente.');
        d.customer = { kind: 'free', data: { display_name: (v.length === 11 ? 'RUC ' : 'DNI ') + v, document_type: v.length === 11 ? 'RUC' : 'DNI', tax_id: v } }; }
      if (legal && legal.name && legal.name.value.trim()) { d.customer.data.display_name = legal.name.value.trim(); d.customer.data.legal_name = legal.name.value.trim(); }
      if (!d.customer) throw new Error('Falta el cliente.');
      if (!row.description.trim()) throw new Error('Indica la descripción del servicio.');
      if (!row.qty || !row.unit_price) throw new Error('Indica cantidad y precio.');
      if (saveCat && saveCat.checked) {
        let sid = m0 && m0.service_id;
        if (!sid) { const code = row.description.normalize('NFD').replace(/[\u0300-\u036f]/g, '').toUpperCase().replace(/[^A-Z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 32) || 'SERVICIO';
          const s = await api('catalog', 'POST', { code, name: row.description.trim(), billing_mode: row.billing_mode, unit: row.unit || 'und', tax_rule: row.tax_rule }); sid = s.id; }
        const p = await api('catalog/' + sid + '/prices', 'POST', { currency: d.currency, unit_price: row.unit_price, price_includes_igv: !!d.prices_include_igv, source: { note: 'Ingresado desde el chat' } });
        row.price_source = { kind: 'catalog', service_id: sid, price_version_id: p.id, valid_from: p.valid_from, verified: true, origin: p.source };
        saveCat.checked = false; saveCat.parentNode.hidden = true;
      } else if (priceIn && !row.price_source) row.price_source = { kind: 'manual' };
      return d;
    }
    async function createDraft() { const q = await api('quotes', 'POST', d); return q; }
    const open = btn('Abrir en editor', 'ghost sm', 'notes');
    open.addEventListener('click', async () => { busyBtn(open, true, 'Guardando…'); try { await prepare(); const q = await createDraft(); busyBtn(open, false); enter(true); openEditor(q.id); } catch (e) { busyBtn(open, false); err.textContent = e.message; err.hidden = false; } });
    acts.appendChild(open);
    const canApprove = me.permissions.includes('quotes.approve');
    const go = btn(canApprove ? 'Aprobar y generar PDF' : 'Guardar y solicitar aprobación', 'primary sm', canApprove ? 'check' : 'send');
    go.addEventListener('click', async () => {
      err.hidden = true;
      try { await prepare(); } catch (e) { err.textContent = e.message; err.hidden = false; return; }
      if (canApprove && !confirm('Se aprobará la cotización (número definitivo, versión congelada) y se generará el PDF. ¿Continuar?')) return;
      busyBtn(go, true, 'Procesando…'); open.disabled = true;
      try {
        let q = await createDraft();
        q = await api('quotes/' + q.id + '/request-approval', 'POST', {});
        if (!canApprove) { acts.replaceChildren(el('p', 'q-note', 'Enviada a aprobación como borrador. Un aprobador debe aprobarla.')); return; }
        q = await api('quotes/' + q.id + '/approve', 'POST', {});
        const m = await api('quotes/' + q.id + '/export', 'POST', { format: 'pdf' });
        top.lastChild.className = 'q-status q-st-approved'; top.lastChild.textContent = 'Aprobada · ' + q.number;
        acts.replaceChildren();
        const dl = dlLink(m); dl.classList.add('primary'); dl.firstChild.nextSibling.textContent = 'Descargar PDF ' + q.number; acts.appendChild(dl);
        const view = btn('Ver cotización', 'ghost sm', 'notes'); view.addEventListener('click', () => { enter(true); openEditor(q.id); }); acts.appendChild(view);
        if (r.send && r.send.requested && !r.send.available) acts.appendChild(el('small', 'muted', 'Correo no configurado: descarga el PDF y envíalo manualmente.'));
      } catch (e) { busyBtn(go, false); open.disabled = false; err.textContent = e.message; err.hidden = false; }
    });
    acts.appendChild(go);
  }
  function MustAdd(v) { // display-only: base = gravada + exonerada + inafecta, as decimal strings (no floats)
    const parts = [v.op_gravada, v.op_exonerada, v.op_inafecta].map((s) => { const [i, f] = String(s).split('.'); return BigInt(i) * 100n + BigInt(((f || '') + '00').slice(0, 2)) * (String(s).startsWith('-') ? -1n : 1n); });
    const t = parts.reduce((a, b) => a + b, 0n); const neg = t < 0n; const ab = neg ? -t : t; return (neg ? '-' : '') + (ab / 100n) + '.' + String(ab % 100n).padStart(2, '0');
  }

  // ---------- configuración (admin): empresa, términos, serie, catálogo ----------
  async function openSettings() {
    if (ed && ed.dirty && !confirm('Hay cambios sin guardar. ¿Descartarlos?')) return;
    ed = null; panel.hidden = false; app.classList.add('q-panel-open'); panel.replaceChildren(panelHead('Configuración')); panel.appendChild(skeleton(6));
    try {
      const [co, se, st, cat] = await Promise.all([api('company'), api('series'), api('settings'), api('catalog?all=1')]);
      const body = el('div', 'q-pbody'); panel.replaceChildren(panelHead('Configuración'), body);
      if (co.is_placeholder) body.appendChild(el('p', 'q-note warn', 'Datos de empresa de ejemplo: complétalos para que aparezcan en el PDF.'));
      const cs = section('Empresa emisora'); body.appendChild(cs);
      const g = el('div', 'q-grid'); const vals = {};
      [['legal_name', 'Razón social *', 'wide'], ['trade_name', 'Nombre comercial'], ['tax_id', 'RUC'], ['address', 'Dirección', 'wide'], ['email', 'Correo'], ['phone', 'Teléfono'], ['website', 'Web'], ['footer', 'Pie del documento', 'wide']].forEach(([k, l, w]) => {
        const i = inp(co[k], { maxlength: '300' }); i.addEventListener('input', () => (vals[k] = i.value)); g.appendChild(field(l, i, w)); });
      const te = el('textarea', 'q-in q-terms'); te.rows = 5; te.value = co.default_terms || ''; te.addEventListener('input', () => (vals.default_terms = te.value));
      g.appendChild(field('Términos por defecto (nuevas cotizaciones)', te, 'wide')); cs.appendChild(g);
      cs.appendChild(el('p', 'muted q-sm', 'Logo: pendiente (se añadirá a la plantilla cuando lo subas).'));
      const sv = btn('Guardar empresa', 'primary sm', 'check'); const e1 = el('p', 'q-err-inline'); e1.hidden = true;
      sv.addEventListener('click', async () => { e1.hidden = true; busyBtn(sv, true, 'Guardando…'); try { await api('company', 'PATCH', vals); busyBtn(sv, false); toast('Empresa guardada'); } catch (x) { busyBtn(sv, false); e1.textContent = x.message; e1.hidden = false; } });
      cs.append(e1, el('div', 'q-row-end')); cs.lastChild.appendChild(sv);
      const ss = section('Numeración'); body.appendChild(ss);
      const sg = el('div', 'q-grid'); const sc = inp(se.code), sp = inp(se.pattern), sn = inp(String(se.next_number), { inputmode: 'numeric' });
      const sr = sel([['true', 'Reinicia cada año'], ['false', 'No reinicia']], String(se.reset_yearly));
      sg.append(field('Código', sc), field('Formato', sp), field('Siguiente número', sn), field('Reinicio', sr)); ss.appendChild(sg);
      const prev = el('p', 'muted q-sm', 'Próximo: ' + se.preview); ss.appendChild(prev);
      const sb = btn('Guardar numeración', 'ghost sm', 'check'); const e2 = el('p', 'q-err-inline'); e2.hidden = true;
      sb.addEventListener('click', async () => { e2.hidden = true; try { const r = await api('series', 'PATCH', { code: sc.value.trim(), pattern: sp.value.trim(), next_number: parseInt(sn.value, 10), reset_yearly: sr.value === 'true' }); prev.textContent = 'Próximo: ' + r.preview; toast('Numeración guardada'); } catch (x) { e2.textContent = x.message; e2.hidden = false; } });
      ss.append(e2, el('div', 'q-row-end')); ss.lastChild.appendChild(sb);
      const ks = section('Impuestos y moneda'); body.appendChild(ks);
      ks.appendChild(el('p', 'muted q-sm', 'Moneda ' + st.default_currency + ' · IGV ' + st.igv_rate + ' · ' + (st.prices_include_igv ? 'precios incluyen IGV' : 'precios sin IGV') + ' · vigencia ' + st.default_validity_days + ' días.'));
      const cat0 = section('Catálogo de servicios', el('small', 'muted', 'Precios versionados; nunca inventados')); body.appendChild(cat0);
      const list = el('div', 'q-cat'); cat0.appendChild(list);
      const drawCat = (svcs) => { list.replaceChildren(); if (!svcs.length) list.appendChild(el('p', 'muted q-sm', 'Catálogo vacío.'));
        svcs.forEach((s) => { const r = el('div', 'q-cat-row'); const t = el('div'); t.appendChild(el('strong', null, s.name)); t.appendChild(el('small', 'muted', s.code + ' · ' + CYC[s.billing_mode] + ' · ' + s.unit));
          const p = s.current_price; const pr = el('div', 'q-cat-price');
          pr.appendChild(el('span', null, p ? money(p.currency, p.unit_price) + (p.price_includes_igv ? ' inc. IGV' : ' + IGV') : 'Sin precio vigente'));
          if (p) pr.appendChild(el('small', 'muted', 'desde ' + fdate(p.valid_from) + (p.verified ? '' : ' · por confirmar')));
          const np = inp('', { inputmode: 'decimal', placeholder: 'Nuevo precio', 'aria-label': 'Nuevo precio ' + s.name }); np.classList.add('q-num-in');
          const ok = btn('Fijar', 'ghost sm'); ok.addEventListener('click', async () => { try { await api('catalog/' + s.id + '/prices', 'POST', { currency: st.default_currency, unit_price: np.value.trim(), price_includes_igv: st.prices_include_igv }); toast('Precio actualizado'); drawCat((await api('catalog?all=1')).services); } catch (x) { toast(x.message); } });
          const pe = el('div', 'q-cat-set'); pe.append(np, ok);
          r.append(t, pr, pe); list.appendChild(r); }); };
      drawCat(cat.services || []);
      const add = el('div', 'q-grid q-cat-add'); const ac = inp('', { placeholder: 'CODIGO' }), an = inp('', { placeholder: 'Nombre del servicio' }), am = sel(CYCLES, 'monthly'), au = inp('und');
      add.append(field('Código', ac), field('Nombre', an), field('Ciclo', am), field('Unidad', au)); cat0.appendChild(add);
      const ab = btn('Agregar servicio', 'ghost sm', 'plus'); const e3 = el('p', 'q-err-inline'); e3.hidden = true;
      ab.addEventListener('click', async () => { e3.hidden = true; try { await api('catalog', 'POST', { code: ac.value.trim().toUpperCase(), name: an.value.trim(), billing_mode: am.value, unit: au.value.trim() || 'und' }); ac.value = an.value = ''; drawCat((await api('catalog?all=1')).services); toast('Servicio agregado'); } catch (x) { e3.textContent = x.message; e3.hidden = false; } });
      cat0.append(e3, el('div', 'q-row-end')); cat0.lastChild.appendChild(ab);
      const integ = await api('integrations').catch(() => null);
      if (integ) { const is = section('Integraciones'); body.appendChild(is);
        integ.adapters.forEach((x) => { const r = el('div', 'q-cust-card'); const t = el('div'); t.appendChild(el('strong', null, x.kind === 'mail' ? 'Correo (SMTP)' : 'Consulta RUC (SUNAT)')); t.appendChild(el('small', null, 'No configurado · necesita: ' + x.needs.join('; '))); r.appendChild(t); is.appendChild(r); });
        is.appendChild(el('p', 'muted q-sm', 'PDF: ' + (integ.pdf_available ? 'disponible' : 'no disponible en el servidor') + '.')); }
    } catch (e) { panel.replaceChildren(panelHead('Configuración'), stateBox('error', 'No se pudo cargar', e.message)); }
  }

  // ---------- routing / keys ----------
  document.addEventListener('keydown', (e) => { if (e.key === 'Escape' && !panel.hidden && !document.querySelector('.pop:not([hidden])')) closePanel(false); });
  window.addEventListener('beforeunload', (e) => { if (ed && ed.dirty) { e.preventDefault(); e.returnValue = ''; } });
  const m = /^#cotizacion(?:es|\/([0-9a-f-]{36}))$/.exec(location.hash);
  if (m) { enter(false); if (m[1]) openEditor(m[1]); }
})();
