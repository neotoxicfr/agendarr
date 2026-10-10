// agendarr front-end entry point.
"use strict";

const I18N = (() => {
  try { return JSON.parse(document.getElementById("i18n").textContent); } catch { return {}; }
})();
const t = (key) => I18N[key] || key;

if ("serviceWorker" in navigator) {
  window.addEventListener("load", () => {
    navigator.serviceWorker.register("/sw.js").catch(() => {});
  });
}

// Forms that need a confirmation (destructive admin actions).
document.addEventListener("submit", (e) => {
  if (e.defaultPrevented) return; // handled already (sign-out below)
  const msg = e.target.dataset && e.target.dataset.confirm;
  if (msg && !window.confirm(msg)) e.preventDefault();
});

// The Plex popup lands on /auth/plex/done, which closes itself.
if (document.querySelector("[data-close-window]") && window.opener) {
  setTimeout(() => window.close(), 600);
}

// Plex sign-in. A popup keeps agendarr open behind it (desktop, and a new tab
// on phones); the page polls until Plex confirms the PIN. If the popup is
// blocked, fall back to a full redirect that comes back to /auth/plex/return.
// The button stays usable: clicking again (popup closed by mistake) starts a
// fresh PIN and drops the previous poll.
const loginBtn = document.querySelector("[data-plex-login]");
if (loginBtn) {
  const status = document.querySelector("[data-plex-status]");
  const say = (msg) => { status.textContent = msg; };
  let timer = null;
  let deadline = 0;
  let busy = false;

  const stop = () => {
    clearInterval(timer);
    timer = null;
    document.removeEventListener("visibilitychange", onVisible);
  };
  const check = async () => {
    if (Date.now() > deadline) { stop(); say(t("login.timeout")); return; }
    try {
      const res = await fetch("/auth/plex/check", { method: "POST" });
      const body = await res.json();
      if (body.redirect) { stop(); window.location.assign(body.redirect); }
    } catch { /* network blip: keep polling */ }
  };
  // Mobile browsers freeze background tabs: check at once when we come back.
  const onVisible = () => { if (document.visibilityState === "visible" && timer) check(); };

  loginBtn.addEventListener("click", async () => {
    if (busy) return;
    busy = true;
    loginBtn.setAttribute("aria-disabled", "true");
    if (timer) stop();
    // Open the window synchronously, inside the click, or it gets blocked.
    const popup = window.open("", "plex-auth", "width=600,height=760");
    try {
      const res = await fetch("/auth/plex/start", { method: "POST" });
      const body = await res.json();
      if (!res.ok || !body.popupUrl) {
        if (popup) popup.close();
        window.location.assign(body.redirect || "/login?e=plex");
        return;
      }
      if (!popup) { window.location.assign(body.redirectUrl); return; }
      popup.location = body.popupUrl;
      say(t("login.waiting"));
      deadline = Date.now() + 10 * 60 * 1000;
      timer = setInterval(check, 2000);
      document.addEventListener("visibilitychange", onVisible);
    } catch {
      if (popup) popup.close();
      say(t("login.failed"));
    } finally {
      busy = false;
      loginBtn.removeAttribute("aria-disabled");
    }
  });
}

// Instance form: the API key hint follows the kind, and "Test" checks the
// values as typed without saving them.
const instForm = document.querySelector("[data-instance-form]");
if (instForm) {
  const syncKind = () => {
    const kind = (instForm.querySelector("[name=kind]:checked") || instForm.querySelector("[name=kind]")).value;
    instForm.querySelectorAll("[data-kind-help]").forEach((el) => {
      el.hidden = el.dataset.kindHelp !== (kind === "seerr" ? "seerr" : "arr");
    });
  };
  instForm.addEventListener("change", syncKind);
  syncKind();

  const out = instForm.querySelector("[data-test-result]");
  const btn = instForm.querySelector("[data-test-instance]");
  // A form sent back with errors: put the focus on the first bad field.
  if (instForm.hasAttribute("data-has-errors")) instForm.querySelector("[aria-invalid=true]")?.focus();

  btn.addEventListener("click", async () => {
    if (btn.getAttribute("aria-disabled") === "true") return;
    // aria-disabled rather than disabled: a disabled button loses the focus.
    btn.setAttribute("aria-disabled", "true");
    out.className = "test-result";
    out.textContent = t("js.testing");
    try {
      const res = await fetch("/admin/instances/test", { method: "POST", body: new FormData(instForm) });
      const body = await res.json();
      out.textContent = body.message;
      out.classList.add(body.ok ? "is-ok" : "is-error");
    } catch {
      out.textContent = t("js.test_failed");
      out.classList.add("is-error");
    } finally {
      btn.removeAttribute("aria-disabled");
    }
  });
}

// Copy buttons (sync page).
document.querySelectorAll("[data-copy]").forEach((btn) => {
  btn.addEventListener("click", async () => {
    const input = document.querySelector(btn.dataset.copy);
    const status = document.querySelector("[data-copy-status]");
    try {
      await navigator.clipboard.writeText(input.value);
      if (status) status.textContent = t("js.copied");
    } catch {
      input.select();
      if (status) status.textContent = t("js.copy_failed");
    }
  });
});

// Shared helpers for the bell and the settings page.
const tf = (key, vars = {}) => t(key).replace(/\{(\w+)\}/g, (_, k) => vars[k] ?? "");
const h = (tag, attrs = {}, ...kids) => {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v == null || v === false) continue;
    if (k === "text") n.textContent = v;
    else if (k === "class") n.className = v;
    else if (k.startsWith("on")) n.addEventListener(k.slice(2), v);
    else n.setAttribute(k, v === true ? "" : v);
  }
  for (const c of kids.flat()) if (c != null && c !== false) n.append(c);
  return n;
};
const sendJSON = (method, url, body) => fetch(url, { method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
const pageLang = document.documentElement.lang || "fr";

// Web Push on this device: subscribe, unsubscribe, and drop the subscription
// on sign-out so the next person on a shared device does not get it (SEC-02).
const push = (() => {
  const supported = "serviceWorker" in navigator && "PushManager" in window && "Notification" in window;
  const b64 = (s) => Uint8Array.from(atob(s.replace(/-/g, "+").replace(/_/g, "/").padEnd(Math.ceil(s.length / 4) * 4, "=")), (c) => c.charCodeAt(0));
  const current = async () => (supported ? (await navigator.serviceWorker.ready).pushManager.getSubscription() : null);
  return {
    supported,
    current,
    async on(vapidKey) {
      const reg = await navigator.serviceWorker.ready;
      const sub = (await reg.pushManager.getSubscription()) || await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: b64(vapidKey) });
      const res = await sendJSON("POST", "/api/push", { subscription: sub.toJSON(), lang: pageLang });
      if (!res.ok) { await sub.unsubscribe(); throw new Error("refused"); }
    },
    async off() {
      const sub = await current();
      if (!sub) return;
      await sendJSON("POST", "/api/push", { endpoint: sub.endpoint, off: true }).catch(() => {});
      await sub.unsubscribe();
    },
  };
})();

// Sign-out: forget this device's push subscription first (SEC-02).
document.querySelectorAll('form[action="/logout"]').forEach((form) => {
  form.addEventListener("submit", async (e) => {
    if (!push.supported) return; // the confirmation below the document handles it
    e.preventDefault();
    if (form.dataset.confirm && !window.confirm(form.dataset.confirm)) return;
    try { await Promise.race([push.off(), new Promise((r) => setTimeout(r, 3000))]); } catch { /* sign out anyway */ }
    HTMLFormElement.prototype.submit.call(form);
  });
});

// Notifications: the bell in the top bar and its panel (history, mark read,
// unfollow; the settings live on /settings).
const bell = document.querySelector("[data-bell]");
if (bell) {
  const wrap = bell.closest(".bell-wrap");
  const panel = document.querySelector("[data-notif-panel]");
  const list = panel.querySelector("[data-notif-list]");
  const empty = panel.querySelector("[data-notif-empty]");
  const count = bell.querySelector("[data-bell-count]");
  let names = null;
  try { names = new Intl.DisplayNames([pageLang], { type: "language" }); } catch { /* old browser */ }
  const langName = (c) => { try { return (names && names.of(c)) || c; } catch { return c; } };
  const fWhen = new Intl.DateTimeFormat(pageLang, { dateStyle: "medium", timeStyle: "short" });
  const pad = (n) => String(n).padStart(2, "0");
  const code = (e) => `S${pad(e.s)}E${pad(e.e)}`;
  const colon = pageLang.startsWith("fr") ? " : " : ": ";

  let state = { items: [], unread: 0 };
  let renderListSoon = false; // a refresh came while the list had the focus

  function summary(d) {
    const parts = [];
    const eps = d.episodes || [];
    if (eps.length === 1) parts.push(code(eps[0]) + (eps[0].t ? ` · ${eps[0].t}` : ""));
    else if (eps.length > 1) parts.push(`${code(eps[0])}–${code(eps[eps.length - 1])} (${tf("notif.n_episodes", { n: eps.length })})`);
    for (const x of d.diffs || []) {
      const label = t("notif.d_" + x.what);
      if (x.add) parts.push(`${label}${colon}+ ${x.add.map(langName).join(", ")}`);
      else parts.push(`${label}${colon}${x.from ? x.from + " → " : ""}${x.to}`);
    }
    return parts;
  }

  function renderCount() {
    count.hidden = state.unread === 0;
    count.textContent = state.unread > 99 ? "99+" : String(state.unread);
    bell.setAttribute("aria-label", state.unread ? `${t("notif.title")} — ${tf("notif.unread", { n: state.unread })}` : t("notif.title"));
  }

  function renderList() {
    // Keep the keyboard where it was across a re-render (UX-01).
    const focused = document.activeElement;
    const at = panel.contains(focused) && focused.closest("li[data-id]");
    const focusID = at && at.dataset.id;
    const focusIndex = at ? [...at.querySelectorAll("button")].indexOf(focused) : -1;
    empty.hidden = state.items.length > 0;
    list.replaceChildren(...state.items.map((n) => {
      let d = {};
      try { d = JSON.parse(n.detail); } catch { /* keep empty */ }
      return h("li", { class: `notif${n.read ? "" : " is-unread"}`, "data-id": n.id },
        d.poster ? h("img", { class: "notif-poster", src: d.poster + "?w=250", alt: "", loading: "lazy", width: 40, height: 60 }) : h("span", { class: "notif-poster" }),
        h("div", { class: "notif-body" },
          h("p", { class: "notif-title" },
            n.read ? null : h("span", { class: "sr-only", text: t("notif.unread_one") }), // UX-05
            h("strong", { text: n.title }),
            h("span", { class: `notif-kind kind-${n.kind}`, text: t("notif." + n.kind) })),
          summary(d).map((l) => h("p", { class: "notif-line", text: l })),
          h("p", { class: "notif-when" }, h("time", { datetime: n.at, text: fWhen.format(new Date(n.at)) })),
          h("p", { class: "notif-actions" },
            n.read ? null : h("button", { class: "btn btn-ghost", type: "button", text: t("notif.ack"), onclick: () => ack([n.id]) }),
            n.follows ? h("button", { class: "btn btn-ghost", type: "button", text: t("notif.unfollow"), onclick: () => unfollow(n) }) : null)));
    }));
    if (focusID != null) {
      const li = list.querySelector(`li[data-id="${focusID}"]`);
      const btns = li ? [...li.querySelectorAll("button")] : [];
      (btns[Math.min(Math.max(focusIndex, 0), btns.length - 1)] || li?.nextElementSibling?.querySelector("button") || panel.querySelector("#notif-title")).focus();
    }
  }

  async function load({ list: withList = !panel.hidden } = {}) {
    try {
      const res = await fetch("/api/notifications");
      if (!res.ok) return;
      state = await res.json();
      renderCount();
      // Do not rebuild the list under someone reading it (UX-01).
      if (withList && !(panel.contains(document.activeElement) && document.activeElement !== bell)) renderList();
      else if (withList) renderListSoon = true;
    } catch { /* offline */ }
  }
  async function ack(ids) {
    await sendJSON("POST", "/api/notifications/read", { ids }).catch(() => {});
    const res = await fetch("/api/notifications").catch(() => null);
    if (res && res.ok) { state = await res.json(); renderCount(); renderList(); }
  }
  async function unfollow(n) {
    const res = await sendJSON("PUT", "/api/follows", { group: n.group, title: n.title, on: false }).catch(() => null);
    if (!res || !res.ok) return;
    state.items.forEach((x) => { if (x.group === n.group) x.follows = false; });
    renderList();
    document.dispatchEvent(new CustomEvent("agendarr:follow", { detail: { group: n.group, on: false } }));
  }

  // Panel: a popover under the bell, closed by Escape, a click outside, or
  // the keyboard leaving it (UX-02).
  const setOpen = (open) => {
    panel.hidden = !open;
    bell.setAttribute("aria-expanded", String(open));
    if (open) { renderList(); load({ list: true }); panel.querySelector("button, a")?.focus(); }
  };
  bell.addEventListener("click", () => setOpen(panel.hidden));
  document.addEventListener("keydown", (ev) => { if (ev.key === "Escape" && !panel.hidden) { setOpen(false); bell.focus(); } });
  document.addEventListener("click", (ev) => { if (!panel.hidden && !ev.target.closest(".bell-wrap")) setOpen(false); });
  wrap.addEventListener("focusout", (ev) => {
    if (!panel.hidden && ev.relatedTarget && !wrap.contains(ev.relatedTarget)) setOpen(false);
    else if (renderListSoon && !panel.contains(ev.relatedTarget)) { renderListSoon = false; renderList(); }
  });
  panel.querySelector("[data-notif-all]").addEventListener("click", () => ack([]));

  load({ list: false });
  // Refresh the count when coming back to the tab, and every two minutes.
  document.addEventListener("visibilitychange", () => { if (document.visibilityState === "visible") load(); });
  setInterval(() => { if (document.visibilityState === "visible") load(); }, 120000);
  // A push message received while the page is open refreshes the bell.
  navigator.serviceWorker?.addEventListener("message", (ev) => { if (ev.data === "agendarr:notification") load(); });
  // A subscription left by someone else on this browser now belongs to
  // whoever is signed in (SEC-02).
  push.current().then((sub) => { if (sub) sendJSON("POST", "/api/push", { subscription: sub.toJSON(), lang: pageLang }).catch(() => {}); }).catch(() => {});
}

// Settings page: each control saves at once; a failure says so (UX-04).
const settings = document.querySelector(".settings");
if (settings) {
  // The answer shows next to the setting that changed, and is re-announced
  // even when it says the same thing twice (PAGE-05).
  let status = null;
  const say = (key) => {
    if (!status) return;
    status.textContent = "";
    requestAnimationFrame(() => { status.textContent = t(key); });
  };
  const save = async (url, method, body, revert) => {
    const res = await sendJSON(method, url, body).catch(() => null);
    if (res && res.ok) { say("settings.saved"); return true; }
    say("settings.failed");
    revert();
    return false;
  };
  settings.addEventListener("change", async (ev) => {
    const x = ev.target;
    const group = x.closest(".settings-group");
    status = group && (group.querySelector("[data-group-status]") ||
      group.appendChild(h("p", { class: "settings-status", role: "status", "data-group-status": "" })));
    if (x.matches("[data-pref-formats]")) {
      await save("/api/prefs", "PUT", { formats: x.value }, () => { settings.querySelector(`[data-pref-formats]:not([value="${x.value}"])`).checked = true; });
      try { localStorage.setItem("agendarr.formats", JSON.stringify(x.value)); } catch { /* private mode */ }
    } else if (x.matches("[data-pref-site]")) {
      const sites = [...settings.querySelectorAll("[data-pref-site]:checked")].map((c) => c.dataset.prefSite);
      if (await save("/api/prefs", "PUT", { sites }, () => { x.checked = !x.checked; })) {
        try { localStorage.setItem("agendarr.sites", JSON.stringify(sites)); } catch { /* private mode */ }
      }
    } else if (x.matches("[data-pref-shortcuts]")) {
      if (await save("/api/prefs", "PUT", { shortcuts: x.checked }, () => { x.checked = !x.checked; })) {
        try { localStorage.setItem("agendarr.shortcuts", JSON.stringify(x.checked)); } catch { /* private mode */ }
      }
    } else if (x.matches("[data-notify]")) {
      const notify = [...settings.querySelectorAll("[data-notify]:checked")].map((c) => c.dataset.notify);
      await save("/api/notifications/sources", "PUT", { notify }, () => { x.checked = !x.checked; });
    }
  });

  // This device's notifications.
  const box = settings.querySelector("[data-push]");
  const help = settings.querySelector("[data-push-help]");
  const refresh = async () => {
    if (!window.isSecureContext) { box.disabled = true; help.textContent = t("notif.push_https"); return; }
    if (!push.supported) { box.disabled = true; help.textContent = t("notif.push_unsupported"); return; }
    if (Notification.permission === "denied") { box.disabled = true; box.checked = false; help.textContent = t("notif.push_denied"); return; }
    box.checked = !!(await push.current());
    help.textContent = "";
  };
  box.addEventListener("change", async () => {
    try {
      if (!box.checked) { await push.off(); return; }
      if ((await Notification.requestPermission()) !== "granted") { await refresh(); return; }
      const res = await fetch("/api/notifications");
      const { vapidKey } = res.ok ? await res.json() : {};
      if (!vapidKey) throw new Error("no key");
      await push.on(vapidKey);
      help.textContent = "";
    } catch {
      box.checked = false;
      help.textContent = t("notif.push_unsupported");
    }
  });
  refresh().catch(() => {});
}

// "My list": what the person is notified about, with where it comes from;
// alerts added by hand can be removed. Filters and sort stay on the device.
const mylist = document.querySelector("[data-mylist]");
if (mylist) {
  const $ = (s) => mylist.querySelector(s);
  const ul = $("[data-list-items]"), empty = $("[data-list-empty]"), countEl = $("[data-list-count]"), warning = $("[data-list-warning]");
  const SRC = ["seerr", "watching", "watchlist", "manual"];
  const fDate = new Intl.DateTimeFormat(pageLang, { dateStyle: "medium" });
  const saved = (() => { try { return JSON.parse(localStorage.getItem("agendarr.list")) || {}; } catch { return {}; } })();
  const st = Object.assign({ q: "", kind: "all", state: "all", sort: "title", src: SRC.slice() }, saved, { q: "" });
  let items = [];

  const persist = () => { try { localStorage.setItem("agendarr.list", JSON.stringify({ kind: st.kind, state: st.state, sort: st.sort, src: st.src })); } catch { /* private mode */ } };
  const stateOf = (it) => (it.hasFile ? "available" : "upcoming"); // same words as the calendar (COH-02)
  const fold = (s) => s.normalize("NFD").replace(/\p{Diacritic}/gu, "").toLowerCase();

  function sync() {
    $("[data-list-q]").value = st.q;
    mylist.querySelectorAll("input[name=list-kind]").forEach((x) => { x.checked = x.value === st.kind; });
    $("[data-list-state]").value = st.state;
    $("[data-list-sort]").value = st.sort;
    mylist.querySelectorAll("[data-list-src]").forEach((x) => { x.checked = st.src.includes(x.dataset.listSrc); });
  }

  function row(it) {
    const info = [];
    if (!it.inLibrary) info.push(t("list.not_in_library"));
    else if (it.hasFile) info.push(t("cal.status_downloaded"));
    if (it.next) info.push(tf("list.next", { date: fDate.format(new Date(it.next)) }));
    else if (it.status === "ended") info.push(t("list.ended"));
    if (it.inLibrary && !it.monitored) info.push(t("list.unmonitored"));
    const manual = it.sources.includes("manual");
    return h("li", { class: `list-item is-${stateOf(it)}`, "data-group": it.group },
      it.poster ? h("img", { class: "list-poster", src: it.poster + "?w=250", alt: "", loading: "lazy", width: 48, height: 72 }) : h("span", { class: "list-poster" }),
      h("div", { class: "list-body" },
        h("p", { class: "list-title" }, h("strong", { text: it.title }), it.year ? h("span", { class: "list-year", text: String(it.year) }) : null,
          h("span", { class: "list-kind", text: t(it.kind === "movie" ? "cal.movies" : "list.series") })),
        h("p", { class: "list-srcs" }, it.sources.map((s) => h("span", { class: `src-badge src-${s}`, text: t("list.src_" + s) }))),
        info.length ? h("p", { class: "list-info", text: info.join(" · ") }) : null),
      manual ? h("button", { class: "btn btn-ghost list-remove", type: "button", text: t("list.remove"),
        "aria-label": `${t("list.remove")} : ${it.title}`, onclick: (ev) => remove(it, ev.currentTarget) }) : null);
  }

  function render() {
    const q = fold(st.q.trim());
    const shown = items.filter((it) => (st.kind === "all" || it.kind === st.kind)
      && (st.state === "all" || stateOf(it) === st.state)
      && it.sources.some((s) => st.src.includes(s))
      && (!q || fold(it.title).includes(q)));
    const by = {
      title: (a, b) => a.title.localeCompare(b.title, pageLang, { sensitivity: "base" }),
      next: (a, b) => (a.next ? Date.parse(a.next) : Infinity) - (b.next ? Date.parse(b.next) : Infinity) || by.title(a, b),
      added: (a, b) => (b.added ? Date.parse(b.added) : 0) - (a.added ? Date.parse(a.added) : 0) || by.title(a, b),
    };
    shown.sort(by[st.sort] || by.title);
    ul.replaceChildren(...shown.map(row));
    ul.removeAttribute("aria-busy");
    // Nothing at all is not "nothing matches": say how to fill the list (PAGE-04).
    $("[data-list-none]").hidden = items.length > 0;
    empty.hidden = shown.length > 0 || items.length === 0;
    countEl.textContent = tf("list.count", { n: shown.length, total: items.length });
  }

  async function remove(it, btn) {
    const res = await sendJSON("PUT", "/api/follows", { group: it.group, title: it.title, on: false }).catch(() => null);
    if (!res || !res.ok) { countEl.textContent = `${t("settings.failed")} — ${it.title}`; return; } // announced (PAGE-06)
    it.sources = it.sources.filter((s) => s !== "manual");
    if (!it.sources.length) items = items.filter((x) => x !== it);
    const next = btn.closest("li").nextElementSibling;
    render();
    // Keep the keyboard in the list after the row changed or left.
    (ul.querySelector(`li[data-group="${CSS.escape(it.group)}"] button`) || (next && ul.querySelector(`li[data-group="${CSS.escape(next.dataset.group)}"] button`)) || $("[data-list-q]")).focus();
    countEl.textContent = t("list.removed") + " — " + countEl.textContent;
  }

  mylist.addEventListener("input", (ev) => { if (ev.target.matches("[data-list-q]")) { st.q = ev.target.value; render(); } });
  mylist.addEventListener("change", (ev) => {
    const x = ev.target;
    if (x.name === "list-kind") st.kind = x.value;
    else if (x.matches("[data-list-state]")) st.state = x.value;
    else if (x.matches("[data-list-sort]")) st.sort = x.value;
    else if (x.matches("[data-list-src]")) st.src = [...mylist.querySelectorAll("[data-list-src]:checked")].map((c) => c.dataset.listSrc);
    else return;
    persist();
    render();
  });

  sync();
  fetch("/api/mylist").then((r) => (r.ok ? r.json() : Promise.reject())).then((data) => {
    items = data.items || [];
    // Sources not used for notifications are not offered as filters.
    mylist.querySelectorAll("[data-list-src]").forEach((x) => {
      x.closest("label").hidden = x.dataset.listSrc !== "manual" && !(data.sources || []).includes(x.dataset.listSrc);
    });
    // Each source that could not be read says why, as on the calendar (PAGE-03).
    const notes = Object.entries(data.errors || {}).map(([src, code]) => t("cal.mine_error_" + src).replace("{why}", t("cal.why_" + code)));
    warning.hidden = notes.length === 0;
    warning.textContent = notes.join(" ");
    render();
  }).catch(() => { ul.removeAttribute("aria-busy"); countEl.textContent = t("list.load_failed"); }); // PAGE-07
}

// A confirmation shown after a redirect (saved, deleted…) is read out: the
// focus moves to it (PAGE-09).
const flash = document.querySelector(".flash");
if (flash) { flash.tabIndex = -1; flash.focus({ preventScroll: false }); }

// A Plex avatar that does not load shows the person's initial, not a broken
// image.
document.querySelectorAll("img.avatar").forEach((img) => {
  const swap = () => {
    const name = img.closest("li, .me")?.querySelector(".row-title, .me-name")?.firstChild?.textContent || "?";
    img.replaceWith(h("span", { class: "avatar avatar-initial", "aria-hidden": "true", text: name.trim().charAt(0).toUpperCase() }));
  };
  if (img.complete && img.naturalWidth === 0) swap();
  else img.addEventListener("error", swap, { once: true });
});
