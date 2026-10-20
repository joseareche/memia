"use strict";
// DevBrain admin dashboard v2. No innerHTML, no external libs: DOM + inline SVG charts.
(function () {
  const $ = (id) => document.getElementById(id);
  const SVG = "http://www.w3.org/2000/svg";
  function el(tag, attrs, ...kids) {
    const e = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs || {})) k === "class" ? (e.className = v) : e.setAttribute(k, v);
    for (const c of kids) if (c != null) e.append(c instanceof Node ? c : String(c));
    return e;
  }
  function s(tag, attrs, text) {
    const e = document.createElementNS(SVG, tag);
    for (const [k, v] of Object.entries(attrs || {})) e.setAttribute(k, v);
    if (text != null) e.textContent = text;
    return e;
  }
  const num = (x) => Number(x || 0);
  // $: 2 decimals from $1, 3 from $0.01, else 4; tiny non-zero shown as <$0.0001
  const usd = (x) => { const v = num(x); if (v === 0) return "$0"; if (v < 0.0001) return "<$0.0001"; return "$" + v.toFixed(v >= 1 ? 2 : v >= 0.01 ? 3 : 4); };
  const compact = (x) => { const v = num(x), a = Math.abs(v);
    if (a >= 1e9) return (v / 1e9).toFixed(1).replace(/\.0$/, "") + "B";
    if (a >= 1e6) return (v / 1e6).toFixed(1).replace(/\.0$/, "") + "M";
    if (a >= 1e4) return (v / 1e3).toFixed(1).replace(/\.0$/, "") + "k";
    return Math.round(v).toLocaleString("es-PE"); };
  const int = (x) => Math.round(num(x)).toLocaleString("es-PE");
  const pct = (a, b) => (b > 0 ? Math.round((100 * a) / b) : 0) + "%";
  const when = (t) => (t ? new Date(t).toLocaleString("es-PE", { timeZone: "America/Lima", hour12: false, dateStyle: "short", timeStyle: "short" }) : "–");
  const ago = (t) => { if (!t) return "nunca"; const m = Math.round((Date.now() - new Date(t)) / 60000);
    return m < 1 ? "hace segundos" : m < 60 ? `hace ${m} min` : m < 1440 ? `hace ${Math.round(m / 60)} h` : `hace ${Math.round(m / 1440)} d`; };
  const OPS = { embed_ingest: "Importación / indexado", distill_map: "Resúmenes (fase 1)", distill_reduce: "Resúmenes (fase 2)",
    embed_search: "Búsqueda", answer: "Respuestas", pending_extract: "Pendientes", distill: "Resúmenes" };
  const IMPORT = new Set(["embed_ingest", "distill_map", "distill_reduce"]);
  const RED = { email: "Correos", phone: "Teléfonos", secret_assignment: "Secretos asignados", token: "Tokens", api_key: "Claves API", jwt: "JWT",
    private_key: "Claves privadas", url_credentials: "Credenciales en URL", sip_credentials: "Credenciales SIP" };
  const ERR = { provider_rate_limit: "límite del proveedor", provider_error: "error del proveedor", timeout: "tiempo agotado", policy_local_only: "política solo local", internal: "interno", error: "error", policy_refused: "rechazo por política" };
  const PER = { today: "hoy", "7d": "últimos 7 días", "30d": "últimos 30 días" };
  const opCell = (op) => el("span", { title: "operación técnica: " + op }, OPS[op] || op, el("span", { class: "tech" }, op));
  function fill(id, ...nodes) { $(id).replaceChildren(...nodes); }
  function table(cols, rows) {
    if (!rows.length) return el("p", { class: "empty" }, "Sin datos en este periodo.");
    const t = el("table", {}, el("thead", {}, el("tr", {}, ...cols.map((c) => el("th", { class: c.n ? "n" : "", title: c.t || "" }, c.h)))));
    const b = el("tbody");
    for (const r of rows) b.append(el("tr", {}, ...cols.map((c) => el("td", { class: c.n ? "n" : "" }, c.f(r)))));
    t.append(b);
    return t;
  }
  const pill = (st, txt) => el("span", { class: "pill " + st }, txt || st);
  const state = { period: "30d", tenant: "", project: "", data: null };

  function kpis(S) {
    const K = S.kpi || {}, C = S.content || {}, P = S.pending || {}, L = S.last_sync;
    const k = (l, v, sub, cls, x, tip) => el("div", { class: "kpi " + (cls || ""), title: tip || "" }, el("div", { class: "l" }, l), el("div", { class: "v" }, v),
      el("div", { class: "s" }, sub), x ? el("div", { class: "x" }, x) : null);
    const hits = num(K.cache_hits), miss = num(K.cache_misses);
    const lsum = (L && L.summary) || {};
    fill("kpis",
      k("Coste hoy", usd(K.cost_today), "uso diario, sin importaciones únicas", "blue"),
      k("Coste 30 días", usd(K.cost_30d), "uso " + usd(K.usage_30d) + " · importación única " + usd(K.import_30d), "", null,
        "Importación = indexado + resúmenes de la carga masiva (pago único). Uso = búsquedas, respuestas y pendientes."),
      k("Ahorro por caché", usd(K.saved_usd), "acierto " + pct(hits, hits + miss) + " · " + compact(hits) + " de " + compact(hits + miss) + " fragmentos", "good",
        "Cada fragmento ya embebido se reutiliza sin volver a pagar al proveedor.", "Ahorro estimado del periodo; tokens ≈ caracteres/4."),
      k("Contenido", compact(C.documents) + " docs", compact(C.chunks) + " fragmentos · " + int(C.projects) + " proyectos", "",
        int(C.knowledge_docs) + " resúmenes de conocimiento · " + int(C.recipes) + " recetas"),
      k("Pendientes abiertos", int(P.open), int(P.done) + " cerrados" + (P.latest ? " · último " + when(P.latest) : ""), num(P.open) > 0 ? "warn" : "good"),
      k("Última sincronización", L ? (L.status === "ok" ? "Correcta" : L.status === "skipped" ? "Omitida" : "Con fallos") : "Nunca",
        L ? ago(L.finished_at) + " · " + when(L.finished_at) : "aún no hay ejecuciones", L ? (L.status === "ok" ? "good" : "warn") : "",
        L ? (lsum.reason ? "motivo: " + lsum.reason : int(lsum.changed_convs) + " conversaciones · " + int(lsum.pending_new) + " pendientes nuevos · " + usd(lsum.cost_usd)) : null),
      k("Solicitudes (" + PER[state.period] + ")", compact(K.requests), compact(num(K.input_tokens) + num(K.output_tokens)) + " tokens (" + compact(K.input_tokens) + " in · " + compact(K.output_tokens) + " out)", ""),
      k("Errores / rechazos", int(K.errors) + " / " + int(K.policy_refused), int(K.redacted_items) + " datos sensibles redactados", num(K.errors) ? "warn" : ""));
  }

  function dayChart(days) {
    const withImp = $("t-import").checked, log = $("t-log").checked;
    const W = 960, H = 240, P = { l: 58, r: 10, t: 12, b: 26 };
    if (!days.length) { fill("chart-days", el("p", { class: "empty" }, "Sin datos todavía.")); $("chart-note").textContent = ""; return; }
    const val = (d) => num(d.usage_usd) + (withImp ? num(d.import_usd) : 0);
    const max = Math.max(1e-6, ...days.map(val));
    const FLOOR = 1e-5, lmin = Math.log10(FLOOR), lmax = Math.log10(Math.max(max, FLOOR * 10));
    const iw = W - P.l - P.r, ih = H - P.t - P.b;
    const y = (v) => (log ? (v <= FLOOR ? 0 : (ih * (Math.log10(v) - lmin)) / (lmax - lmin)) : (ih * v) / max);
    const svg = s("svg", { viewBox: `0 0 ${W} ${H}`, role: "img", "aria-label": "Coste por día" });
    const ticks = log ? Array.from({ length: Math.ceil(lmax - lmin) + 1 }, (_, i) => Math.pow(10, lmin + i)).filter((v) => v <= Math.pow(10, lmax) * 1.001) : [0, 1, 2, 3, 4].map((i) => (max * i) / 4);
    for (const tv of ticks) {
      const yy = P.t + ih - y(tv);
      svg.append(s("line", { x1: P.l, x2: W - P.r, y1: yy, y2: yy, stroke: "rgba(18,21,27,.08)" }));
      svg.append(s("text", { x: P.l - 6, y: yy + 4, "text-anchor": "end" }, usd(tv)));
    }
    const bw = Math.max(4, Math.min(30, (iw / days.length) * 0.6));
    days.forEach((d, i) => {
      const cx = P.l + (iw * (i + 0.5)) / days.length;
      const u = num(d.usage_usd), im = withImp ? num(d.import_usd) : 0, tot = u + im;
      const hT = y(tot), hU = tot > 0 ? (hT * u) / tot : 0;
      const tip = `${d.day}: uso ${usd(u)} · importación ${usd(d.import_usd)} · ahorro ${usd(d.saved_usd)} · ${int(d.requests)} solicitudes`;
      if (im > 0) { const r = s("rect", { x: cx - bw / 2, y: P.t + ih - hT, width: bw, height: Math.max(hT - hU, 0.5), rx: 3, fill: "#ff6b4a" }); r.append(s("title", {}, tip)); svg.append(r); }
      const r2 = s("rect", { x: cx - bw / 2, y: P.t + ih - hU, width: bw, height: Math.max(hU, u > 0 ? 1.5 : 0.5), rx: 3, fill: "#0078e6" }); r2.append(s("title", {}, tip)); svg.append(r2);
      if (days.length <= 16 || i % 2 === 0) svg.append(s("text", { x: cx, y: H - 8, "text-anchor": "middle" }, d.day.slice(5)));
    });
    const lg = el("div", { class: "legend" }, el("span", {}, el("i", { class: "c1" }), "Uso diario (búsqueda, respuestas, pendientes)"),
      withImp ? el("span", {}, el("i", { class: "c2" }), "Importación única (indexado + resúmenes)") : null);
    fill("chart-days", svg, lg);
    const impT = days.reduce((a, d) => a + num(d.import_usd), 0), useT = days.reduce((a, d) => a + num(d.usage_usd), 0);
    const peak = days.reduce((a, d) => (num(d.import_usd) > num(a.import_usd) ? d : a), days[0]);
    $("chart-note").textContent = `30 días: uso ${usd(useT)} · importación única ${usd(impT)}` + (impT > 0 ? ` (pico ${peak.day}: ${usd(peak.import_usd)})` : "") +
      (withImp && !log && impT > useT * 5 ? ". La importación domina la escala: desmarca «Incluir importación» o usa escala logarítmica." : !withImp && impT > 0 ? ". La importación única está oculta; márcala para verla." : ".");
  }

  function donut(hits, miss) {
    const tot = hits + miss, r = 58, c = 2 * Math.PI * r, f = tot ? hits / tot : 0;
    const svg = s("svg", { viewBox: "0 0 160 160", role: "img", "aria-label": "Tasa de acierto de caché" });
    svg.append(s("circle", { cx: 80, cy: 80, r, fill: "none", stroke: "#e6e9e5", "stroke-width": 18 }));
    svg.append(s("circle", { cx: 80, cy: 80, r, fill: "none", stroke: "#00a49b", "stroke-width": 18, "stroke-dasharray": `${c * f} ${c}`, transform: "rotate(-90 80 80)", "stroke-linecap": "round" }));
    svg.append(s("text", { x: 80, y: 88, "text-anchor": "middle", style: "font:700 24px Bricolage Grotesque,sans-serif;fill:#12151b" }, Math.round(f * 100) + "%"));
    return svg;
  }
  function bars(entries) {
    if (!entries.length) return el("p", { class: "empty" }, "Nada que reportar en este periodo.");
    const max = Math.max(...entries.map((x) => x[2]));
    return el("div", {}, ...entries.sort((a, b) => b[2] - a[2]).map(([label, tech, v]) => { const sp = el("span"); sp.style.width = (100 * v) / max + "%";
      return el("div", { class: "bar" }, el("span", { class: "t", title: tech }, label), el("span", { class: "b" }, sp), el("b", {}, int(v))); }));
  }

  function filters(S) {
    const F = S.filters || {};
    const t = $("f-tenant"), p = $("f-project");
    if (t.options.length <= 1) for (const n of F.tenants || []) t.append(el("option", { value: n }, n));
    const cur = state.project;
    p.replaceChildren(el("option", { value: "" }, "Todos"), ...(F.projects || []).map((x) => el("option", { value: x.name }, (state.tenant ? "" : x.tenant + " · ") + x.name)));
    p.value = cur;
    t.value = state.tenant;
  }

  function render(j) {
    const S = j.stats || {}, K = S.kpi || {};
    state.data = S;
    $("updated").textContent = "Actualizado " + when(S.generated_at);
    filters(S);
    kpis(S);
    dayChart(S.per_day || []);
    $("ops-p").textContent = "(" + PER[state.period] + ")"; $("ten-p").textContent = "(" + PER[state.period] + ")";
    const hits = num(K.cache_hits), miss = num(K.cache_misses);
    fill("cache", donut(hits, miss), el("div", { class: "big" }, usd(K.saved_usd) + " ahorrados"),
      el("div", { class: "muted" }, compact(hits) + " fragmentos reutilizados de " + compact(hits + miss) + " (" + PER[state.period] + ")"),
      el("p", { class: "muted note" }, "Tasa de acierto = fragmentos cuyo embedding ya estaba en caché ÷ total procesados. Un acierto no se paga al proveedor."));
    fill("ops", table([{ h: "Operación", f: (r) => el("span", {}, opCell(r.operation), IMPORT.has(r.operation) ? el("span", {}, " ", pill("imp", "única")) : null) },
      { h: "Modelo", f: (r) => r.model }, { h: "Solicitudes", n: 1, f: (r) => int(r.requests) },
      { h: "Tokens in", n: 1, f: (r) => compact(r.input_tokens), t: "tokens de entrada" }, { h: "Tokens out", n: 1, f: (r) => compact(r.output_tokens) },
      { h: "Coste", n: 1, f: (r) => usd(r.cost_usd) }, { h: "Ahorro", n: 1, f: (r) => usd(r.saved_usd) },
      { h: "Caché", n: 1, f: (r) => (num(r.cache_hits) + num(r.cache_misses) ? pct(num(r.cache_hits), num(r.cache_hits) + num(r.cache_misses)) : "–"), t: "tasa de acierto de caché" },
      { h: "Errores", n: 1, f: (r) => int(r.errors) },
      { h: "p50", n: 1, f: (r) => (r.p50_ms == null ? "–" : int(r.p50_ms) + " ms"), t: "latencia mediana" }, { h: "p95", n: 1, f: (r) => (r.p95_ms == null ? "–" : int(r.p95_ms) + " ms") }], S.per_operation || []));
    fill("tenants", table([{ h: "Tenant", f: (r) => r.tenant }, { h: "Solicitudes", n: 1, f: (r) => int(r.requests) }, { h: "Tokens", n: 1, f: (r) => compact(r.tokens) },
      { h: "Coste", n: 1, f: (r) => usd(r.cost_usd) }, { h: "Ahorro", n: 1, f: (r) => usd(r.saved_usd) }], S.per_tenant || []));
    const ix = S.active_index;
    fill("index", ix ? el("dl", {}, el("dt", {}, "Nombre"), el("dd", {}, ix.name), el("dt", {}, "Modelo"), el("dd", {}, ix.provider + " / " + ix.model),
      el("dt", {}, "Dimensiones"), el("dd", {}, int(ix.dims)), el("dt", {}, "Embeddings"), el("dd", {}, int(ix.embeddings)),
      el("dt", {}, "Sin embedding"), el("dd", {}, num(ix.chunks_missing) ? pill("partial", int(ix.chunks_missing) + " fragmentos") : pill("ok", "0 · completo")),
      el("dt", {}, "Activo desde"), el("dd", {}, when(ix.activated_at))) : el("p", { class: "empty" }, "Sin índice activo."));
    fill("redactions", bars(Object.entries(S.redactions_by_type || {}).map(([k, v]) => [RED[k] || k, k, num(v)])),
      el("p", { class: "muted note" }, "Solo conteos: los valores nunca se guardan ni se envían."));
    const errs = Object.entries(S.errors_by_kind || {}).map(([k, v]) => { const [op, kind] = k.split(":"); return [(OPS[op] || op) + " · " + (ERR[kind] || kind), k, num(v)]; });
    const recentErr = (S.recent_errors || []).slice(0, 5);
    fill("errors", bars(errs), recentErr.length ? el("ul", { class: "errs" }, ...recentErr.map((e) => el("li", {}, when(e.at) + " · " + (OPS[e.operation] || e.operation) + " · " + (ERR[e.error_kind] || e.error_kind || e.status)))) : null);
    fill("recent", table([{ h: "Hora (Lima)", f: (r) => when(r.at) }, { h: "Tenant", f: (r) => r.tenant }, { h: "Operación", f: (r) => opCell(r.operation) },
      { h: "Modelo", f: (r) => r.model }, { h: "Estado", f: (r) => pill(r.status, r.status === "ok" ? "ok" : (ERR[r.error_kind] || r.status)) },
      { h: "Items", n: 1, f: (r) => int(r.items) }, { h: "Caché", n: 1, f: (r) => (r.cache_hits + r.cache_misses ? r.cache_hits + "/" + (r.cache_hits + r.cache_misses) : "–") },
      { h: "Tokens", n: 1, f: (r) => compact(r.tokens) }, { h: "Latencia", n: 1, f: (r) => int(r.latency_ms) + " ms" }, { h: "Coste", n: 1, f: (r) => usd(r.cost_usd) }], S.recent || []));
    const pr = Object.entries(j.prices_per_1m_usd || {}).map(([k, v]) => k + " in $" + v.In + (v.Out ? " / out $" + v.Out : "") + " por 1M").join(" · ");
    $("prices").textContent = "Precios estimados: " + pr + ". " + (j.prices_note || "");
  }

  async function load() {
    const b = $("refresh"); b.disabled = true;
    try {
      const q = new URLSearchParams({ period: state.period, tenant: state.tenant, project: state.project });
      const r = await fetch((window.ADMIN_STATS_URL || "/admin/api/stats2") + "?" + q, { credentials: "same-origin", cache: "no-store" });
      if (!r.ok) throw new Error("HTTP " + r.status);
      render(await r.json());
    } catch (e) { $("updated").textContent = "No se pudo cargar: " + e.message; }
    finally { b.disabled = false; }
  }
  document.addEventListener("DOMContentLoaded", () => {
    $("refresh").addEventListener("click", load);
    for (const btn of $("period").querySelectorAll("button")) btn.addEventListener("click", () => {
      state.period = btn.dataset.p; for (const x of $("period").querySelectorAll("button")) x.classList.toggle("on", x === btn); load(); });
    $("f-tenant").addEventListener("change", (e) => { state.tenant = e.target.value; state.project = ""; load(); });
    $("f-project").addEventListener("change", (e) => { state.project = e.target.value; load(); });
    for (const id of ["t-import", "t-log"]) $(id).addEventListener("change", () => state.data && dayChart(state.data.per_day || []));
    load(); setInterval(load, 60000);
  });
})();
