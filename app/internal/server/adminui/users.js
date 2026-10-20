"use strict";
// Users & module entitlements. No innerHTML; writes send X-DevBrain-Admin: 1 + JSON (CSRF defence).
(function () {
  const $ = (id) => document.getElementById(id);
  function el(tag, attrs, ...kids) {
    const e = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs || {})) k === "class" ? (e.className = v) : e.setAttribute(k, v);
    for (const c of kids) if (c != null) e.append(c instanceof Node ? c : String(c));
    return e;
  }
  const when = (t) => (t ? new Date(t).toLocaleString("es-PE", { timeZone: "America/Lima", hour12: false, dateStyle: "short", timeStyle: "short" }) : "–");
  const msg = (t, bad) => { const m = $("msg"); m.textContent = t; m.className = bad ? "muted err" : "muted"; clearTimeout(msg.t); msg.t = setTimeout(() => (m.textContent = ""), 5000); };
  async function api(path, method, body) {
    const o = { method: method || "GET", headers: { "X-DevBrain-Admin": "1" }, credentials: "same-origin" };
    if (body) { o.headers["Content-Type"] = "application/json"; o.body = JSON.stringify(body); }
    const r = await fetch("/admin/api/" + path, o);
    const d = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(d.error || "Error " + r.status);
    return d;
  }
  let MODS = [], TENS = [];
  const tenantSel = (val, onChange) => {
    const s = el("select", { "aria-label": "Tenant" }, el("option", { value: "" }, "Sin tenant"));
    TENS.forEach((t) => s.append(el("option", { value: t.id }, t.name)));
    s.value = val || ""; if (onChange) s.addEventListener("change", () => onChange(s.value)); return s;
  };
  function drawUsers(users) {
    const head = el("tr", null, el("th", null, "Usuario"), el("th", null, "Tenant"), el("th", null, "Acceso"), ...MODS.map((m) => el("th", { title: m.description }, m.name)), el("th", null, "Actualizado"));
    const body = el("tbody");
    users.forEach((u) => {
      const sup = u.superadmin;
      const who = el("td", null, el("b", null, u.display_name || u.email), el("span", { class: "tech" }, u.email), sup ? el("span", { class: "pill ok" }, "superadmin") : null);
      const ten = el("td", null, sup ? (u.tenant || "–") : tenantSel(u.tenant_id, (v) => {
        if (v === "" || confirm("¿Asignar el tenant «" + (TENS.find((t) => t.id === v) || {}).name + "» a " + u.email + "?\nCon el módulo Notas verá esa base de conocimiento.")) save("users/" + encodeURIComponent(u.email), "PATCH", { tenant_id: v }); else load();
      }));
      const en = el("input", { type: "checkbox", "aria-label": "Habilitado " + u.email }); en.checked = u.enabled; en.disabled = sup;
      en.addEventListener("change", () => save("users/" + encodeURIComponent(u.email), "PATCH", { enabled: en.checked }));
      const acc = el("td", null, el("label", { class: "chk" }, en, u.enabled ? "habilitado" : "deshabilitado"));
      const cells = MODS.map((m) => {
        const cur = u.modules[m.key] || { enabled: false, role: "" };
        const td = el("td", { class: "mod" });
        if (sup) { td.append(el("span", { class: "pill ok" }, "todo" + (m.roles.length ? " · admin" : ""))); return td; }
        const cb = el("input", { type: "checkbox", "aria-label": m.name + " para " + u.email }); cb.checked = cur.enabled; cb.disabled = !u.enabled;
        td.append(el("label", { class: "chk" }, cb, cur.enabled ? "sí" : "no"));
        let rs = null;
        if (m.roles.length) {
          rs = el("select", { "aria-label": "Rol " + m.name }); m.roles.forEach((r) => rs.append(el("option", { value: r }, r)));
          rs.value = cur.role || m.default_role || m.roles[0]; rs.disabled = !cur.enabled || !u.enabled; td.append(rs);
          rs.addEventListener("change", () => save("users/" + encodeURIComponent(u.email) + "/modules/" + m.key, "PUT", { enabled: true, role: rs.value }));
        }
        cb.addEventListener("change", () => save("users/" + encodeURIComponent(u.email) + "/modules/" + m.key, "PUT", rs ? { enabled: cb.checked, role: rs.value } : { enabled: cb.checked }));
        return td;
      });
      body.append(el("tr", u.enabled ? null : { class: "off" }, who, ten, acc, ...cells, el("td", null, when(u.updated_at))));
    });
    $("users").replaceChildren(el("table", null, el("thead", null, head), body));
  }
  function drawMods() {
    $("mods").replaceChildren(el("dl", null, ...MODS.flatMap((m) => [el("dt", null, m.name + " (" + m.key + ")"), el("dd", null, m.description + (m.roles.length ? " · roles: " + m.roles.join(", ") : ""))])));
  }
  function drawAudit(rows) {
    const fmt = (x) => (x == null ? "–" : JSON.stringify(x).slice(0, 160));
    const t = el("table", null, el("thead", null, el("tr", null, ...["Fecha (Lima)", "Acción", "Usuario", "Módulo", "Antes", "Después", "IP"].map((h) => el("th", null, h)))),
      el("tbody", null, ...rows.map((a) => el("tr", null, el("td", null, when(a.at)), el("td", null, a.action), el("td", null, a.email), el("td", null, a.module || "–"),
        el("td", { class: "js" }, fmt(a.before)), el("td", { class: "js" }, fmt(a.after)), el("td", null, a.source_ip || "–")))));
    $("audit").replaceChildren(rows.length ? t : el("p", { class: "empty" }, "Sin cambios todavía."));
  }
  async function save(path, method, body) {
    try { await api(path, method, body); msg("Guardado."); } catch (e) { msg(e.message, true); }
    load();
  }
  async function load() {
    try {
      const [c, u, a] = await Promise.all([api("modules"), api("users"), api("audit")]);
      MODS = c.modules; TENS = c.tenants;
      const nt = $("n-tenant"); if (!nt.options.length) nt.replaceWith(Object.assign(tenantSel(""), { id: "n-tenant" }));
      drawUsers(u.users); drawMods(); drawAudit(a.audit);
    } catch (e) { msg(e.message, true); }
  }
  $("new").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const email = $("n-email").value.trim().toLowerCase(), tid = $("n-tenant").value;
    if (tid && !confirm("El tenant elegido comparte su base de conocimiento con este usuario si le das el módulo Notas. ¿Continuar?")) return;
    try { await api("users", "POST", { email, display_name: $("n-name").value.trim(), tenant_id: tid }); msg("Usuario agregado (solo Chat)."); $("new").reset(); }
    catch (e) { msg(e.message, true); }
    load();
  });
  load();
})();
