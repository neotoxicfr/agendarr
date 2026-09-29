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
