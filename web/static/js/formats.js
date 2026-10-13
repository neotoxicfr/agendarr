// Agendarr: how formats, languages and logos are drawn, shared by the
// calendar's detail sheet and the notifications. The person picks logos or
// text in Settings; the choice is kept on the device too.
"use strict";
window.AgFormats = (() => {
  const lang = document.documentElement.lang || "fr";
  let mode = "logos";
  try { mode = JSON.parse(localStorage.getItem("agendarr.formats")) || "logos"; } catch { /* private mode */ }
  const el = (tag, attrs = {}, ...children) => {
    const n = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs)) {
      if (v === false || v == null) continue;
      if (k === "class") n.className = v;
      else if (k === "text") n.textContent = v;
      else n.setAttribute(k, v === true ? "" : v);
    }
    for (const c of children.flat()) if (c != null && c !== false) n.append(c);
    return n;
  };
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
  // Formats read as badges: "4K", "WEBRip", "Dolby Vision", "HEVC", "Atmos".
  // In "logos" mode, Dolby, DTS and HDR formats show their logo; each one's
  // file and proportions live in app.css (.logo-*), the CSP forbids inline styles.
  const LOGO = { DV: "dolby-vision", HDR10: "hdr10", "HDR10+": "hdr10plus", Atmos: "dolby-atmos", "E-AC3": "dolby-digital-plus",
    AC3: "dolby-digital", TrueHD: "dolby-truehd", DTS: "dts", "DTS-HD MA": "dts-hd-ma", "4K": "ultra-hd", "720p": "hd",
    AV1: "av1", FLAC: "flac", Opus: "opus", AAC: "aac" };
  // Formats no logo exists for (HEVC, H.264, Full HD, 10-bit, channels,
  // subtitle formats) read as a bold wordmark next to the logos.
  const MARK = { "1440p": "QHD", "1080p": "FULL HD", "H.264": "AVC" };
  const badge = (x) => {
    if (mode === "text") return el("span", { class: "fmt", text: x, title: FULL[x] });
    const logo = LOGO[x];
    if (!logo) return el("span", { class: "fmt-mark", text: MARK[x] || x, title: FULL[x] || (MARK[x] ? x : null) });
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
  return { setMode(m) { mode = m === "text" ? "text" : "logos"; }, langName, flagOf, langBadge, langList, codecName, badge, badges, hdrNames, videoName, audioBadges };
})();
