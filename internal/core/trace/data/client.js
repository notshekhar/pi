(function () {
  "use strict";
  var model = JSON.parse(document.getElementById("trace").textContent);
  var app = document.getElementById("app");

  /* ------------------------------------------------------------------ dom */

  function h(tag, attrs, children) {
    var el = document.createElement(tag);
    if (attrs) for (var k in attrs) {
      if (attrs[k] === undefined || attrs[k] === null || attrs[k] === false) continue;
      if (k === "class") el.className = attrs[k];
      else if (k === "text") el.textContent = attrs[k];
      else if (k === "style") el.style.cssText = attrs[k];
      else if (k.slice(0, 2) === "on") el.addEventListener(k.slice(2), attrs[k]);
      else el.setAttribute(k, attrs[k]);
    }
    if (children) for (var i = 0; i < children.length; i++) {
      var c = children[i];
      if (c === null || c === undefined || c === false) continue;
      el.appendChild(typeof c === "string" ? document.createTextNode(c) : c);
    }
    return el;
  }
  function qs(sel, root) { return (root || document).querySelector(sel); }

  /* Prose in the ledger — prompts, replies, reasoning, subagent briefs — is
   * markdown, because that is what was written and what the terminal showed.
   * __mdRender builds it with createTextNode only, so untrusted model output
   * can never become markup. */
  function md(cls, text) {
    var el = h("div", { class: cls + " md" });
    el.appendChild(window.__mdRender(text));
    return el;
  }
  function each(list, fn) { Array.prototype.forEach.call(list, fn); }
  function clamp(v, lo, hi) { return v < lo ? lo : v > hi ? hi : v; }

  /* --------------------------------------------------------------- format */

  function fmtMs(ms) {
    if (!isFinite(ms)) return "—";
    if (ms < 1000) return Math.round(ms) + " ms";
    if (ms < 60000) return (ms / 1000).toFixed(ms < 10000 ? 1 : 0) + " s";
    var m = Math.floor(ms / 60000), s = Math.round((ms % 60000) / 1000);
    if (m < 60) return s ? m + " min " + s + " s" : m + " min";
    var hr = Math.floor(m / 60);
    return (m % 60) ? hr + " h " + (m % 60) + " min" : hr + " h";
  }
  function fmtTok(n) {
    if (n < 1000) return String(n);
    if (n < 100000) return (n / 1000).toFixed(1).replace(/\.0$/, "") + "k";
    if (n < 999500) return Math.round(n / 1000) + "k";
    var units = [["M", 1e6], ["B", 1e9], ["T", 1e12]];
    for (var i = 0; i < units.length; i++) {
      var v = n / units[i][1];
      if (v < 999.5 || i === units.length - 1) return v.toFixed(2).replace(/\.?0+$/, "") + units[i][0];
    }
  }
  function fmtUsd(u) {
    if (u === undefined || u === null) return "—";
    if (u === 0) return "$0";
    if (u < 0.000001) return "<$0.000001";
    if (u < 0.01) return "$" + u.toPrecision(2);
    if (u < 1) return "$" + u.toFixed(3);
    return "$" + u.toFixed(2);
  }
  function clock(ts) {
    return new Date(ts).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
  }
  function clockMs(ts) {
    var d = new Date(ts), ms = String(d.getMilliseconds());
    while (ms.length < 3) ms = "0" + ms;
    return clock(ts) + "." + ms;
  }
  function shortModel(id) { return id ? id.split("/").pop() : ""; }
  function oneLine(s) { return (s || "").replace(/\s+/g, " ").trim(); }
  function plural(n, one) { return n + " " + one + (n === 1 ? "" : "s"); }
  function usageText(u) {
    if (!u) return null;
    var parts = [fmtTok(u.input) + " in"];
    if (u.cacheRead) parts.push(fmtTok(u.cacheRead) + " cached");
    if (u.cacheWrite) parts.push(fmtTok(u.cacheWrite) + " cache write");
    parts.push(fmtTok(u.output) + " out");
    if (u.reasoning) parts.push(fmtTok(u.reasoning) + " reasoning");
    return parts.join(" · ") + " · " + (u.estimated ? "~" : "") + fmtUsd(u.usd);
  }

  /* ------------------------------------------------------- record ledger  */
  /* One flat, ordered list of everything that happened, with where it lives
   * in the page and how honest its clock is. Every view below — the overview
   * bars, the search, the brush, the stats — is a projection of this list,
   * so a record can only ever be drawn one way. */

  var KIND_LABEL = { user: "prompt", model: "model", tool: "tool", event: "event" };
  var records = [];

  function rec(r) { r.idx = records.length; records.push(r); return r; }

  function stepSearch(s) {
    return ((s.text || "") + " " + (s.reasoning || "") + " " + (s.model || "")).toLowerCase();
  }
  function toolSearch(t) {
    return ((t.name || "") + " " + (t.input || "") + " " + (t.output || "") +
            (t.subagent ? " " + t.subagent.agent + " " + t.subagent.prompt : "")).toLowerCase();
  }

  model.turns.forEach(function (turn) {
    rec({
      kind: "user", lane: 0, turn: turn.index, t0: turn.user.ts, t1: turn.user.ts,
      prov: "recorded", label: "turn " + turn.index, sub: oneLine(turn.user.text) || "(no prompt)",
      search: (turn.user.text || "").toLowerCase(), anchor: "turn-" + turn.index, turnHead: true
    });
    turn.events.forEach(function (e) {
      rec({
        kind: "event", lane: 0, turn: turn.index, t0: e.ts, t1: e.ts, prov: "recorded",
        label: e.kind === "compact" ? "compaction" : e.kind === "model-change" ? "model change" : "branch summary",
        sub: e.label, search: e.label.toLowerCase(), anchor: "turn-" + turn.index, event: e.kind
      });
    });
    turn.steps.forEach(function (s) {
      var t = s.timing, r;
      if (t.kind === "recorded") {
        r = rec({
          kind: "model", lane: 1, t0: t.startedAt, t1: Math.max(t.startedAt, t.modelEndedAt),
          prov: "recorded", ttft: t.firstTokenAt, retryMs: t.retryWaitMs, stepEnd: t.endedAt
        });
      } else if (t.kind === "derived") {
        r = rec({ kind: "model", lane: 1, t0: t.startedAt, t1: Math.max(t.startedAt, t.endedAt), prov: "derived" });
      } else {
        r = rec({ kind: "model", lane: 1, t0: t.endedAt, t1: t.endedAt, prov: "none" });
      }
      r.turn = turn.index; r.step = s.index;
      r.label = "step " + s.index;
      r.sub = oneLine(s.text) || (s.reasoning ? oneLine(s.reasoning) : "");
      r.search = stepSearch(s);
      r.anchor = "step-" + turn.index + "-" + s.index;
      r.model = s.model;
      r.usage = s.usage;
      r.interrupted = s.interrupted;

      s.tools.forEach(function (tool) {
        var st, en, prov = "recorded";
        if (tool.timing) {
          st = tool.timing.startedAt;
          en = tool.timing.endedAt === undefined
            ? (t.kind === "recorded" ? Math.max(st, t.endedAt) : st)
            : tool.timing.endedAt;
          if (tool.timing.endedAt === undefined) prov = "open";
        } else if (t.kind === "recorded") {
          // The call is real, its clock is not: place it in the step's tool
          // window and say so rather than inventing a bar.
          st = t.modelEndedAt; en = Math.max(st, t.endedAt); prov = "derived";
        } else {
          st = t.endedAt; en = t.endedAt; prov = "none";
        }
        rec({
          kind: "tool", lane: 2, turn: turn.index, step: s.index, toolCallId: tool.toolCallId,
          t0: st, t1: Math.max(st, en), prov: prov, error: tool.error === true,
          label: tool.name, sub: tool.input || (tool.subagent ? oneLine(tool.subagent.prompt) : ""),
          search: toolSearch(tool), anchor: "step-" + turn.index + "-" + s.index,
          toolAnchor: "tool-" + turn.index + "-" + tool.toolCallId,
          subagent: tool.subagent, usage: tool.subagent ? tool.subagent.usage : undefined
        });
      });
    });
  });

  var timed = records.filter(function (r) { return r.prov !== "none"; });
  var hasTiming = timed.length > 0;

  /* ------------------------------------------------------------ projection */
  /* Four horizontal projections of the same records. Time is not the only
   * useful axis: a session that waited 20 minutes for a human is unreadable
   * on a wall clock, and a session of 300 fast steps is unreadable on
   * anything else. */

  var MODES = [
    ["actual", "wall clock", "real time, idle included"],
    ["compressed", "no idle", "real durations, gaps between records removed"],
    ["duration", "durations", "records packed end to end, length kept"],
    ["sequence", "sequence", "one slot per record, length ignored"]
  ];

  var domain = { start: 0, end: 1 };
  var gaps = [];

  function project(mode) {
    gaps = [];
    var order = timed.slice().sort(function (a, b) { return a.t0 - b.t0 || a.idx - b.idx; });
    if (mode === "sequence") {
      order.forEach(function (r, i) { r.x0 = i; r.x1 = i + 1; });
      domain = { start: 0, end: Math.max(1, order.length) };
    } else if (mode === "duration") {
      var at = 0;
      order.forEach(function (r) { r.x0 = at; r.x1 = at + (r.t1 - r.t0); at = r.x1; });
      domain = { start: 0, end: Math.max(1, at) };
    } else {
      var removed = 0, covered = null;
      order.forEach(function (r) {
        if (mode === "compressed" && covered !== null && r.t0 > covered) {
          var gap = r.t0 - covered;
          removed += gap;
          gaps.push({ x: covered - (removed - gap), ms: gap });
        }
        r.x0 = r.t0 - removed;
        r.x1 = Math.max(r.x0, r.t1 - removed);
        covered = covered === null ? r.t1 : Math.max(covered, r.t1);
      });
      if (mode === "actual") {
        var cov = null;
        order.forEach(function (r) {
          if (cov !== null && r.t0 > cov + 2000) gaps.push({ x: cov, x2: r.t0, ms: r.t0 - cov });
          cov = cov === null ? r.t1 : Math.max(cov, r.t1);
        });
      }
      var lo = Infinity, hi = -Infinity;
      order.forEach(function (r) { if (r.x0 < lo) lo = r.x0; if (r.x1 > hi) hi = r.x1; });
      domain = { start: lo, end: Math.max(lo + 1, hi) };
    }
    records.forEach(function (r) { if (r.prov === "none") { r.x0 = null; r.x1 = null; } });
    stackTools();
  }

  /* Overlapping tool calls get their own row so a parallel fan-out reads as
   * one, not as a smear. */
  var toolRows = 1;
  function stackTools() {
    var rowEnds = [];
    timed.filter(function (r) { return r.kind === "tool"; })
      .sort(function (a, b) { return a.x0 - b.x0; })
      .forEach(function (r) {
        var row = 0;
        while (row < rowEnds.length && rowEnds[row] > r.x0 + 1e-9) row++;
        rowEnds[row] = r.x1;
        r.row = row;
      });
    toolRows = Math.max(1, Math.min(6, rowEnds.length));
  }

  function unit(v) {
    if (state.mode === "sequence") return "#" + Math.round(v);
    return fmtMs(v - domain.start);
  }

  /* -------------------------------------------------------------- state  */

  var state = {
    mode: "compressed",
    viewport: null,   /* {start,end} in domain units; null = whole domain */
    focus: null,      /* brushed interval, filters the ledger */
    query: "",
    tool: "",
    errorsOnly: false,
    minMs: 0,
    selected: null,
    matches: [],
    matchAt: -1
  };

  function viewStart() { return state.viewport ? state.viewport.start : domain.start; }
  function viewSpan() {
    return state.viewport ? Math.max(1e-6, state.viewport.end - state.viewport.start)
                          : Math.max(1e-6, domain.end - domain.start);
  }
  function fullSpan() { return Math.max(1e-6, domain.end - domain.start); }
  function setViewport(v) {
    if (v && v.end - v.start >= fullSpan() * 0.999) v = null;
    if (v) {
      var span = Math.min(fullSpan(), Math.max(minSpan(), v.end - v.start));
      var start = clamp(v.start, domain.start, domain.end - span);
      v = { start: start, end: start + span };
    }
    state.viewport = v;
    layoutViewport();
  }
  function minSpan() {
    return state.mode === "sequence" ? 4 : Math.max(20, fullSpan() / 4000);
  }

  /* ------------------------------------------------------------ overview  */
  /* A Chrome-network-style overview of the whole session: three lanes
   * (prompts, model, tools) over one time axis. The bars are laid out ONCE
   * in full-domain percentages inside a single element; zooming and panning
   * only move and stretch that element, so a 5,000-bar session zooms without
   * touching the DOM. */

  var ov = {};

  function buildOverview() {
    var lanes = h("div", { class: "ov-lanes" }, [
      h("div", { class: "ov-lane-label", text: "prompts" }),
      h("div", { class: "ov-lane-label", text: "model" }),
      h("div", { class: "ov-lane-label", text: "tools" })
    ]);
    ov.ruler = h("div", { class: "ov-ruler" });
    ov.dom = h("div", { class: "ov-domain" });
    ov.brush = h("div", { class: "ov-brush", hidden: "hidden" });
    ov.cursor = h("div", { class: "ov-cursor", hidden: "hidden" });
    ov.track = h("div", { class: "ov-track" }, [ov.dom, ov.brush, ov.cursor]);
    ov.map = h("canvas", { class: "ov-map-canvas" });
    ov.mapWindow = h("div", { class: "ov-map-window", hidden: "hidden" });
    ov.mapWrap = h("div", { class: "ov-map" }, [ov.map, ov.mapWindow]);
    ov.badge = h("div", { class: "ov-badge" });

    var modeBox = h("div", { class: "seg" }, MODES.map(function (m) {
      return h("button", {
        class: "seg-b" + (m[0] === state.mode ? " on" : ""), type: "button", "data-mode": m[0],
        "data-tip": m[1] + "\n" + m[2], text: m[1],
        onclick: function () { setMode(m[0]); }
      });
    }));
    ov.modeBox = modeBox;

    var head = h("div", { class: "ov-head" }, [
      h("h2", { text: "overview" }),
      modeBox,
      ov.badge,
      h("button", { class: "btn", type: "button", text: "reset zoom", onclick: function () { setViewport(null); clearFocus(); } })
    ]);

    ov.root = h("section", { class: "ov" }, [
      head,
      h("div", { class: "ov-plot" }, [lanes, h("div", { class: "ov-track-wrap" }, [ov.ruler, ov.track])]),
      ov.mapWrap,
      h("div", { class: "ov-foot" }, [
        legend(),
        h("p", { class: "ov-hint", text: "drag to focus a slice · wheel to zoom · right-drag to pan · right-click or esc clears · click a bar to open it · ? for keys" })
      ])
    ]);
    return ov.root;
  }

  function legend() {
    var items = [
      ["l-model", "model, streaming"], ["l-wait", "before the first token"], ["l-tool", "tool running"],
      ["l-derived", "wall time only"], ["l-retry", "retry wait"], ["l-error", "error"]
    ];
    return h("div", { class: "legend" }, items.map(function (it) {
      return h("span", { class: it[0] }, [h("i"), it[1]]);
    }));
  }

  function barsFor(r) {
    /* position in FULL-domain percentages; zoom moves the parent */
    var span = fullSpan();
    var left = ((r.x0 - domain.start) / span) * 100;
    var width = Math.max(0, ((r.x1 - r.x0) / span) * 100);
    return { left: left, width: width };
  }

  function renderBars() {
    ov.dom.textContent = "";
    var span = fullSpan();
    var toolH = 6, toolGap = 1;
    ov.track.style.height = (28 + toolRows * (toolH + toolGap) + 8) + "px";

    /* idle bands (wall-clock mode) and compressed-gap seams */
    gaps.forEach(function (g) {
      if (g.x2 !== undefined) {
        var l = ((g.x - domain.start) / span) * 100, w = ((g.x2 - g.x) / span) * 100;
        ov.dom.appendChild(h("span", {
          class: "ov-idle", style: "left:" + l + "%;width:" + w + "%",
          "data-tip": "idle · " + fmtMs(g.ms)
        }));
      } else {
        var lx = ((g.x - domain.start) / span) * 100;
        ov.dom.appendChild(h("span", {
          class: "ov-seam", style: "left:" + lx + "%", "data-tip": fmtMs(g.ms) + " of idle removed"
        }));
      }
    });

    records.forEach(function (r) {
      if (r.x0 === null) return;
      var p = barsFor(r), el;
      if (r.kind === "user") {
        el = h("span", { class: "ov-b user", style: "left:" + p.left + "%" });
      } else if (r.kind === "event") {
        el = h("span", { class: "ov-b ev " + r.event, style: "left:" + p.left + "%" });
      } else if (r.kind === "model") {
        el = h("span", {
          class: "ov-b model" + (r.prov === "derived" ? " derived" : "") + (r.interrupted ? " cut" : ""),
          style: "left:" + p.left + "%;width:" + p.width + "%"
        });
        if (r.ttft !== undefined && r.prov === "recorded") {
          el.appendChild(h("i", { class: "ov-ttft", style: "left:" + clamp(((r.ttft - r.t0) / Math.max(1, r.t1 - r.t0)) * 100, 0, 100) + "%" }));
        }
      } else {
        el = h("span", {
          class: "ov-b tool" + (r.error ? " err" : "") + (r.prov === "derived" ? " derived" : "") + (r.prov === "open" ? " open" : ""),
          style: "left:" + p.left + "%;width:" + p.width + "%;top:" + (28 + (r.row || 0) * (toolH + toolGap)) + "px"
        });
      }
      el.setAttribute("data-rec", String(r.idx));
      ov.dom.appendChild(el);
      r.el = el;
    });

    /* turn boundaries, drawn over everything */
    records.filter(function (r) { return r.turnHead && r.x0 !== null; }).forEach(function (r) {
      var l = ((r.x0 - domain.start) / span) * 100;
      ov.dom.appendChild(h("span", { class: "ov-turnline", style: "left:" + l + "%", "data-rec": String(r.idx) },
        [h("i", { text: String(r.turn) })]));
    });

    layoutViewport();
    drawMap();
  }

  function layoutViewport() {
    var span = viewSpan(), start = viewStart();
    ov.dom.style.left = (-(start - domain.start) / span) * 100 + "%";
    ov.dom.style.width = (fullSpan() / span) * 100 + "%";
    renderRuler();
    renderBrush();
    drawMapWindow();
  }

  function niceTick(span, targetTicks, isMs) {
    var raw = span / targetTicks;
    if (!isMs && state.mode === "sequence") {
      var seq = [1, 2, 5, 10, 25, 50, 100, 250, 500, 1000];
      for (var j = 0; j < seq.length; j++) if (seq[j] >= raw) return seq[j];
      return seq[seq.length - 1];
    }
    var c = [10, 25, 50, 100, 250, 500, 1000, 2000, 5000, 10000, 15000, 30000, 60000,
             120000, 300000, 600000, 1800000, 3600000, 7200000];
    for (var i = 0; i < c.length; i++) if (c[i] >= raw) return c[i];
    return c[c.length - 1];
  }

  function renderRuler() {
    var span = viewSpan(), start = viewStart();
    var width = ov.track.clientWidth || 900;
    var tick = niceTick(span, Math.max(4, Math.round(width / 110)));
    ov.ruler.textContent = "";
    var first = Math.ceil(start / tick) * tick;
    for (var x = first; x <= start + span; x += tick) {
      var left = ((x - start) / span) * 100;
      if (left > 97) continue;
      ov.ruler.appendChild(h("span", {
        class: "ov-tick", style: "left:" + left + "%",
        text: unit(x)
      }));
    }
    /* the track's grid IS the ruler's ticks, carried down over the bars */
    ov.track.style.backgroundSize = ((tick / span) * 100) + "% 100%";
    ov.track.style.backgroundPosition = ((((first - tick) - start) / span) * 100) + "% 0";
  }

  function renderBrush() {
    if (!state.focus) { ov.brush.hidden = true; return; }
    var span = viewSpan(), start = viewStart();
    var l = ((state.focus.start - start) / span) * 100;
    var w = ((state.focus.end - state.focus.start) / span) * 100;
    ov.brush.hidden = false;
    ov.brush.style.left = l + "%";
    ov.brush.style.width = Math.max(0.2, w) + "%";
  }

  /* the minimap: the whole session at a glance, always unzoomed */
  function drawMap() {
    var c = ov.map, wrap = ov.mapWrap;
    var w = wrap.clientWidth || 900, hgt = 30;
    var dpr = window.devicePixelRatio || 1;
    c.width = Math.round(w * dpr); c.height = Math.round(hgt * dpr);
    c.style.width = w + "px"; c.style.height = hgt + "px";
    var g = c.getContext("2d");
    if (!g) return;
    g.setTransform(dpr, 0, 0, dpr, 0, 0);
    g.clearRect(0, 0, w, hgt);
    var span = fullSpan();
    var css = getComputedStyle(document.documentElement);
    var colModel = css.getPropertyValue("--model").trim() || "#2F5BEA";
    var colTool = css.getPropertyValue("--tool").trim() || "#E09A1E";
    var colErr = css.getPropertyValue("--error").trim() || "#D6453D";
    var colUser = css.getPropertyValue("--muted").trim() || "#6E7987";
    timed.forEach(function (r) {
      var x = ((r.x0 - domain.start) / span) * w;
      var bw = Math.max(1, ((r.x1 - r.x0) / span) * w);
      if (r.kind === "model") { g.fillStyle = colModel; g.fillRect(x, 4, bw, 9); }
      else if (r.kind === "tool") { g.fillStyle = r.error ? colErr : colTool; g.fillRect(x, 15, bw, 9); }
      else { g.fillStyle = colUser; g.fillRect(x, 1, 1, hgt - 2); }
    });
  }

  function drawMapWindow() {
    if (!state.viewport) { ov.mapWindow.hidden = true; return; }
    var span = fullSpan();
    ov.mapWindow.hidden = false;
    ov.mapWindow.style.left = ((viewStart() - domain.start) / span) * 100 + "%";
    ov.mapWindow.style.width = (viewSpan() / span) * 100 + "%";
  }

  /* ---- overview interaction ---- */

  function timeAt(ev, el) {
    var rect = (el || ov.track).getBoundingClientRect();
    var f = clamp((ev.clientX - rect.left) / Math.max(1, rect.width), 0, 1);
    return viewStart() + f * viewSpan();
  }
  function recordAt(ev) {
    var t = ev.target;
    var el = t && t.closest ? t.closest("[data-rec]") : null;
    if (!el) return null;
    var i = Number(el.getAttribute("data-rec"));
    return isFinite(i) ? records[i] : null;
  }

  function tipHtmlFor(r) {
    var rows = [];
    rows.push(h("b", { text: (KIND_LABEL[r.kind] || r.kind) + (r.label ? " · " + r.label : "") }));
    if (r.kind === "tool" || r.kind === "model") {
      if (r.prov === "none") rows.push(h("span", { class: "warn", text: "timing not recorded" }));
      else if (r.prov === "derived") rows.push(h("span", { class: "warn", text: "wall time only — not measured" }));
      else if (r.prov === "open") rows.push(h("span", { class: "warn", text: "did not return" }));
    }
    if (r.prov !== "none") {
      rows.push(h("span", { text: clockMs(r.t0) + (r.t1 > r.t0 ? " → " + clockMs(r.t1) : "") }));
      if (r.t1 > r.t0) rows.push(h("span", { text: fmtMs(r.t1 - r.t0) }));
    }
    if (r.kind === "model" && r.ttft !== undefined) {
      rows.push(h("span", { text: "first token " + fmtMs(r.ttft - r.t0) + " · decode " + fmtMs(Math.max(0, r.t1 - r.ttft)) }));
    }
    if (r.retryMs) rows.push(h("span", { class: "warn", text: "after " + fmtMs(r.retryMs) + " retry wait" }));
    if (r.usage) rows.push(h("span", { text: usageText(r.usage) }));
    if (r.sub) rows.push(h("span", { class: "dim", text: r.sub.slice(0, 160) }));
    return rows;
  }

  /* ------------------------------------------------------------ tooltip */
  /* One bubble, on <body> and position:fixed, so it is never clipped by an
   * ancestor's overflow and never has to know where in the page the thing it
   * describes lives. It is placed in viewport coordinates and then fitted:
   * slide back inside horizontally, flip above the cursor rather than cover
   * it vertically. width:max-content in the stylesheet is load-bearing —
   * a fixed box shrink-to-fits against the distance from its left edge to the
   * viewport edge, so without it a bubble near the right edge measures narrow
   * and wraps before the clamp ever runs. */

  var TIP_GAP = 14, TIP_EDGE = 10;
  var tip = h("div", { class: "tip", role: "tooltip", hidden: "hidden" });
  document.body.appendChild(tip);
  var tipTimer = null, tipAt = null;

  /** Place the bubble near (x, y) in viewport coordinates, inside the viewport. */
  function placeTip(x, y) {
    tip.style.left = "0px";
    tip.style.top = "0px";
    var r = tip.getBoundingClientRect();
    var vw = document.documentElement.clientWidth;
    var vh = document.documentElement.clientHeight;
    var left = clamp(x + TIP_GAP, TIP_EDGE, Math.max(TIP_EDGE, vw - r.width - TIP_EDGE));
    var below = y + TIP_GAP + 4;
    var top = below + r.height <= vh - TIP_EDGE ? below : y - TIP_GAP - r.height;
    top = clamp(top, TIP_EDGE, Math.max(TIP_EDGE, vh - r.height - TIP_EDGE));
    tip.style.left = left + "px";
    tip.style.top = top + "px";
  }

  /** Content for a bubble: a record, or the data-tip text on the element
   * under the pointer (newline-separated lines, first line the heading). */
  function tipContent(ev, r) {
    if (r) return tipHtmlFor(r);
    var el = ev.target && ev.target.closest ? ev.target.closest("[data-tip]") : null;
    if (!el) return null;
    var lines = el.getAttribute("data-tip").split("\n");
    return lines.map(function (line, i) {
      return h(i === 0 ? "b" : "span", { class: i === 0 ? null : "dim", text: line });
    });
  }

  /* The bubble is rebuilt only when what it describes changes; the same
   * source under a moving pointer is a reposition, not a repaint. */
  function tipKeyFor(ev, r) {
    if (r) return "r" + r.idx;
    var el = ev.target && ev.target.closest ? ev.target.closest("[data-tip]") : null;
    return el ? "d" + el.getAttribute("data-tip") : null;
  }

  function showTip(ev, r) {
    var key = tipKeyFor(ev, r);
    if (key === null) { hideTip(); return; }
    if (!tip.hidden && key === tipAt) { placeTip(ev.clientX, ev.clientY); return; }
    var rows = tipContent(ev, r);
    if (!rows || !rows.length) { hideTip(); return; }
    var x = ev.clientX, y = ev.clientY;
    if (tipTimer) clearTimeout(tipTimer);
    var paint = function () {
      tipTimer = null;
      tip.textContent = "";
      rows.forEach(function (n) { tip.appendChild(n); });
      tip.hidden = false;
      tipAt = key;
      placeTip(x, y);
    };
    /* once a bubble is up, moving to the next one should not re-wait */
    if (!tip.hidden) paint();
    else tipTimer = setTimeout(paint, 90);
  }

  function hideTip() {
    if (tipTimer) clearTimeout(tipTimer);
    tipTimer = null;
    tipAt = null;
    tip.hidden = true;
  }

  /* Scrolling or leaving the window moves everything under the bubble. */
  window.addEventListener("scroll", hideTip, true);
  window.addEventListener("blur", hideTip);

  /* Every other hover target in the page just carries data-tip. */
  document.addEventListener("pointerover", function (ev) {
    var el = ev.target && ev.target.closest ? ev.target.closest("[data-tip]") : null;
    if (!el) { if (!ev.target || !ev.target.closest || !ev.target.closest(".ov-track, .ts-track")) hideTip(); return; }
    showTip(ev, null);
  });
  document.addEventListener("pointermove", function (ev) {
    if (!tip.hidden && ev.target && ev.target.closest && ev.target.closest("[data-tip]")) showTip(ev, null);
  });

  function wireOverview() {
    var drag = null, pan = null;

    ov.track.addEventListener("wheel", function (ev) {
      ev.preventDefault();
      var rect = ov.track.getBoundingClientRect();
      var f = clamp((ev.clientX - rect.left) / Math.max(1, rect.width), 0, 1);
      if (ev.shiftKey) {
        setViewport({ start: viewStart() + ev.deltaY * 0.001 * viewSpan(), end: viewStart() + viewSpan() + ev.deltaY * 0.001 * viewSpan() });
        return;
      }
      var next = clamp(viewSpan() * Math.exp(ev.deltaY * 0.0015), minSpan(), fullSpan());
      var anchor = viewStart() + f * viewSpan();
      setViewport({ start: anchor - f * next, end: anchor - f * next + next });
    }, { passive: false });

    ov.track.addEventListener("contextmenu", function (ev) { ev.preventDefault(); });

    ov.track.addEventListener("pointerdown", function (ev) {
      if (ev.button === 2 || ev.button === 1) {
        pan = { x: ev.clientX, start: viewStart(), moved: false, id: ev.pointerId };
        try { ov.track.setPointerCapture(ev.pointerId); } catch (err) { /* synthetic pointers have no capture */ }
        ov.track.classList.add("panning");
        return;
      }
      if (ev.button !== 0) return;
      drag = { anchor: timeAt(ev), id: ev.pointerId, moved: false, rec: recordAt(ev) };
      try { ov.track.setPointerCapture(ev.pointerId); } catch (err) { /* synthetic pointers have no capture */ }
    });

    ov.track.addEventListener("pointermove", function (ev) {
      var rect = ov.track.getBoundingClientRect();
      var f = clamp((ev.clientX - rect.left) / Math.max(1, rect.width), 0, 1);
      ov.cursor.hidden = false;
      ov.cursor.style.left = f * 100 + "%";
      ov.cursor.setAttribute("data-t", unit(viewStart() + f * viewSpan()));

      if (pan) {
        var d = (ev.clientX - pan.x) / Math.max(1, rect.width);
        if (Math.abs(ev.clientX - pan.x) > 3) pan.moved = true;
        if (state.viewport) setViewport({ start: pan.start - d * viewSpan(), end: pan.start - d * viewSpan() + viewSpan() });
        return;
      }
      if (drag) {
        if (Math.abs(timeAt(ev) - drag.anchor) > viewSpan() * 0.002) drag.moved = true;
        if (drag.moved) {
          var t = timeAt(ev);
          state.focus = { start: Math.min(drag.anchor, t), end: Math.max(drag.anchor, t) };
          renderBrush();
          applyFilters();
        }
        return;
      }
      showTip(ev, recordAt(ev));
    });

    var end = function (ev) {
      if (pan) {
        ov.track.classList.remove("panning");
        if (!pan.moved) { clearFocus(); }
        pan = null;
        return;
      }
      if (!drag) return;
      if (!drag.moved) {
        if (drag.rec) selectRecord(drag.rec.idx, true);
        else clearFocus();
      }
      drag = null;
    };
    ov.track.addEventListener("pointerup", end);
    ov.track.addEventListener("pointercancel", end);
    ov.track.addEventListener("pointerleave", function () { ov.cursor.hidden = true; hideTip(); });

    /* minimap: click or drag to move the zoom window */
    var mapDrag = false;
    var moveWindow = function (ev) {
      if (!state.viewport) return;
      var rect = ov.mapWrap.getBoundingClientRect();
      var f = clamp((ev.clientX - rect.left) / Math.max(1, rect.width), 0, 1);
      var center = domain.start + f * fullSpan();
      setViewport({ start: center - viewSpan() / 2, end: center + viewSpan() / 2 });
    };
    ov.mapWrap.addEventListener("pointerdown", function (ev) {
      mapDrag = true;
      try { ov.mapWrap.setPointerCapture(ev.pointerId); } catch (err) { /* as above */ }
      moveWindow(ev);
    });
    ov.mapWrap.addEventListener("pointermove", function (ev) { if (mapDrag) moveWindow(ev); });
    ov.mapWrap.addEventListener("pointerup", function () { mapDrag = false; });

    window.addEventListener("resize", function () { renderRuler(); drawMap(); });
  }

  function setMode(mode) {
    state.mode = mode;
    each(ov.modeBox.children, function (b) {
      b.className = "seg-b" + (b.getAttribute("data-mode") === mode ? " on" : "");
    });
    var keep = state.focus;
    project(mode);
    state.viewport = null;
    state.focus = keep && mode === "actual" ? keep : null;
    renderBars();
    applyFilters();
  }

  function clearFocus() {
    if (!state.focus) return;
    state.focus = null;
    renderBrush();
    applyFilters();
  }

  /* -------------------------------------------------------------- filters */
  /* One predicate, used by every surface: the ledger hides, the overview
   * dims, the counter counts. */

  function matchesQuery(r) {
    if (!state.query) return true;
    return r.search.indexOf(state.query) !== -1 ||
           (r.label || "").toLowerCase().indexOf(state.query) !== -1 ||
           (r.sub || "").toLowerCase().indexOf(state.query) !== -1;
  }
  function passes(r) {
    if (state.errorsOnly && !r.error) return false;
    if (state.tool && !(r.kind === "tool" && r.label === state.tool)) return false;
    if (state.minMs && (r.t1 - r.t0) < state.minMs) return false;
    if (state.focus && r.x0 !== null && !(r.x0 <= state.focus.end && r.x1 >= state.focus.start)) return false;
    if (state.focus && r.x0 === null) return false;
    return matchesQuery(r);
  }

  var visibleRecords = [];

  function applyFilters() {
    var filtering = !!(state.query || state.errorsOnly || state.tool || state.minMs || state.focus);
    visibleRecords = records.filter(passes);
    var keepStep = {}, keepTurn = {}, keepTool = {};
    visibleRecords.forEach(function (r) {
      keepTurn[r.turn] = true;
      if (r.step !== undefined) keepStep[r.turn + "-" + r.step] = true;
      if (r.toolAnchor) keepTool[r.toolAnchor] = true;
      if (r.el) r.el.classList.remove("dim");
    });
    records.forEach(function (r) {
      if (r.el && !passes(r)) r.el.classList.toggle("dim", filtering);
    });

    each(document.querySelectorAll(".turn"), function (sec) {
      var n = Number(sec.getAttribute("data-turn"));
      sec.classList.toggle("hidden", filtering && !keepTurn[n]);
    });
    each(document.querySelectorAll(".step"), function (card) {
      var key = card.getAttribute("data-turn") + "-" + card.getAttribute("data-step");
      card.classList.toggle("hidden", filtering && !keepStep[key]);
    });
    each(document.querySelectorAll(".tool-li"), function (li) {
      var a = li.getAttribute("data-tool-anchor");
      li.classList.toggle("hidden", filtering && !keepTool[a]);
    });

    /* the focus / result badge */
    ov.badge.textContent = "";
    if (filtering) {
      var shown = visibleRecords.length;
      var bits = [plural(shown, "record")];
      if (state.focus) bits.push(fmtMs(state.focus.end - state.focus.start) + " slice");
      if (state.query) bits.push('"' + state.query + '"');
      if (state.tool) bits.push(state.tool);
      if (state.errorsOnly) bits.push("errors");
      if (state.minMs) bits.push("≥ " + fmtMs(state.minMs));
      ov.badge.appendChild(h("span", { class: "badge", text: bits.join(" · ") }));
      ov.badge.appendChild(h("button", { class: "btn ghost", type: "button", text: "clear", onclick: resetFilters }));
    }
    state.matches = visibleRecords.slice();
    state.matchAt = -1;
    highlightQuery();
    updateCounts();
  }

  function resetFilters() {
    state.query = ""; state.tool = ""; state.errorsOnly = false; state.minMs = 0; state.focus = null;
    if (ui.search) ui.search.value = "";
    if (ui.toolSel) ui.toolSel.value = "";
    if (ui.errBtn) ui.errBtn.classList.remove("on");
    if (ui.slowSel) ui.slowSel.value = "0";
    renderBrush();
    applyFilters();
  }

  function updateCounts() {
    if (!ui.count) return;
    var turns = 0, steps = 0, tools = 0, errs = 0, usd = 0, ms = 0;
    visibleRecords.forEach(function (r) {
      if (r.kind === "user") turns++;
      if (r.kind === "model") { steps++; ms += r.t1 - r.t0; if (r.usage) usd += r.usage.usd || 0; }
      if (r.kind === "tool") { tools++; if (r.error) errs++; }
    });
    ui.count.textContent = plural(turns, "turn") + " · " + plural(steps, "step") + " · " +
      plural(tools, "tool call") + (errs ? " · " + errs + " failed" : "") +
      " · model " + fmtMs(ms) + " · " + fmtUsd(usd);
  }

  /* Inline query highlight, capped so a huge session stays responsive. The
   * prose is rendered markup now, so a match is wrapped inside the text node
   * that holds it and the whole element is restored from an HTML snapshot —
   * rewriting textContent would flatten the markdown away. */
  var HL_SEL = ".turn-q, .step .text, .tool .input, .tool .name, .reason, .sub-prompt";
  var marked = [];

  function unmark() {
    marked.forEach(function (el) { el.innerHTML = el.__snap; el.__snap = null; });
    marked = [];
  }

  function markInside(el, q) {
    var walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT, null);
    var hits = [], node;
    while ((node = walker.nextNode()) !== null) {
      if (node.nodeValue.toLowerCase().indexOf(q) !== -1) hits.push(node);
    }
    if (!hits.length) return false;
    hits.forEach(function (n) {
      var text = n.nodeValue, lower = text.toLowerCase();
      var frag = document.createDocumentFragment(), pos = 0;
      while (pos < text.length) {
        var k = lower.indexOf(q, pos);
        if (k === -1) { frag.appendChild(document.createTextNode(text.slice(pos))); break; }
        if (k > pos) frag.appendChild(document.createTextNode(text.slice(pos, k)));
        frag.appendChild(h("mark", { text: text.slice(k, k + q.length) }));
        pos = k + q.length;
      }
      n.parentNode.replaceChild(frag, n);
    });
    return true;
  }

  function highlightQuery() {
    unmark();
    if (!state.query || state.query.length < 2) return;
    var nodes = document.querySelectorAll(HL_SEL), n = 0;
    for (var i = 0; i < nodes.length && n < 300; i++) {
      var el = nodes[i];
      if (el.closest(".hidden")) continue;
      if (el.textContent.toLowerCase().indexOf(state.query) === -1) continue;
      var snap = el.innerHTML;
      if (!markInside(el, state.query)) continue;
      el.__snap = snap;
      marked.push(el);
      n++;
    }
  }

  /* ------------------------------------------------------------- toolbar  */

  var ui = {};

  function toolNames() {
    var seen = {}, out = [];
    records.forEach(function (r) { if (r.kind === "tool" && !seen[r.label]) { seen[r.label] = 1; out.push(r.label); } });
    return out.sort();
  }

  function buildToolbar() {
    ui.search = h("input", {
      class: "search", type: "search", placeholder: "search prompts, replies, tool i/o    /",
      oninput: function () { state.query = this.value.trim().toLowerCase(); applyFilters(); },
      onkeydown: function (e) {
        if (e.key === "Escape") { this.value = ""; state.query = ""; applyFilters(); this.blur(); }
        if (e.key === "Enter") { e.shiftKey ? stepMatch(-1) : stepMatch(1); }
      }
    });
    ui.toolSel = h("select", {
      class: "sel", onchange: function () { state.tool = this.value; applyFilters(); }
    }, [h("option", { value: "", text: "all tools" })].concat(toolNames().map(function (n) {
      return h("option", { value: n, text: n });
    })));
    ui.errBtn = h("button", {
      class: "btn", type: "button", text: "errors only",
      onclick: function () { state.errorsOnly = !state.errorsOnly; this.classList.toggle("on", state.errorsOnly); applyFilters(); }
    });
    ui.slowSel = h("select", {
      class: "sel", onchange: function () { state.minMs = Number(this.value); applyFilters(); }
    }, [["0", "any duration"], ["1000", "≥ 1 s"], ["5000", "≥ 5 s"], ["15000", "≥ 15 s"], ["60000", "≥ 1 min"]]
      .map(function (o) { return h("option", { value: o[0], text: o[1] }); }));
    ui.count = h("span", { class: "count" });

    /* Controls on one row, the readout on its own line under it: the count's
     * text changes length with every filter, and inline it would reflow the
     * whole (sticky) bar on each keystroke. */
    return h("div", { class: "tbar-wrap" }, [
      h("div", { class: "tbar" }, [
        ui.search,
        h("span", { class: "grp" }, [ui.toolSel, ui.slowSel, ui.errBtn]),
        h("span", { class: "spacer" }),
        h("span", { class: "grp" }, [
          h("button", { class: "btn ghost", type: "button", text: "expand all", onclick: function () { openAll(true); } }),
          h("button", { class: "btn ghost", type: "button", text: "collapse", onclick: function () { openAll(false); } }),
          h("button", { class: "btn ghost", type: "button", text: "?", "data-tip": "keyboard shortcuts", onclick: toggleHelp })
        ])
      ]),
      h("div", { class: "tbar-foot" }, [ui.count])
    ]);
  }

  function openAll(open) {
    each(document.querySelectorAll(".steps details"), function (d) { d.open = open; });
  }

  /* -------------------------------------------------------------- stats   */

  function quantile(sorted, q) {
    if (!sorted.length) return 0;
    var pos = (sorted.length - 1) * q, base = Math.floor(pos), rest = pos - base;
    return sorted[base + 1] !== undefined ? sorted[base] + rest * (sorted[base + 1] - sorted[base]) : sorted[base];
  }

  function buildStats() {
    /* where the time went, from recorded steps only */
    var wait = 0, streamMs = 0, toolMs = 0, idle = 0, covered = null;
    var iv = [];
    records.forEach(function (r) {
      if (r.prov !== "recorded" && r.prov !== "open") return;
      if (r.kind === "model") {
        if (r.ttft !== undefined) { wait += r.ttft - r.t0; streamMs += Math.max(0, r.t1 - r.ttft); }
        else streamMs += r.t1 - r.t0;
        iv.push([r.t0, r.t1]);
      } else if (r.kind === "tool") { toolMs += r.t1 - r.t0; iv.push([r.t0, r.t1]); }
    });
    iv.sort(function (a, b) { return a[0] - b[0]; });
    var busyStart = iv.length ? iv[0][0] : 0, busyEnd = busyStart;
    iv.forEach(function (p) {
      if (covered === null) { covered = p[1]; busyEnd = p[1]; return; }
      if (p[0] > covered) idle += p[0] - covered;
      covered = Math.max(covered, p[1]);
      busyEnd = Math.max(busyEnd, p[1]);
    });
    var total = Math.max(1, wait + streamMs + toolMs + idle);
    var segs = [
      ["bd-wait", "waiting for the first token", wait],
      ["bd-stream", "streaming", streamMs],
      ["bd-tools", "tools", toolMs],
      ["bd-idle", "idle (you, mostly)", idle]
    ].filter(function (s) { return s[2] > 0; });

    var breakdown = h("div", { class: "bd" }, [
      h("div", { class: "bd-bar" }, segs.map(function (s) {
        return h("span", {
          class: "bd-seg " + s[0], style: "flex-grow:" + s[2],
          "data-tip": fmtMs(s[2]) + " · " + Math.round((s[2] / total) * 100) + "%\n" + s[1]
        });
      })),
      h("ul", { class: "bd-key" }, segs.map(function (s) {
        return h("li", { class: s[0] }, [h("b", { text: fmtMs(s[2]) }), " " + s[1] + " · " + Math.round((s[2] / total) * 100) + "%"]);
      }))
    ]);

    /* per-tool aggregates */
    var by = {};
    records.forEach(function (r) {
      if (r.kind !== "tool") return;
      var b = by[r.label] || (by[r.label] = { name: r.label, n: 0, ms: [], total: 0, err: 0, untimed: 0 });
      b.n++;
      if (r.error) b.err++;
      if (r.prov === "recorded") { var d = r.t1 - r.t0; b.ms.push(d); b.total += d; }
      else b.untimed++;
    });
    var rows = Object.keys(by).map(function (k) { return by[k]; })
      .sort(function (a, b2) { return b2.total - a.total || b2.n - a.n; });
    var maxTotal = rows.length ? rows[0].total : 1;
    var table = h("table", { class: "agg" }, [
      h("thead", {}, [h("tr", {}, ["tool", "calls", "total", "median", "p95", "slowest", "errors"].map(function (c) {
        return h("th", { text: c, class: c === "tool" ? "l" : "" });
      }))]),
      h("tbody", {}, rows.map(function (b) {
        var s = b.ms.slice().sort(function (x, y) { return x - y; });
        return h("tr", {
          class: "agg-row" + (state.tool === b.name ? " on" : ""), tabindex: "0",
          "data-tip": b.name + "\n" + plural(b.n, "call") + (b.err ? " · " + b.err + " failed" : "") +
            "\nclick to filter the ledger to it",
          onclick: function () { state.tool = state.tool === b.name ? "" : b.name; ui.toolSel.value = state.tool; applyFilters(); refreshAggSelection(); }
        }, [
          h("td", { class: "l" }, [
            h("span", { class: "agg-name", text: b.name }),
            h("span", { class: "agg-track" }, [h("i", { style: "width:" + (b.total / Math.max(1, maxTotal)) * 100 + "%" })])
          ]),
          h("td", { text: String(b.n) }),
          h("td", { text: b.ms.length ? fmtMs(b.total) : "—" }),
          h("td", { text: b.ms.length ? fmtMs(quantile(s, 0.5)) : "—" }),
          h("td", { text: b.ms.length ? fmtMs(quantile(s, 0.95)) : "—" }),
          h("td", { text: b.ms.length ? fmtMs(s[s.length - 1]) : "—" }),
          h("td", { class: b.err ? "err" : "", text: b.err ? String(b.err) : "—" })
        ]);
      }))
    ]);

    /* the slowest things in the session, clickable */
    var slow = timed.filter(function (r) { return r.prov === "recorded" && (r.kind === "model" || r.kind === "tool"); })
      .sort(function (a, b2) { return (b2.t1 - b2.t0) - (a.t1 - a.t0); }).slice(0, 6);
    var slowList = h("ol", { class: "slow" }, slow.map(function (r) {
      return h("li", {}, [
        h("a", {
          href: "#" + r.anchor, class: "slow-a",
          onclick: function (e) { e.preventDefault(); selectRecord(r.idx, true); }
        }, [
          h("b", { text: fmtMs(r.t1 - r.t0) }),
          h("span", { class: "slow-k", text: r.kind === "model" ? "step " + r.turn + "." + r.step : r.label }),
          h("span", { class: "slow-s", text: r.sub ? r.sub.slice(0, 90) : "",
            "data-tip": r.sub ? (r.kind === "model" ? "step " + r.turn + "." + r.step : r.label) + "\n" + r.sub.slice(0, 200) : null })
        ])
      ]);
    }));

    /* cost by turn */
    var maxUsd = 0;
    model.turns.forEach(function (t) { maxUsd = Math.max(maxUsd, t.usage.usd || 0); });
    var costRow = h("div", { class: "cost" }, model.turns.map(function (t) {
      var v = t.usage.usd || 0;
      return h("a", {
        class: "cost-b", href: "#turn-" + t.index,
        "data-tip": "turn " + t.index + " · " + fmtUsd(v) + "\n" + (oneLine(t.user.text).slice(0, 90) || "(no prompt)"),
        style: "height:" + Math.max(2, (v / Math.max(1e-9, maxUsd)) * 100) + "%"
      });
    }));

    return h("section", { class: "stats" }, [
      h("h2", { text: "where the time and money went" }),
      breakdown,
      h("div", { class: "stats-grid" }, [
        h("div", {}, [h("h3", { text: "tools" }), rows.length ? table : h("p", { class: "empty", text: "no tool calls" })]),
        h("div", {}, [
          h("h3", { text: "slowest" }),
          slow.length ? slowList : h("p", { class: "empty", text: "nothing with recorded timing" }),
          h("h3", { text: "cost by turn", style: "margin-top:22px" }),
          h("div", { class: "cost-wrap" }, [costRow]),
          h("p", { class: "cost-foot", text: "turn 1 → " + model.turns.length + " · " + fmtUsd(model.totals.usage.usd) + " total" })
        ])
      ])
    ]);
  }

  function refreshAggSelection() {
    each(document.querySelectorAll(".agg-row"), function (tr) {
      var name = qs(".agg-name", tr).textContent;
      tr.classList.toggle("on", state.tool === name);
    });
  }

  /* ------------------------------------------------------------ selection */

  function selectRecord(idx, scroll) {
    var r = records[idx];
    if (!r) return;
    state.selected = idx;
    ensureTurnBuilt(r.turn);
    each(document.querySelectorAll(".hl"), function (el) { el.classList.remove("hl"); });
    each(document.querySelectorAll(".ov-b.on"), function (el) { el.classList.remove("on"); });
    if (r.el) r.el.classList.add("on");
    var target = document.getElementById(r.anchor);
    if (r.toolAnchor) {
      var d = document.getElementById(r.toolAnchor);
      if (d) { d.open = true; target = d; }
    }
    if (!target) return;
    target.classList.add("hl");
    if (scroll) target.scrollIntoView({ behavior: "smooth", block: "center" });
    /* the zoom follows the selection when it is off screen */
    if (state.viewport && r.x0 !== null && (r.x1 < viewStart() || r.x0 > viewStart() + viewSpan())) {
      setViewport({ start: r.x0 - viewSpan() / 3, end: r.x0 - viewSpan() / 3 + viewSpan() });
    }
  }

  function stepMatch(dir) {
    if (!state.matches.length) return;
    state.matchAt = (state.matchAt + dir + state.matches.length) % state.matches.length;
    selectRecord(state.matches[state.matchAt].idx, true);
  }

  function moveSelection(dir, kindFilter) {
    var list = (visibleRecords.length ? visibleRecords : records)
      .filter(function (r) { return kindFilter ? kindFilter(r) : true; });
    if (!list.length) return;
    var at = -1;
    for (var i = 0; i < list.length; i++) if (list[i].idx === state.selected) { at = i; break; }
    var next = at === -1 ? (dir > 0 ? 0 : list.length - 1) : clamp(at + dir, 0, list.length - 1);
    selectRecord(list[next].idx, true);
  }

  /* -------------------------------------------------------------- keyboard */

  var SHORTCUTS = [
    ["j / k", "next / previous record"],
    ["J / K", "next / previous turn"],
    ["n / p", "next / previous search match"],
    ["/", "search"],
    ["enter", "open the selected record"],
    ["e / c", "expand / collapse everything"],
    ["+ / -", "zoom the overview in / out"],
    ["0", "reset zoom and filters"],
    ["1 – 4", "wall clock / no idle / durations / sequence"],
    ["g / G", "top / bottom"],
    ["esc", "clear the selection and the brush"],
    ["?", "this list"]
  ];

  var helpEl = null;
  function toggleHelp() {
    if (helpEl) { helpEl.remove(); helpEl = null; return; }
    helpEl = h("div", { class: "help", onclick: function (e) { if (e.target === helpEl) toggleHelp(); } }, [
      h("div", { class: "help-card" }, [
        h("h3", { text: "keyboard" }),
        h("dl", {}, SHORTCUTS.reduce(function (acc, s) {
          acc.push(h("dt", { text: s[0] }));
          acc.push(h("dd", { text: s[1] }));
          return acc;
        }, [])),
        h("p", { class: "help-foot", text: "the overview takes the mouse too: drag to focus a slice, wheel to zoom, right-drag to pan." })
      ])
    ]);
    document.body.appendChild(helpEl);
  }

  function wireKeys() {
    document.addEventListener("keydown", function (e) {
      var tag = (e.target.tagName || "").toLowerCase();
      if (tag === "input" || tag === "select" || tag === "textarea") return;
      if (e.metaKey || e.ctrlKey || e.altKey) return;
      var k = e.key;
      if (k === "/") { e.preventDefault(); ui.search.focus(); ui.search.select(); return; }
      if (k === "?") { e.preventDefault(); toggleHelp(); return; }
      if (k === "j") { e.preventDefault(); moveSelection(1); return; }
      if (k === "k") { e.preventDefault(); moveSelection(-1); return; }
      if (k === "J") { e.preventDefault(); moveSelection(1, function (r) { return r.kind === "user"; }); return; }
      if (k === "K") { e.preventDefault(); moveSelection(-1, function (r) { return r.kind === "user"; }); return; }
      if (k === "n") { e.preventDefault(); stepMatch(1); return; }
      if (k === "p") { e.preventDefault(); stepMatch(-1); return; }
      if (k === "e") { openAll(true); return; }
      if (k === "c") { openAll(false); return; }
      if (k === "g") { window.scrollTo({ top: 0, behavior: "smooth" }); return; }
      if (k === "G") { window.scrollTo({ top: document.body.scrollHeight, behavior: "smooth" }); return; }
      if (k === "+" || k === "=") { zoomBy(0.6); return; }
      if (k === "-" || k === "_") { zoomBy(1.7); return; }
      if (k === "0") { setViewport(null); resetFilters(); return; }
      if (k >= "1" && k <= "4") { setMode(MODES[Number(k) - 1][0]); return; }
      if (k === "Enter" && state.selected !== null) { selectRecord(state.selected, true); return; }
      if (k === "Escape") {
        if (helpEl) { toggleHelp(); return; }
        state.selected = null;
        each(document.querySelectorAll(".hl"), function (el) { el.classList.remove("hl"); });
        each(document.querySelectorAll(".ov-b.on"), function (el) { el.classList.remove("on"); });
        clearFocus();
      }
    });
  }

  function zoomBy(factor) {
    var center = viewStart() + viewSpan() / 2;
    var span = clamp(viewSpan() * factor, minSpan(), fullSpan());
    setViewport({ start: center - span / 2, end: center - span / 2 + span });
  }

  /* --------------------------------------------------------------- ledger */

  function stepSpans(step) {
    var t = step.timing;
    if (t.kind !== "recorded") return null;
    var modelMs = Math.max(0, t.modelEndedAt - t.startedAt);
    var toolMs = 0;
    var iv = step.tools.filter(function (x) { return x.timing; }).map(function (x) {
      return [x.timing.startedAt, x.timing.endedAt === undefined ? t.endedAt : x.timing.endedAt];
    }).sort(function (a, b) { return a[0] - b[0]; });
    var cur = null;
    for (var i = 0; i < iv.length; i++) {
      if (!cur || iv[i][0] > cur[1]) { if (cur) toolMs += cur[1] - cur[0]; cur = [iv[i][0], iv[i][1]]; }
      else if (iv[i][1] > cur[1]) cur[1] = iv[i][1];
    }
    if (cur) toolMs += cur[1] - cur[0];
    return {
      modelMs: modelMs, toolMs: toolMs, totalMs: Math.max(0, t.endedAt - t.startedAt),
      ttftMs: t.firstTokenAt !== undefined ? Math.max(0, t.firstTokenAt - t.startedAt) : undefined
    };
  }
  function turnSplit(turn) {
    var m = 0, tl = 0;
    for (var i = 0; i < turn.steps.length; i++) {
      var s = stepSpans(turn.steps[i]);
      if (!s) return null;
      m += s.modelMs; tl += s.toolMs;
    }
    return { modelMs: m, toolMs: tl };
  }
  function turnStart(turn) {
    var t0 = turn.startedAt;
    turn.steps.forEach(function (s) {
      if (s.timing.kind === "recorded") {
        var st = s.timing.startedAt - (s.timing.retryWaitMs || 0);
        if (st < t0) t0 = st;
      }
    });
    return t0;
  }

  /* Per-turn strip: the same records, but scaled to this turn alone, so a
   * 300 ms step is still visible inside a 20 minute session. */
  function turnStrip(turn) {
    var mine = records.filter(function (r) { return r.turn === turn.index && r.prov !== "none" && r.kind !== "user"; });
    if (!mine.length) return null;
    var t0 = turnStart(turn), t1 = Math.max(turn.endedAt, t0 + 1), span = t1 - t0;
    var rows = 0;
    var rowEnds = [];
    var track = h("div", { class: "ts-track" });
    mine.slice().sort(function (a, b) { return a.t0 - b.t0; }).forEach(function (r) {
      var left = clamp(((r.t0 - t0) / span) * 100, 0, 100);
      var width = clamp(((r.t1 - r.t0) / span) * 100, 0, 100 - left);
      var cls, top;
      if (r.kind === "model") { cls = "ts-b model" + (r.prov === "derived" ? " derived" : ""); top = 4; }
      else if (r.kind === "event") { cls = "ts-b ev"; top = 0; }
      else {
        var row = 0;
        while (row < rowEnds.length && rowEnds[row] > r.t0) row++;
        rowEnds[row] = r.t1; rows = Math.max(rows, row + 1);
        cls = "ts-b tool" + (r.error ? " err" : "") + (r.prov === "derived" ? " derived" : "") + (r.prov === "open" ? " open" : "");
        top = 18 + row * 7;
      }
      var el = h("span", {
        class: cls, "data-rec": String(r.idx),
        style: "left:" + left + "%;width:" + Math.max(width, 0.4) + "%;top:" + top + "px",
        onclick: function () { selectRecord(r.idx, false); },
        onpointerenter: function (ev) { showTip(ev, r); },
        onpointermove: function (ev) { showTip(ev, r); },
        onpointerleave: hideTip
      });
      if (r.kind === "model" && r.ttft !== undefined && r.prov === "recorded") {
        el.appendChild(h("i", { class: "ov-ttft", style: "left:" + clamp(((r.ttft - r.t0) / Math.max(1, r.t1 - r.t0)) * 100, 0, 100) + "%" }));
      }
      track.appendChild(el);
      r.stripEl = el;
    });
    track.style.height = (24 + rows * 7) + "px";
    var ruler = h("div", { class: "ts-ruler" });
    var tick = niceTick(span, 6, true);
    for (var x = 0; x <= span; x += tick) {
      var l = (x / span) * 100;
      if (l > 94) continue;
      ruler.appendChild(h("span", { class: "ov-tick", style: "left:" + l + "%", text: x === 0 ? "0" : fmtMs(x) }));
    }
    track.style.backgroundSize = ((tick / span) * 100) + "% 100%";
    return h("div", { class: "ts" }, [ruler, track]);
  }

  function stepCard(turn, s) {
    var t = s.timing, sp = stepSpans(s);
    var head = [h("span", { class: "n", text: "step " + s.index })];
    if (t.kind === "recorded") {
      head.push(h("span", {}, [h("b", { text: fmtMs(sp.totalMs) }), " total"]));
      head.push(h("span", {}, ["model ", h("b", { text: fmtMs(sp.modelMs) }), sp.ttftMs !== undefined ? " (first token " + fmtMs(sp.ttftMs) + ")" : ""]));
      if (s.tools.length) head.push(h("span", {}, ["tools ", h("b", { text: fmtMs(sp.toolMs) })]));
      if (t.retryWaitMs) head.push(h("span", { class: "warn", text: "after " + fmtMs(t.retryWaitMs) + " retry wait" }));
    } else if (t.kind === "derived") {
      head.push(h("span", {}, [h("b", { text: fmtMs(t.endedAt - t.startedAt) }), " wall time"]));
      head.push(h("span", { class: "quiet", text: "model and tools not split" }));
    } else {
      head.push(h("span", { class: "quiet", text: "timing not recorded" }));
    }
    if (s.interrupted) head.push(h("span", { class: "warn", text: "interrupted" }));
    head.push(h("span", { class: "mono", text: shortModel(s.model) }));
    var u = usageText(s.usage);
    if (u) head.push(h("span", { class: "step-usage", text: u }));

    var body = [h("div", { class: "step-head" }, head)];
    if (s.reasoning) {
      var thought = s.reasoningMs && s.reasoningMs.length
        ? " for " + fmtMs(s.reasoningMs.reduce(function (a, b) { return a + b; }, 0)) : "";
      body.push(h("details", {}, [h("summary", { text: "thought" + thought }), md("reason", s.reasoning)]));
    }
    if (s.text) body.push(md("text", s.text));
    if (s.tools.length) {
      body.push(h("ul", { class: "tools" }, s.tools.map(function (tool) {
        var dur = tool.timing
          ? (tool.timing.endedAt === undefined ? "did not return" : fmtMs(tool.timing.endedAt - tool.timing.startedAt))
          : tool.subagent && tool.subagent.durationMs !== undefined ? fmtMs(tool.subagent.durationMs) : "";
        var kids = [h("summary", {}, [
          h("span", { class: "name", text: tool.name }),
          h("span", { class: "input", text: tool.input, "data-tip": tool.input ? tool.name + "\n" + tool.input : null }),
          h("span", { class: "dur" + (tool.error ? " err" : ""), text: (tool.error ? "error · " : "") + dur })
        ])];
        if (tool.subagent) {
          var sm = [tool.subagent.agent];
          if (tool.subagent.steps !== undefined) sm.push(plural(tool.subagent.steps, "step"));
          var su = usageText(tool.subagent.usage);
          if (su) sm.push(su);
          kids.push(h("div", { class: "sub-meta", text: "subagent · " + sm.join(" · ") }));
          if (tool.subagent.prompt) kids.push(md("sub-prompt", tool.subagent.prompt));
        }
        if (tool.output !== undefined && tool.output !== "") kids.push(h("pre", { text: tool.output }));
        else if (!tool.subagent) kids.push(h("div", { class: "more", text: "no result recorded" }));
        var shown = (tool.output || "").length;
        if (tool.outputChars && tool.outputChars > shown) {
          kids.push(h("div", { class: "more", text: "showing the first " + fmtTok(shown) + " of " + fmtTok(tool.outputChars) + " characters" }));
        }
        return h("li", { class: "tool-li", "data-tool-anchor": "tool-" + turn.index + "-" + tool.toolCallId }, [
          h("details", {
            class: "tool" + (tool.error ? " err" : "") + (tool.subagent ? " sub" : ""),
            id: "tool-" + turn.index + "-" + tool.toolCallId
          }, kids)
        ]);
      })));
    }
    if (!s.text && !s.reasoning && !s.tools.length) {
      body.push(h("p", { class: "empty", text: s.interrupted ? "nothing streamed before the interrupt" : "empty step" }));
    }
    return h("article", {
      class: "step" + (t.kind === "derived" ? " derived-step" : t.kind === "none" ? " none-step" : ""),
      id: "step-" + turn.index + "-" + s.index, "data-turn": String(turn.index), "data-step": String(s.index)
    }, body);
  }

  function timingNote(turn) {
    if (!turn.steps.length || turn.fullyRecorded) return "";
    var recorded = turn.steps.filter(function (s) { return s.timing.kind === "recorded"; }).length;
    var none = turn.steps.filter(function (s) { return s.timing.kind === "none"; }).length;
    if (recorded > 0) return " · partial timing";
    if (none === turn.steps.length) return " · timing not recorded";
    return " · wall time only";
  }

  var pending = {};

  function turnSection(turn) {
    var t0 = turnStart(turn), dur = turn.endedAt - t0, split = turnSplit(turn);
    var stats = [h("div", {}, [h("b", { text: fmtMs(dur) }), split ? " · model " + fmtMs(split.modelMs) + " · tools " + fmtMs(split.toolMs) : " wall time"])];
    stats.push(h("div", {}, [plural(turn.steps.length, "step"), " · ",
      plural(turn.steps.reduce(function (n, s) { return n + s.tools.length; }, 0), "tool call")]));
    var u = usageText(turn.usage);
    if (u) stats.push(h("div", { text: u }));
    stats.push(h("div", {}, [
      h("a", { class: "jump", href: "#", "data-tip": "show this turn in the overview", text: clock(turn.startedAt),
        onclick: function (e) { e.preventDefault(); zoomToTurn(turn); } })
    ]));

    var head = h("div", { class: "turn-head" }, [
      h("div", {}, [
        h("p", { class: "turn-n", text: "turn " + turn.index + timingNote(turn) }),
        turn.user.text
          ? md("turn-q", turn.user.text)
          : h("p", { class: "turn-q empty", text: "(no prompt recorded)" })
      ]),
      h("div", { class: "turn-stats" }, stats)
    ]);
    var kids = [head];
    if (turn.events.length) {
      kids.push(h("ul", { class: "turn-events" }, turn.events.map(function (e) {
        return h("li", { text: e.label + " · " + clock(e.ts) });
      })));
    }
    var body = h("div", { class: "turn-body" });
    kids.push(body);
    var sec = h("section", { class: "turn", id: "turn-" + turn.index, "data-turn": String(turn.index) }, kids);
    pending[turn.index] = function () {
      if (turn.steps.length) {
        var strip = turnStrip(turn);
        if (strip) body.appendChild(strip);
        body.appendChild(h("div", { class: "steps" }, turn.steps.map(function (s) { return stepCard(turn, s); })));
      } else {
        body.appendChild(h("p", { class: "empty", text: "No reply was recorded for this turn." }));
      }
    };
    return sec;
  }

  function ensureTurnBuilt(n) {
    var build = pending[n];
    if (!build) return;
    delete pending[n];
    build();
  }
  function ensureAllBuilt() {
    Object.keys(pending).forEach(function (k) { ensureTurnBuilt(Number(k)); });
  }

  function zoomToTurn(turn) {
    var mine = records.filter(function (r) { return r.turn === turn.index && r.x0 !== null; });
    if (!mine.length) return;
    var lo = Infinity, hi = -Infinity;
    mine.forEach(function (r) { lo = Math.min(lo, r.x0); hi = Math.max(hi, r.x1); });
    var pad = Math.max(minSpan() / 4, (hi - lo) * 0.08);
    setViewport({ start: lo - pad, end: hi + pad });
    ov.root.scrollIntoView({ behavior: "smooth", block: "start" });
  }

  /* ----------------------------------------------------------------- boot */

  app.textContent = "";
  if (!model.turns.length) {
    app.appendChild(h("p", { class: "empty", text: "No turns in this session yet." }));
    return;
  }

  project(state.mode);
  var overview = buildOverview();
  var sticky = h("div", { class: "sticky" }, [buildToolbar()]);
  app.appendChild(sticky);
  if (hasTiming) app.appendChild(overview);
  app.appendChild(buildStats());

  var ledger = h("div", { class: "ledger" });
  model.turns.forEach(function (t) { ledger.appendChild(turnSection(t)); });
  app.appendChild(ledger);

  if (hasTiming) { renderBars(); wireOverview(); }
  wireKeys();

  /* build a turn's body when it comes near the viewport; a filter forces
   * the rest, since a hidden turn still has to be searchable */
  if (window.IntersectionObserver) {
    var io = new IntersectionObserver(function (entries) {
      entries.forEach(function (e) {
        if (!e.isIntersecting) return;
        ensureTurnBuilt(Number(e.target.getAttribute("data-turn")));
        io.unobserve(e.target);
      });
    }, { rootMargin: "800px 0px" });
    each(ledger.children, function (sec) { io.observe(sec); });
  } else {
    ensureAllBuilt();
  }

  var _applyFilters = applyFilters;
  applyFilters = function () {
    if (state.query || state.errorsOnly || state.tool || state.minMs || state.focus) ensureAllBuilt();
    _applyFilters();
  };

  applyFilters();

  if (location.hash) {
    var el = document.querySelector(location.hash);
    if (el) {
      var n = el.getAttribute("data-turn");
      if (n) ensureTurnBuilt(Number(n));
      el.scrollIntoView();
    }
  }
})();
