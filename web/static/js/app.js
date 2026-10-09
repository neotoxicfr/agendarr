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

// Notifications: the bell in the top bar, its panel (history, acknowledge,
// unfollow, sources, this device's Web Push subscription).
const bell = document.querySelector("[data-bell]");
if (bell) {
  const panel = document.querySelector("[data-notif-panel]");
  const list = panel.querySelector("[data-notif-list]");
  const empty = panel.querySelector("[data-notif-empty]");
  const count = bell.querySelector("[data-bell-count]");
  const pushBox = panel.querySelector("[data-push]");
  const pushHelp = panel.querySelector("[data-push-help]");
  const lang = document.documentElement.lang || "fr";
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
  const send = (method, url, body) => fetch(url, { method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  let names = null;
  try { names = new Intl.DisplayNames([lang], { type: "language" }); } catch { /* old browser */ }
  const langName = (c) => { try { return (names && names.of(c)) || c; } catch { return c; } };
  const fWhen = new Intl.DateTimeFormat(lang, { dateStyle: "medium", timeStyle: "short" });
  const pad = (n) => String(n).padStart(2, "0");
  const code = (e) => `S${pad(e.s)}E${pad(e.e)}`;
  const colon = lang.startsWith("fr") ? " : " : ": ";

  let state = { items: [], unread: 0, notify: [], vapidKey: "" };

  function summary(n, d) {
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

  function render() {
    count.hidden = state.unread === 0;
    count.textContent = state.unread > 99 ? "99+" : String(state.unread);
    bell.setAttribute("aria-label", state.unread ? `${t("notif.title")} — ${tf("notif.unread", { n: state.unread })}` : t("notif.title"));
    panel.querySelectorAll("[data-notify]").forEach((x) => { x.checked = state.notify.includes(x.dataset.notify); });
    empty.hidden = state.items.length > 0;
    list.replaceChildren(...state.items.map((n) => {
      let d = {};
      try { d = JSON.parse(n.detail); } catch { /* keep empty */ }
      const lines = summary(n, d);
      return h("li", { class: `notif${n.read ? "" : " is-unread"}` },
        d.poster ? h("img", { class: "notif-poster", src: d.poster + "?w=250", alt: "", loading: "lazy", width: 40, height: 60 }) : h("span", { class: "notif-poster" }),
        h("div", { class: "notif-body" },
          h("p", { class: "notif-title" }, h("strong", { text: n.title }),
            h("span", { class: `notif-kind kind-${n.kind}`, text: t("notif." + n.kind) })),
          lines.map((l) => h("p", { class: "notif-line", text: l })),
          h("p", { class: "notif-when" }, h("time", { datetime: n.at, text: fWhen.format(new Date(n.at)) })),
          h("p", { class: "notif-actions" },
            n.read ? null : h("button", { class: "btn btn-ghost", type: "button", text: t("notif.ack"), onclick: () => ack([n.id]) }),
            n.follows ? h("button", { class: "btn btn-ghost", type: "button", text: t("notif.unfollow"), onclick: (ev) => unfollow(n, ev.currentTarget) }) : null)));
    }));
  }

  async function load() {
    try {
      const res = await fetch("/api/notifications");
      if (!res.ok) return;
      state = await res.json();
      render();
    } catch { /* offline */ }
  }
  async function ack(ids) {
    await send("POST", "/api/notifications/read", { ids });
    await load();
  }
  async function unfollow(n, btn) {
    await send("PUT", "/api/follows", { group: n.group, title: n.title, on: false });
    btn.replaceWith(h("span", { class: "notif-done", text: t("notif.unfollowed") }));
    document.dispatchEvent(new CustomEvent("agendarr:follow", { detail: { group: n.group, on: false } }));
  }

  // Panel: a popover under the bell, closed by Escape or a click outside.
  const setOpen = (open) => {
    panel.hidden = !open;
    bell.setAttribute("aria-expanded", String(open));
    if (open) { load(); panel.querySelector("button, input, summary")?.focus(); }
  };
  bell.addEventListener("click", () => setOpen(panel.hidden));
  document.addEventListener("keydown", (ev) => { if (ev.key === "Escape" && !panel.hidden) { setOpen(false); bell.focus(); } });
  document.addEventListener("click", (ev) => { if (!panel.hidden && !ev.target.closest(".bell-wrap")) setOpen(false); });
  panel.querySelector("[data-notif-all]").addEventListener("click", () => ack([]));
  panel.addEventListener("change", async (ev) => {
    if (!ev.target.dataset.notify) return;
    const notify = [...panel.querySelectorAll("[data-notify]:checked")].map((x) => x.dataset.notify);
    await send("PUT", "/api/notifications/sources", { notify });
    state.notify = notify;
  });

  // Web Push on this device.
  const b64 = (s) => Uint8Array.from(atob(s.replace(/-/g, "+").replace(/_/g, "/").padEnd(Math.ceil(s.length / 4) * 4, "=")), (c) => c.charCodeAt(0));
  const pushSupported = "serviceWorker" in navigator && "PushManager" in window && "Notification" in window;
  async function pushState() {
    if (!window.isSecureContext) { pushBox.disabled = true; pushHelp.textContent = t("notif.push_https"); return; }
    if (!pushSupported) { pushBox.disabled = true; pushHelp.textContent = t("notif.push_unsupported"); return; }
    if (Notification.permission === "denied") { pushBox.disabled = true; pushBox.checked = false; pushHelp.textContent = t("notif.push_denied"); return; }
    const reg = await navigator.serviceWorker.ready;
    pushBox.checked = !!(await reg.pushManager.getSubscription());
    pushHelp.textContent = "";
  }
  pushBox.addEventListener("change", async () => {
    try {
      const reg = await navigator.serviceWorker.ready;
      const current = await reg.pushManager.getSubscription();
      if (!pushBox.checked) {
        if (current) {
          await send("POST", "/api/push", { endpoint: current.endpoint, off: true });
          await current.unsubscribe();
        }
        return;
      }
      if (!state.vapidKey) await load();
      if ((await Notification.requestPermission()) !== "granted") { await pushState(); return; }
      const sub = current || await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: b64(state.vapidKey) });
      const res = await send("POST", "/api/push", { subscription: sub.toJSON(), lang });
      if (!res.ok) { await sub.unsubscribe(); pushBox.checked = false; pushHelp.textContent = t("notif.push_unsupported"); }
    } catch {
      pushBox.checked = false;
      pushHelp.textContent = t("notif.push_unsupported");
    }
  });

  load();
  pushState().catch(() => {});
  // Refresh the count when coming back to the tab, and every two minutes.
  document.addEventListener("visibilitychange", () => { if (document.visibilityState === "visible") load(); });
  setInterval(() => { if (document.visibilityState === "visible") load(); }, 120000);
  // A push message received while the page is open refreshes the bell.
  navigator.serviceWorker?.addEventListener("message", (ev) => { if (ev.data === "agendarr:notification") load(); });
}
