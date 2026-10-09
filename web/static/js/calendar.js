// agendarr calendar: agenda, week and month views over /api/calendar.
"use strict";

(() => {
  const root = document.querySelector("[data-calendar]");
  if (!root) return;

  // --- i18n ------------------------------------------------------------------
  const I18N = (() => {
    try { return JSON.parse(document.getElementById("i18n").textContent); } catch { return {}; }
  })();
  const t = (key, vars) => {
    let s = I18N[key] || key;
    if (vars) for (const [k, v] of Object.entries(vars)) s = s.split(`{${k}}`).join(v);
    return s;
  };
  const lang = document.documentElement.lang || "fr";
  const fmt = (opts) => new Intl.DateTimeFormat(lang, opts);
  const fDayLong = fmt({ weekday: "long", day: "numeric", month: "long" });
  const fDayFull = fmt({ weekday: "long", day: "numeric", month: "long", year: "numeric" });
  const fMonth = fmt({ month: "long", year: "numeric" });
  const fDayShort = fmt({ day: "numeric", month: "short" });
  const fWeekday = fmt({ weekday: "short" });
  const fTime = fmt({ hour: "2-digit", minute: "2-digit" });
  // Monday first unless the locale says otherwise (en-US starts on Sunday).
  const weekStart = (() => {
    try {
      const info = new Intl.Locale(navigator.language || lang).getWeekInfo?.() || new Intl.Locale(navigator.language || lang).weekInfo;
      if (info && info.firstDay) return info.firstDay % 7;
    } catch { /* older engines */ }
    return 1;
  })();

  // --- dates (local calendar days as "YYYY-MM-DD") -----------------------------
  const pad = (n) => String(n).padStart(2, "0");
  const keyOf = (d) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
  const parseKey = (k) => { const [y, m, d] = k.split("-").map(Number); return new Date(y, m - 1, d); };
  const addDays = (d, n) => new Date(d.getFullYear(), d.getMonth(), d.getDate() + n);
  const startOfWeek = (d) => addDays(d, -((d.getDay() - weekStart + 7) % 7));
  const startOfMonth = (d) => new Date(d.getFullYear(), d.getMonth(), 1);
  const todayKey = () => keyOf(new Date());
  // Episodes carry an exact UTC air time: place them on the viewer's day.
  const dayOf = (e) => (e.air ? keyOf(new Date(e.air)) : e.date);

  // --- state -------------------------------------------------------------------
  // "status" replaced the hideDownloaded checkbox: an old saved choice maps to "upcoming".
  function normFilters(f) {
    if (!["all", "available", "upcoming"].includes(f.status)) f.status = f.hideDownloaded ? "upcoming" : "all";
    delete f.hideDownloaded;
    return f;
  }
  const store = {
    get(k, d) { try { const v = localStorage.getItem("agendarr." + k); return v === null ? d : JSON.parse(v); } catch { return d; } },
    set(k, v) { try { localStorage.setItem("agendarr." + k, JSON.stringify(v)); } catch { /* private mode */ } },
  };
  const wide = window.matchMedia("(min-width: 721px)");
  const params = new URLSearchParams(location.search);
  const state = {
    view: ["agenda", "week", "month"].includes(params.get("view")) ? params.get("view") : store.get("view", wide.matches ? "month" : "agenda"),
    anchor: /^\d{4}-\d{2}-\d{2}$/.test(params.get("date") || "") ? parseKey(params.get("date")) : new Date(),
    filters: normFilters(Object.assign({ movies: true, episodes: true, status: "all", hideUnmonitored: false, instances: {} }, store.get("filters", {}))),
    mine: store.get("mine", []),
    shortcuts: store.get("shortcuts", true),
    sites: store.get("sites", ["tmdb", "imdb"]),
    formats: store.get("formats", "logos"),
    agendaWeeks: 5,
    selected: null, // phone month view: selected day key
  };

  const $ = (sel) => root.querySelector(sel);
  const body = $("[data-cal-body]");
  const titleEl = $("[data-cal-title]");
  const warning = $("[data-cal-warning]");
  const dialog = $("[data-detail]");
  const detailBody = $("[data-detail-body]");
  const isAdmin = root.dataset.admin === "true";

  const el = (tag, attrs = {}, ...children) => {
    const n = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs)) {
      if (v === false || v == null) continue;
      if (k === "class") n.className = v;
      else if (k === "text") n.textContent = v;
      else if (k.startsWith("on")) n.addEventListener(k.slice(2), v);
      else n.setAttribute(k, v === true ? "" : v);
    }
    for (const c of children.flat()) if (c != null && c !== false) n.append(c);
    return n;
  };

  // --- data --------------------------------------------------------------------
  const cache = new Map();
  async function load(from, to, signal) {
    const mine = state.mine.join(",");
    const key = `${keyOf(from)}_${keyOf(to)}_${mine}`;
    if (cache.has(key)) return cache.get(key);
    const res = await fetch(`/api/calendar?from=${keyOf(from)}&to=${keyOf(to)}${mine ? "&mine=" + mine : ""}`, { signal });
    if (res.status === 401) { location.assign("/login"); throw new Error("signed out"); }
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const data = await res.json();
    cache.set(key, data);
    setTimeout(() => cache.delete(key), 60_000);
    return data;
  }

  // The API serves at most 62 days. The growing agenda is fetched in 35-day
  // slices aligned on its first day, so loading more weeks only asks for
  // the new slice; the others come from the cache.
  const SLICE = 35;
  let inflight = null;
  async function loadRange(from, to) {
    if (inflight) inflight.abort();
    inflight = new AbortController();
    const parts = [];
    for (let a = from; a <= to; a = addDays(a, SLICE)) {
      const b = addDays(a, SLICE - 1);
      parts.push(load(a, b < to ? b : to, inflight.signal));
    }
    const all = await Promise.all(parts);
    if (all.length === 1) return all[0];
    const seen = new Set();
    const out = { events: [], failing: [], personal: {}, configured: false };
    for (const d of all) {
      out.configured ||= d.configured;
      for (const e of d.events) if (!seen.has(e.id)) { seen.add(e.id); out.events.push(e); }
      for (const f of d.failing) if (!out.failing.includes(f)) out.failing.push(f);
      Object.assign(out.personal, d.personal || {});
    }
    return out;
  }

  // --- filters -----------------------------------------------------------------
  const visible = (e) => {
    const f = state.filters;
    if (e.kind === "movie" && !f.movies) return false;
    if (e.kind === "episode" && !f.episodes) return false;
    if (f.status === "available" && !e.hasFile) return false;
    if (f.status === "upcoming" && e.hasFile) return false;
    if (f.hideUnmonitored && !e.monitored) return false;
    if (e.instances.length && e.instances.every((n) => f.instances[n] === false)) return false;
    return true;
  };
  const activeFilterCount = () => {
    const f = state.filters;
    return (!f.movies) + (!f.episodes) + (f.status !== "all") + f.hideUnmonitored + Object.values(f.instances).filter((v) => v === false).length + state.mine.length;
  };
  const knownInstances = new Set();
  function syncInstanceFilters(events) {
    let added = false;
    for (const e of events) for (const n of e.instances) if (!knownInstances.has(n)) { knownInstances.add(n); added = true; }
    if (!added) return;
    const box = $("[data-instances]");
    box.replaceChildren(...[...knownInstances].sort().map((n) =>
      el("label", { class: "check" },
        el("input", { type: "checkbox", "data-instance": n, checked: state.filters.instances[n] !== false }), " ", n)));
    $("[data-instances-group]").hidden = knownInstances.size < 2;
  }

  // --- labels ------------------------------------------------------------------
  const code = (e) => e.bundle
    ? t("cal.episode_range", { s: pad(e.season), a: pad(e.episode), b: pad(e.bundle[e.bundle.length - 1].episode) })
    : t("cal.episode_code", { s: pad(e.season), e: pad(e.episode) });
  const timeOf = (e) => (e.air ? fTime.format(new Date(e.air)) : "");
  const statusOf = (e) => (e.hasFile ? "downloaded" : e.monitored ? "expected" : "unmonitored");
  const tagsOf = (e) => {
    const out = [];
    if (e.kind === "movie") out.push(t("cal.release_" + e.release));
    if (e.kind === "episode" && e.premiere) out.push(t("cal.premiere"));
    if (e.finale) out.push(t("cal.finale_" + e.finale));
    return out;
  };
  const describe = (e) => [
    e.title,
    e.bundle ? `${code(e)} (${t("cal.episodes_n", { n: e.bundle.length })})` : null,
    e.kind === "episode" && !e.bundle ? t("cal.season_episode", { s: e.season, e: e.episode }) : null,
    e.kind === "episode" && !e.bundle && e.epTitle ? e.epTitle : null,
    ...tagsOf(e),
    timeOf(e) || null,
    t("cal.status_" + statusOf(e)),
  ].filter(Boolean).join(", ");

  function poster(e, cls, width) {
    const wrap = el("span", { class: `poster ${cls} is-${e.kind}`, "aria-hidden": "true" },
      el("span", { class: "poster-fallback", text: (e.title || "?").trim().charAt(0).toUpperCase() }));
    if (e.poster) {
      const img = el("img", { src: `${e.poster}${width === 500 ? "?w=500" : ""}`, alt: "", loading: "lazy", decoding: "async" });
      img.addEventListener("error", () => img.remove());
      img.addEventListener("load", () => wrap.classList.add("has-img"));
      wrap.append(img);
    }
    return wrap;
  }

  // --- items -------------------------------------------------------------------
  // Compact chip for month cells.
  const chip = (e) => el("button", {
    type: "button", class: `chip is-${e.kind} is-${statusOf(e)}`, "aria-label": describe(e), title: describe(e), "data-id": e.id,
    onclick: () => openDetail(e),
  },
  el("span", { class: "chip-dot", "aria-hidden": "true" }),
  timeOf(e) ? el("span", { class: "chip-time", text: timeOf(e) }) : null,
  el("span", { class: "chip-title", text: e.title }),
  e.bundle ? el("span", { class: "chip-code", text: `×${e.bundle.length}` }) : null);

  // Card with poster for week and agenda.
  const statusEl = (e) => el("span", { class: `status status-${statusOf(e)}`, text: t("cal.status_" + statusOf(e)) });
  const card = (e, size) => el("button", {
    type: "button", class: `card card-${size} is-${e.kind} is-${statusOf(e)}`, "aria-label": describe(e), "data-id": e.id,
    onclick: () => openDetail(e),
  },
  (() => {
    const p = poster(e, "poster-sm", 250);
    // Week columns are narrow: the status sits on the poster so every
    // card keeps the same height.
    if (size === "sm") p.append(el("span", { class: "poster-badge" }, statusEl(e)));
    return p;
  })(),
  el("span", { class: "card-body" },
    el("span", { class: "card-title", text: e.title }),
    el("span", { class: "card-sub" },
      e.kind === "episode"
        ? [el("span", { class: "card-code", text: code(e) }),
          e.bundle ? el("span", { class: "card-ep", text: t("cal.episodes_n", { n: e.bundle.length }) })
            : e.epTitle ? el("span", { class: "card-ep", text: e.epTitle }) : null]
        : el("span", { text: e.year ? String(e.year) : "" })),
    el("span", { class: "card-meta" },
      timeOf(e) ? el("span", { class: "card-time", text: timeOf(e) }) : null,
      ...tagsOf(e).map((x) => el("span", { class: "pill", text: x })),
      statusEl(e))));

  // --- views -------------------------------------------------------------------
  function range() {
    const a = state.anchor;
    if (state.view === "month") {
      const first = startOfMonth(a);
      const start = startOfWeek(first);
      const lastOfMonth = new Date(first.getFullYear(), first.getMonth() + 1, 0);
      const end = addDays(startOfWeek(lastOfMonth), 6);
      return { from: start, to: end };
    }
    if (state.view === "week") {
      const start = startOfWeek(a);
      return { from: start, to: addDays(start, 6) };
    }
    const start = startOfWeek(a);
    return { from: start, to: addDays(start, 7 * state.agendaWeeks - 1) };
  }

  function title() {
    const a = state.anchor;
    if (state.view === "month") return fMonth.format(a);
    const { from, to } = range();
    const end = state.view === "week" ? to : addDays(from, 7 * 5 - 1);
    return `${fDayShort.format(from)} – ${fDayShort.format(end)} ${end.getFullYear()}`;
  }

  // Groups events by local day, folding several episodes of one season
  // released the same day (binge drops, double bills) into one bundle.
  function byDay(events) {
    const m = new Map();
    const bundles = new Map();
    for (const e of events) {
      const k = dayOf(e);
      if (!m.has(k)) m.set(k, []);
      if (e.kind === "episode") {
        const bk = `${k}|${e.group}|${e.season}`;
        const b = bundles.get(bk);
        if (b) {
          if (!b.bundle) {
            const first = { ...b };
            Object.assign(b, { bundle: [first], id: first.id + "+" });
          }
          b.bundle.push(e);
          b.bundle.sort((x, y) => x.episode - y.episode);
          Object.assign(b, {
            episode: b.bundle[0].episode,
            hasFile: b.bundle.every((x) => x.hasFile),
            monitored: b.bundle.some((x) => x.monitored),
            premiere: b.bundle.some((x) => x.premiere),
            finale: b.bundle.map((x) => x.finale).filter(Boolean).pop() || "",
          });
          continue;
        }
        const copy = { ...e };
        bundles.set(bk, copy);
        m.get(k).push(copy);
        continue;
      }
      m.get(k).push(e);
    }
    return m;
  }

  function renderMonth(events, from, to) {
    const days = byDay(events);
    const month = state.anchor.getMonth();
    const today = todayKey();
    // Selected day (phone): kept if in this month, else today, else the 1st.
    const inMonth = (k) => k && parseKey(k).getMonth() === month && parseKey(k).getFullYear() === state.anchor.getFullYear();
    const selectedKey = inMonth(state.selected) ? state.selected : inMonth(today) ? today : keyOf(startOfMonth(state.anchor));
    const table = el("table", { class: "month", "aria-labelledby": "cal-title" });
    const head = el("tr");
    for (let i = 0; i < 7; i++) {
      const d = addDays(from, i);
      head.append(el("th", { scope: "col", abbr: fmt({ weekday: "long" }).format(d), text: fWeekday.format(d) }));
    }
    table.append(el("thead", {}, head));
    const tbody = el("tbody");
    const max = wide.matches ? 4 : 0;
    for (let d = from; d <= to; d = addDays(d, 7)) {
      const row = el("tr");
      for (let i = 0; i < 7; i++) {
        const day = addDays(d, i);
        const k = keyOf(day);
        const list = days.get(k) || [];
        const cell = el("td", {
          class: ["day", day.getMonth() !== month && "is-other", k === today && "is-today", (day.getDay() === 0 || day.getDay() === 6) && "is-weekend"].filter(Boolean).join(" "),
        });
        const label = t("cal.day_aria", { date: fDayLong.format(day), n: list.length });
        if (max === 0) {
          // Phone: the cell selects the day, whose releases list below the grid.
          cell.append(el("button", {
            type: "button", class: "day-btn", "aria-label": label, "aria-pressed": String(k === selectedKey),
            "data-day": k,
            onclick: () => { state.selected = k; render({ keepScroll: true }); },
          },
          el("span", { class: "day-num", text: String(day.getDate()), "aria-current": k === today ? "date" : false }),
          el("span", { class: "day-dots", "aria-hidden": "true" },
            ...list.slice(0, list.length > 4 ? 3 : 4).map((e) => el("span", { class: `dot is-${e.kind} is-${statusOf(e)}` })),
            list.length > 4 ? el("span", { class: "dot-more", text: `+${list.length - 3}` }) : null)));
        } else {
          cell.append(el("span", { class: "day-num", "aria-hidden": "true", text: String(day.getDate()) }),
            el("span", { class: "sr-only", text: label }));
          const shown = list.length > max ? list.slice(0, max - 1) : list;
          const ul = el("ul", { class: "day-list" }, shown.map((e) => el("li", {}, chip(e))));
          if (list.length > shown.length) {
            const rest = list.length - shown.length;
            ul.append(el("li", {}, el("button", {
              type: "button", class: "more", "aria-label": t("cal.more_aria", { n: rest, date: fDayLong.format(day) }),
              onclick: () => openDay(day, list), text: t("cal.more", { n: rest }),
            })));
          }
          cell.append(ul);
        }
        row.append(cell);
      }
      tbody.append(row);
    }
    table.append(tbody);
    if (max > 0) return table;
    const sel = parseKey(selectedKey);
    const list = days.get(selectedKey) || [];
    return el("div", { class: "month-phone" }, table,
      el("section", { class: "month-day", "aria-label": fDayFull.format(sel) },
        el("h2", { class: "agenda-head" }, el("span", { class: "agenda-date", text: fDayLong.format(sel) })),
        list.length
          ? el("ul", { class: "agenda-list" }, list.map((e) => el("li", {}, card(e, "lg"))))
          : el("p", { class: "cal-empty-inline", text: t("cal.empty") })));
  }

  function renderWeek(events, from) {
    const days = byDay(events);
    const today = todayKey();
    const ol = el("ol", { class: "week" });
    for (let i = 0; i < 7; i++) {
      const day = addDays(from, i);
      const k = keyOf(day);
      const list = days.get(k) || [];
      ol.append(el("li", { class: `week-day${k === today ? " is-today" : ""}${list.length ? "" : " is-empty"}` },
        el("h2", { class: "week-head" },
          el("span", { class: "week-name", text: fWeekday.format(day), "aria-hidden": "true" }),
          el("span", { class: "week-num", text: String(day.getDate()), "aria-hidden": "true", "data-today": k === today ? "" : false }),
          el("span", { class: "sr-only", text: fDayFull.format(day) })),
        el("ul", { class: "week-list" }, list.map((e) => el("li", {}, card(e, "sm"))))));
    }
    return ol;
  }

  function renderAgenda(events, from, to) {
    const days = byDay(events);
    const today = todayKey();
    const wrap = el("div", { class: "agenda" });
    for (let d = from; d <= to; d = addDays(d, 1)) {
      const k = keyOf(d);
      const list = days.get(k);
      // Today always gets its section, even empty, so "tonight" is findable.
      if (!list && k !== today) continue;
      wrap.append(el("section", { class: `agenda-day${k === today ? " is-today" : ""}`, "aria-label": fDayFull.format(d), "data-agenda-day": k },
        el("h2", { class: "agenda-head" },
          el("span", { class: "agenda-date", text: fDayLong.format(d) }),
          k === today ? el("span", { class: "pill pill-accent", text: t("cal.today") }) : null),
        list
          ? el("ul", { class: "agenda-list" }, list.map((e) => el("li", {}, card(e, "lg"))))
          : el("p", { class: "cal-empty-inline", text: t("cal.today_empty") })));
    }
    return wrap;
  }

  // --- render loop ---------------------------------------------------------------
  let renderSeq = 0;
  async function render({ keepScroll = false } = {}) {
    const seq = ++renderSeq;
    titleEl.textContent = title();
    root.dataset.view = state.view;
    root.querySelectorAll("[name=view]").forEach((r) => { r.checked = r.value === state.view; });
    const url = new URL(location.href);
    url.searchParams.set("view", state.view);
    url.searchParams.set("date", keyOf(state.anchor));
    history.replaceState(null, "", url);
    const count = activeFilterCount();
    const badge = $("[data-filters-count]");
    badge.hidden = count === 0;
    badge.replaceChildren(el("span", { "aria-hidden": "true", text: String(count) }),
      el("span", { class: "sr-only", text: ", " + t("cal.filters_active", { n: count }) }));
    // Remember what had the focus, to put it back after the re-render.
    const active = document.activeElement;
    const refocus = active && body.contains(active)
      ? (active.dataset.id && `[data-id="${CSS.escape(active.dataset.id)}"]`) ||
        (active.dataset.day && `[data-day="${active.dataset.day}"]`) ||
        (active.hasAttribute("data-load-more") && "[data-load-more]") || null
      : null;

    const { from, to } = range();
    body.setAttribute("aria-busy", "true");
    if (!keepScroll) body.classList.add("is-loading");
    let data;
    // Syncing a long Plex history the first time takes a few seconds: say so.
    const slow = state.mine.length ? setTimeout(() => { warning.hidden = false; warning.textContent = t("cal.mine_loading"); }, 1500) : 0;
    try {
      data = await loadRange(from, to);
    } catch (err) {
      clearTimeout(slow);
      if (err.name === "AbortError" || seq !== renderSeq) return;
      body.replaceChildren(el("div", { class: "cal-empty", role: "alert" },
        el("p", { text: t("cal.error") }),
        el("button", { type: "button", class: "btn", onclick: () => render(), text: t("cal.retry") })));
      body.setAttribute("aria-busy", "false");
      body.classList.remove("is-loading");
      return;
    }
    clearTimeout(slow);
    if (seq !== renderSeq) return;
    body.classList.remove("is-loading");
    body.setAttribute("aria-busy", "false");

    const notes = [];
    if (data.failing.length) notes.push(t("cal.failing", { names: data.failing.join(", ") }));
    for (const [src, code] of Object.entries(data.personal || {})) notes.push(t("cal.mine_error_" + src, { why: t("cal.why_" + code) }));
    warning.hidden = notes.length === 0;
    warning.replaceChildren(...notes.map((n) => el("p", { text: n })));

    if (!data.configured) {
      body.replaceChildren(el("div", { class: "cal-empty", role: "status" },
        el("p", { text: t(isAdmin ? "cal.no_instances_admin" : "cal.no_instances_user") }),
        isAdmin ? el("a", { class: "btn btn-primary", href: "/admin/instances", text: t("cal.no_instances_admin_link") }) : null));
      return;
    }
    syncInstanceFilters(data.events);
    const events = data.events.filter(visible);
    let view;
    if (state.view === "month") view = renderMonth(events, from, to);
    else if (state.view === "week") view = renderWeek(events, from);
    else view = renderAgenda(events, from, to);

    const nodes = [view];
    if (state.view !== "month" && events.length === 0) {
      nodes.unshift(el("p", { class: "cal-empty-inline", text: t(data.events.length ? "cal.empty_filtered" : "cal.empty") }));
    }
    if (state.view === "agenda") {
      nodes.push(el("div", { class: "agenda-more" }, el("button", {
        type: "button", class: "btn", "data-load-more": true, text: t("cal.load_more"),
        onclick: () => { state.agendaWeeks += 5; render({ keepScroll: true }); },
      })));
    }
    body.replaceChildren(...nodes);
    if (state.view === "agenda") {
      // Stop loading by itself once the last slice came back empty (nothing
      // scheduled that far out); the button still loads more on demand.
      const tail = keyOf(addDays(to, -(SLICE - 1)));
      if (events.some((e) => dayOf(e) >= tail)) observeMore();
      else if (observer) observer.disconnect();
    }
    if (refocus) body.querySelector(refocus)?.focus({ preventScroll: true });
    else if (!keepScroll && state.view === "agenda") {
      // Only when today is below the fold: otherwise the toolbar would
      // scroll away for nothing.
      const td = body.querySelector(`[data-agenda-day="${todayKey()}"]`);
      if (td && td.getBoundingClientRect().top > window.innerHeight * 0.6) td.scrollIntoView({ block: "start" });
    }
  }

  // Infinite scroll in the agenda: load the next weeks as the button nears.
  let observer = null;
  function observeMore() {
    if (observer) observer.disconnect();
    const btn = body.querySelector("[data-load-more]");
    if (!btn || !("IntersectionObserver" in window) || state.agendaWeeks >= 60) return;
    observer = new IntersectionObserver((entries) => {
      if (entries.some((x) => x.isIntersecting)) {
        observer.disconnect();
        state.agendaWeeks += 5;
        render({ keepScroll: true });
      }
    }, { rootMargin: "400px" });
    observer.observe(btn);
  }

  // --- navigation ----------------------------------------------------------------
  function step(dir) {
    const a = state.anchor;
    if (state.view === "month") state.anchor = new Date(a.getFullYear(), a.getMonth() + dir, 1);
    else if (state.view === "week") state.anchor = addDays(a, 7 * dir);
    else { state.anchor = addDays(startOfWeek(a), 7 * 5 * dir); state.agendaWeeks = 5; }
    render();
  }
  function setView(v) {
    if (v === state.view) return;
    state.view = v;
    state.agendaWeeks = 5;
    store.set("view", v);
    render();
  }
  function goToday() {
    state.anchor = new Date();
    state.agendaWeeks = 5;
    render();
  }

  root.addEventListener("click", (ev) => {
    const b = ev.target.closest("[data-cal]");
    if (!b) return;
    if (b.dataset.cal === "prev") step(-1);
    else if (b.dataset.cal === "next") step(1);
    else goToday();
  });
  root.addEventListener("change", (ev) => {
    const x = ev.target;
    if (x.name === "view") setView(x.value);
    else if (x.name === "status") { state.filters.status = x.value; saveFilters(); }
    else if (x.name === "formats") { state.formats = x.value; store.set("formats", x.value); savePrefs(); }
    else if (x.dataset.filter) { state.filters[x.dataset.filter] = x.checked; saveFilters(); }
    else if (x.dataset.instance) { state.filters.instances[x.dataset.instance] = x.checked; saveFilters(); }
    else if (x.dataset.mine) {
      state.mine = [...root.querySelectorAll("[data-mine]:checked")].map((c) => c.dataset.mine);
      saveFilters();
    } else if (x.hasAttribute("data-shortcuts")) { state.shortcuts = x.checked; syncControls(); savePrefs(); }
    else if (x.dataset.site) {
      state.sites = [...root.querySelectorAll("[data-site]:checked")].map((c) => c.dataset.site);
      store.set("sites", state.sites);
      savePrefs();
    }
  });
  function saveFilters() {
    store.set("filters", state.filters);
    store.set("mine", state.mine);
    savePrefs();
    render({ keepScroll: true });
  }
  // Preferences live on the server (the iCal feed uses them); localStorage
  // only makes the first paint instant.
  let saveTimer = 0;
  function savePrefs() {
    store.set("shortcuts", state.shortcuts);
    clearTimeout(saveTimer);
    saveTimer = setTimeout(() => {
      fetch("/api/prefs", {
        method: "PUT", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ mine: state.mine, filters: state.filters, shortcuts: state.shortcuts, sites: state.sites, formats: state.formats }),
      }).catch(() => {});
    }, 400);
  }
  function syncControls() {
    root.querySelectorAll("[data-filter]").forEach((x) => { x.checked = !!state.filters[x.dataset.filter]; });
    root.querySelectorAll("input[name=status]").forEach((x) => { x.checked = x.value === state.filters.status; });
    root.querySelectorAll("input[name=formats]").forEach((x) => { x.checked = x.value === state.formats; });
    root.querySelectorAll("[data-mine]").forEach((x) => { x.checked = state.mine.includes(x.dataset.mine); });
    root.querySelectorAll("[data-site]").forEach((x) => { x.checked = state.sites.includes(x.dataset.site); });
    const sc = $("[data-shortcuts]");
    sc.checked = state.shortcuts;
    $("[data-shortcuts-note]").hidden = !state.shortcuts;
  }
  syncControls();

  document.addEventListener("keydown", (ev) => {
    if (!state.shortcuts || ev.defaultPrevented || ev.altKey || ev.ctrlKey || ev.metaKey || dialog.open) return;
    if (root.classList.contains("filters-open")) return;
    if (ev.target.closest("input, textarea, select, [contenteditable]")) return;
    const k = ev.key;
    if (k === "ArrowLeft") step(-1);
    else if (k === "ArrowRight") step(1);
    else if (k === "t" || k === "T") goToday();
    else if (k === "1") setView("agenda");
    else if (k === "2") setView("week");
    else if (k === "3") setView("month");
    else return;
    ev.preventDefault();
  });

  // Horizontal swipe changes period in week and month views (the agenda
  // scrolls vertically and keeps native gestures).
  let touch = null;
  body.addEventListener("touchstart", (ev) => {
    if (ev.touches.length === 1) touch = { x: ev.touches[0].clientX, y: ev.touches[0].clientY, at: Date.now() };
  }, { passive: true });
  body.addEventListener("touchend", (ev) => {
    if (!touch || state.view === "agenda") return;
    const dx = ev.changedTouches[0].clientX - touch.x;
    const dy = ev.changedTouches[0].clientY - touch.y;
    const fast = Date.now() - touch.at < 600;
    touch = null;
    if (fast && Math.abs(dx) > 60 && Math.abs(dx) > 2 * Math.abs(dy)) step(dx < 0 ? 1 : -1);
  }, { passive: true });

  // --- filters panel (drawer on phones) --------------------------------------------
  // Below 1100px the filters are a drawer: modal while open (the rest of the
  // page is inert, a scrim closes it, Escape works from anywhere).
  const side = $("[data-cal-filters]");
  const openBtn = $("[data-filters-open]");
  const scrim = $("[data-scrim]");
  const behind = () => [$(".cal-main"), document.querySelector(".topbar"), document.querySelector(".footer"), document.querySelector(".nav")].filter(Boolean);
  const setDrawer = (open) => {
    root.classList.toggle("filters-open", open);
    openBtn.setAttribute("aria-expanded", String(open));
    scrim.hidden = !open;
    behind().forEach((n) => { n.inert = open; });
    if (open) side.querySelector("input, button")?.focus();
    else openBtn.focus();
  };
  openBtn.addEventListener("click", () => setDrawer(!root.classList.contains("filters-open")));
  $("[data-filters-close]").addEventListener("click", () => setDrawer(false));
  scrim.addEventListener("click", () => setDrawer(false));
  document.addEventListener("keydown", (ev) => { if (ev.key === "Escape" && root.classList.contains("filters-open")) setDrawer(false); });
  // Growing past the breakpoint with the drawer open: undo the modal state.
  window.matchMedia("(min-width: 1101px)").addEventListener("change", (m) => {
    if (m.matches && root.classList.contains("filters-open")) setDrawer(false);
  });

  // --- detail sheet --------------------------------------------------------------------
  function show(content) {
    detailBody.replaceChildren(content);
    if (!dialog.open) dialog.showModal();
    detailBody.scrollTop = 0;
  }
  dialog.addEventListener("click", (ev) => { if (ev.target === dialog) dialog.close(); });
  $("[data-detail-close]").addEventListener("click", () => dialog.close());

  // File details from Radarr/Sonarr: one block per instance holding a file.
  const langName = (() => {
    let dn = null;
    try { dn = new Intl.DisplayNames([lang], { type: "language" }); } catch { /* old browser */ }
    return (c) => { try { return (dn && dn.of(c)) || c; } catch { return c; } };
  })();
  // Languages show as flags (the name stays for tooltips and screen readers).
  // A language is not a country: a tag's own region wins (en-US, fr-CA),
  // and a language without an obvious flag stays written out.
  const REGION = { fr: "FR", en: "GB", ja: "JP", de: "DE", es: "ES", it: "IT", pt: "PT", ru: "RU", ko: "KR", zh: "CN",
    nl: "NL", sv: "SE", da: "DK", no: "NO", nb: "NO", nn: "NO", fi: "FI", pl: "PL", cs: "CZ", sk: "SK", hu: "HU",
    ro: "RO", el: "GR", tr: "TR", uk: "UA", he: "IL", hi: "IN", th: "TH", vi: "VN", id: "ID", ms: "MY", bg: "BG",
    hr: "HR", sr: "RS", sl: "SI", et: "EE", lv: "LV", lt: "LT", is: "IS", ga: "IE", fa: "IR", fil: "PH", tl: "PH" };
  const flagOf = (tag) => {
    const [base, region] = String(tag || "").split("-");
    const r = /^[A-Za-z]{2}$/.test(region || "") ? region.toUpperCase() : REGION[base.toLowerCase()];
    return r ? String.fromCodePoint(...[...r].map((c) => 0x1f1e6 + c.charCodeAt(0) - 65)) : "";
  };
  const langBadge = (tag) => {
    const name = tag ? langName(tag) : "?";
    const f = flagOf(tag);
    return f ? el("span", { class: "flag", role: "img", "aria-label": name, title: name, text: f }) : el("span", { text: name });
  };
  const langList = (tags) => (tags || []).length ? el("span", { class: "langs" }, tags.map(langBadge)) : null;
  const CODEC = { eac3: "E-AC3", ac3: "AC3", truehd: "TrueHD", dca: "DTS", dts: "DTS", aac: "AAC", flac: "FLAC", opus: "Opus",
    mp3: "MP3", pcm: "PCM", srt: "SRT", subrip: "SRT", pgs: "PGS", hdmv_pgs_subtitle: "PGS", ass: "ASS", ssa: "SSA",
    vobsub: "VobSub", dvd_subtitle: "VobSub", mov_text: "TX3G", webvtt: "WebVTT" };
  const LAYOUT = { mono: "1.0", stereo: "2.0" };
  const codecName = (c) => CODEC[(c || "").toLowerCase()] || (c || "").toUpperCase();
  // Tracks line up in a grid: flag | format | title and tags.
  const trackItem = (x, audio) => {
    const title = x.title;
    const tags = [x.forced && "cal.track_forced", x.hi && "cal.track_hi", x.external && "cal.track_external"].filter(Boolean);
    return el("li", {},
      langBadge(x.lang),
      el("span", { class: "track-format" }, badges(audio ? audioBadges(x.codec, x.layout, x.profile) : [codecName(x.codec)])),
      el("span", { class: "track-more" },
        title ? el("span", { class: "track-title", text: title }) : null,
        ...tags.map((k) => el("span", { class: "pill", text: t(k) }))));
  };
  const trackList = (list, audio) => (list?.length ? el("ul", { class: "tracks" }, list.map((x) => trackItem(x, audio))) : null);
  const fSize = new Intl.NumberFormat(lang, { style: "unit", unit: "gigabyte", maximumFractionDigits: 1 });
  // Formats read as badges: "4K", "WEBRip", "Dolby Vision", "HEVC", "Atmos".
  // In "logos" mode, Dolby, DTS and HDR formats show their logo; each one's
  // file and proportions live in app.css (.logo-*), the CSP forbids inline styles.
  const LOGO = { DV: "dolby-vision", HDR10: "hdr10", "HDR10+": "hdr10plus", Atmos: "dolby-atmos", "E-AC3": "dolby-digital-plus",
    AC3: "dolby-digital", TrueHD: "dolby-truehd", DTS: "dts", "DTS-HD MA": "dts-hd-ma" };
  const badge = (x) => {
    const logo = state.formats !== "text" && LOGO[x];
    if (!logo) return el("span", { class: "fmt", text: x, title: FULL[x] });
    return el("span", { class: `logo logo-${logo}`, role: "img", "aria-label": FULL[x] || x, title: FULL[x] || x });
  };
  const badges = (list) => {
    const items = list.filter(Boolean);
    return items.length ? el("span", { class: "fmts" }, items.map(badge)) : null;
  };
  const FULL = { DV: "Dolby Vision", "E-AC3": "Dolby Digital Plus", AC3: "Dolby Digital", TrueHD: "Dolby TrueHD", Atmos: "Dolby Atmos",
    "DTS-HD MA": "DTS-HD Master Audio" };
  const HDR = { DV: "DV", HDR10PLUS: "HDR10+", HDR10: "HDR10", HLG: "HLG", PQ: "HDR" };
  const hdrNames = (h) => (h || "").split(/\s+/).filter(Boolean).map((x) => HDR[x.toUpperCase()] || x);
  const VIDEO = { X265: "HEVC", H265: "HEVC", HEVC: "HEVC", X264: "H.264", H264: "H.264", AVC: "H.264", AV1: "AV1", VP9: "VP9",
    MPEG2: "MPEG-2", MPEG2VIDEO: "MPEG-2", VC1: "VC-1", XVID: "Xvid", DIVX: "DivX" };
  const videoName = (c) => VIDEO[(c || "").toUpperCase().replace(/[^A-Z0-9]/g, "")] || c;
  // "EAC3 Atmos" from Radarr, "eac3" + profile from Plex: the same badges.
  const DTS = { ma: "DTS-HD MA", hra: "DTS-HD HRA", x: "DTS:X", "dts:x": "DTS:X", es: "DTS-ES" };
  const audioBadges = (codec, channels, profile = "") => {
    const atmos = /atmos/i.test(codec || "") || /atmos/i.test(profile);
    const base = (codec || "").replace(/\s*atmos\s*/i, " ").trim();
    const name = /^(dca|dts)$/i.test(base) && DTS[profile.toLowerCase()] ? DTS[profile.toLowerCase()] : codecName(base);
    return [name, atmos ? "Atmos" : "", LAYOUT[channels] || channels];
  };
  const row = (label, value, extra = {}) => (value
    ? [el("dt", { text: t(label), ...extra }), el("dd", extra, value)] : null);
  // The file block, from Radarr/Sonarr's reading of the file: only used when
  // Plex does not have the release (Plex's own analysis wins, see loadFile).
  // One file: a header with its size, then video, audio and subtitles on
  // three lines, and every track folded under "Tracks (n)".
  const fileBlock = ({ title, size, video, audio, audioLangs, subLangs, tracks }) => el("section", { class: "detail-file" },
    el("div", { class: "file-head" },
      el("h3", { text: title || t("cal.detail_file") }),
      size ? el("span", { class: "file-size", text: fSize.format(size / 1e9) }) : null),
    el("dl", { class: "detail-facts" },
      row("cal.detail_video", badges(video)),
      row("cal.detail_audio", audio.length || audioLangs.length ? el("span", { class: "langs-line" }, badges(audio), langList(audioLangs)) : null),
      row("cal.detail_subs", langList(subLangs))),
    tracks);
  const videoBadges = (res, hdr, codec, depth) => [res, ...hdr, [videoName(codec), depth > 8 ? `${depth}-bit` : ""].filter(Boolean).join(" ")];
  // Radarr/Sonarr's reading of the file: only when Plex does not have it.
  const files = (e) => (e.media || []).map((m) => fileBlock({
    title: e.media.length > 1 ? m.instance : "", size: m.size,
    video: videoBadges(m.resolution, hdrNames(m.hdr), m.videoCodec, m.bitDepth),
    audio: m.audioCodec ? audioBadges(m.audioCodec, m.channels) : [], audioLangs: m.audioLangs || [], subLangs: m.subs || [],
  }));
  const uniq = (list) => [...new Set(list.filter(Boolean))];
  // Every format of a file's audio, in track order: codecs, then Atmos, then layouts.
  const audioSummary = (tracks) => {
    const b = tracks.map((x) => audioBadges(x.codec, x.layout, x.profile));
    return uniq([...b.map((x) => x[0]), ...b.map((x) => x[1]), ...b.map((x) => x[2])]);
  };
  // One version as Plex analysed it.
  const plexFile = (v, many) => {
    const audio = v.audio || [], subs = v.subs || [];
    const n = audio.length + subs.length;
    return fileBlock({
      title: many ? v.resolution : "", size: v.size,
      video: videoBadges(v.resolution, v.hdr || [], v.videoCodec, v.bitDepth),
      audio: audioSummary(audio), audioLangs: uniq(audio.map((x) => x.lang)), subLangs: uniq(subs.map((x) => x.lang)),
      tracks: n ? el("details", { class: "detail-tracks" },
        el("summary", { text: t("cal.tracks", { n }) }),
        audio.length ? [el("h4", { text: t("cal.detail_audio") }), trackList(audio, true)] : null,
        subs.length ? [el("h4", { text: t("cal.detail_subs") }), trackList(subs, false)] : null) : null,
    });
  };
  const links = (e) => {
    const list = [
      e.plexUrl ? el("a", { class: "btn btn-primary", href: e.plexUrl, target: "_blank", rel: "noopener", text: t("cal.open_plex") }) : null,
      ...(e.arrLinks || []).map((l) => el("a", { class: "btn", href: l.url, target: "_blank", rel: "noopener", text: t("cal.open_in", { name: l.label }) })),
    ].filter(Boolean);
    const ext = (e.ext || []).filter((l) => state.sites.includes(l.id)).map((l) => el("a", { href: l.url, target: "_blank",
      rel: "noopener noreferrer", title: t("cal.open_in", { name: l.label }), text: l.label }));
    return [
      list.length ? el("p", { class: "detail-actions" }, list) : null,
      ext.length ? el("p", { class: "detail-ext" }, el("span", { text: t("cal.see_on") }), ext) : null,
    ];
  };

  // What Plex found in the file(s), once the sheet is open. Until then, and
  // when Plex does not have the release, the Radarr/Sonarr reading shows.
  let fileFor = null;
  async function loadFile(e, article, slot) {
    fileFor = e;
    const q = new URLSearchParams({ group: e.group });
    if (e.kind === "episode") { q.set("season", e.season); q.set("episode", e.episode); }
    let versions = [];
    try {
      const data = await (await fetch("/api/tracks?" + q)).json();
      versions = (data.versions || []).filter((v) => v.videoCodec || v.audio?.length || v.subs?.length);
    } catch { /* Plex unreachable: fall back below */ }
    if (fileFor !== e || !article.isConnected) return;
    slot.replaceChildren(...(versions.length ? versions.map((v) => plexFile(v, versions.length > 1)) : files(e)));
  }

  function openDetail(e) {
    const day = parseKey(dayOf(e));
    const when = [fDayFull.format(day), timeOf(e)].filter(Boolean).join(" · ");
    fileFor = null;
    const fromPlex = e.hasFile && e.plexUrl && !e.bundle;
    const slot = el("div", { class: "detail-files" }, ...(fromPlex ? [] : files(e)));
    const article = el("article", { class: `detail is-${e.kind}` },
      el("div", { class: "detail-top" },
        poster(e, "poster-lg", 500),
        el("div", { class: "detail-head" },
          el("p", { class: "detail-kind", text: e.kind === "movie" ? t("cal.movies") : e.network || t("cal.episodes") }),
          el("h2", { id: "detail-title", text: e.title }),
          e.kind === "episode"
            ? el("p", { class: "detail-sub" }, el("strong", { text: code(e) }), e.epTitle ? ` · ${e.epTitle}` : "")
            : e.year ? el("p", { class: "detail-sub", text: String(e.year) }) : null,
          el("p", { class: "detail-tags" },
            ...tagsOf(e).map((x) => el("span", { class: "pill", text: x })),
            el("span", { class: `status status-${statusOf(e)}`, text: t("cal.status_" + statusOf(e)) })))),
      el("dl", { class: "detail-facts" },
        el("dt", { text: t("cal.detail_date") }), el("dd", { text: when }),
        e.runtime ? [el("dt", { text: t("cal.detail_duration") }), el("dd", { text: t("cal.detail_runtime", { n: e.runtime }) })] : null,
        e.requestedBy?.length ? [el("dt", { text: t("cal.requested_by") }), el("dd", { text: e.requestedBy.join(", ") })] : null,
        el("dt", { text: t("cal.detail_sources") }), el("dd", { text: e.instances.join(", ") })),
      e.bundle ? null : slot,
      links(e),
      e.bundle
        ? el("ol", { class: "detail-episodes" }, e.bundle.map((x) => el("li", {},
          el("strong", { text: t("cal.episode_code", { s: pad(x.season), e: pad(x.episode) }) }),
          x.epTitle ? ` · ${x.epTitle}` : "",
          el("span", { class: `status status-${statusOf(x)}`, text: t("cal.status_" + statusOf(x)) }))))
        : e.overview ? el("p", { class: "detail-overview", text: e.overview }) : null);
    show(article);
    if (fromPlex) loadFile(e, article, slot);
  }

  function openDay(day, list) {
    show(el("article", { class: "detail day-sheet" },
      el("h2", { id: "detail-title", text: fDayFull.format(day) }),
      el("ul", { class: "agenda-list" }, list.filter(visible).map((e) => el("li", {}, card(e, "lg"))))));
  }

  // Re-render when crossing the phone/desktop breakpoint (month cells change shape).
  wide.addEventListener("change", () => render({ keepScroll: true }));
  fetch("/api/prefs").then((r) => (r.ok ? r.json() : null)).then((p) => {
    if (!p) return;
    const before = JSON.stringify([state.mine, state.filters]);
    state.mine = p.mine || [];
    state.filters = normFilters(Object.assign(state.filters, p.filters || {}));
    if (typeof p.shortcuts === "boolean") state.shortcuts = p.shortcuts;
    if (Array.isArray(p.sites)) { state.sites = p.sites; store.set("sites", state.sites); }
    if (p.formats === "text" || p.formats === "logos") { state.formats = p.formats; store.set("formats", state.formats); }
    store.set("filters", state.filters);
    store.set("mine", state.mine);
    syncControls();
    if (JSON.stringify([state.mine, state.filters]) !== before) render({ keepScroll: true });
  }).catch(() => {});
  render();
})();
