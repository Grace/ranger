package report

import (
	"fmt"
	"io"
	"strings"
)

// Render writes the explorer as one self-contained HTML file.
//
// No CDN, no fetch, no server. The payload is embedded as JSON and the page
// is inert without it, so the file works from file:// on a laptop with no
// network — which is where people are when they are looking at an incident.
func Render(w io.Writer, e Explorer) error {
	payload, err := e.JSON()
	if err != nil {
		return err
	}
	// The payload goes into a JSON script block rather than a JS literal, so
	// a span name containing "</script>" or a quote cannot break out of it.
	safe := strings.ReplaceAll(payload, "</", `<\/`)

	_, err = fmt.Fprintf(w, page, htmlEscape(e.Window), safe)
	return err
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

const page = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>inquest — %s</title>
<style>
:root {
  --paper:#faf8f4; --panel:#fffefb; --ink:#1b1d1e; --muted:#6b6862;
  --rule:#e2ddd3; --verdigris:#2f6f66; --ochre:#b08542; --oxblood:#7a2e2e;
  --wait:#ded9cf; --own:#2f6f66; --own-soft:#9dc3bd;
  --mono:"JetBrains Mono",ui-monospace,SFMono-Regular,Menlo,monospace;
  --sans:"Public Sans",-apple-system,BlinkMacSystemFont,"Segoe UI",system-ui,sans-serif;
  --serif:"Newsreader",Georgia,"Times New Roman",serif;
}
@media (prefers-color-scheme: dark) {
  :root:not([data-theme="light"]) {
    --paper:#16181a; --panel:#1d2023; --ink:#eceae5; --muted:#9a958c;
    --rule:#2f3337; --wait:#3a3f44; --own:#5fa89d; --own-soft:#3c6560;
    --ochre:#c9a05c; --oxblood:#c0605c; --verdigris:#5fa89d;
  }
}
* { box-sizing:border-box; }
body { margin:0; background:var(--paper); color:var(--ink); font-family:var(--sans); font-size:13px; }
h1,h2,h3 { font-family:var(--serif); font-weight:600; margin:0; }
.mono { font-family:var(--mono); font-variant-numeric:tabular-nums; }

header { padding:18px 22px 14px; border-bottom:1px solid var(--rule); }
header h1 { font-size:17px; letter-spacing:-0.2px; }
header .sub { color:var(--muted); margin-top:3px; font-size:12px; }
.verdict { margin-top:11px; padding:10px 13px; border-radius:5px;
  background:var(--panel); border:1px solid var(--rule); border-left:3px solid var(--oxblood); }
.verdict.none { border-left-color:var(--muted); }
.verdict .op { font-family:var(--mono); font-size:14px; font-weight:600; }
.verdict .why { color:var(--muted); margin-top:3px; font-size:12px; }

main { display:grid; grid-template-columns:300px minmax(0,1fr); gap:0; align-items:start; }
@media (max-width:900px) { main { grid-template-columns:1fr; } }
aside { border-right:1px solid var(--rule); padding:16px; min-height:70vh; }
section.stage { padding:16px 20px; min-width:0; }

.label { font-size:10px; text-transform:uppercase; letter-spacing:1px; color:var(--muted);
  margin:0 0 8px; font-weight:600; }
aside .label:not(:first-child) { margin-top:20px; }

.rank { border:1px solid var(--rule); border-radius:5px; background:var(--panel);
  padding:9px 10px; margin-bottom:7px; cursor:pointer; }
.rank:hover { border-color:var(--own-soft); }
.rank[aria-pressed="true"] { border-color:var(--own); box-shadow:0 0 0 1px var(--own); }
.rank.culprit { border-left:3px solid var(--oxblood); }
.rank.waiting { opacity:0.62; }
.rank .op { font-family:var(--mono); font-size:11.5px; font-weight:600; word-break:break-word; }
.rank .v { font-size:10.5px; color:var(--muted); margin-top:2px; }
.rank .nums { display:flex; gap:10px; margin-top:5px; font-family:var(--mono); font-size:10.5px; }
.rank .nums b { font-weight:600; color:var(--own); }
.rank.waiting .nums b { color:var(--muted); }

.chips { display:flex; flex-wrap:wrap; gap:5px; }
.chip { border:1px solid var(--rule); background:var(--panel); color:var(--muted);
  border-radius:11px; padding:2px 9px; font-size:11px; cursor:pointer; font-family:var(--mono); }
.chip[aria-pressed="true"] { background:var(--own); border-color:var(--own); color:var(--paper); }

.legend { display:flex; gap:14px; align-items:center; margin:0 0 10px; font-size:11px; color:var(--muted); }
.key { display:inline-block; width:11px; height:11px; border-radius:2px; vertical-align:-1px; margin-right:5px; }
.key.own { background:var(--own); } .key.wait { background:var(--wait); }
.key.cul { background:var(--oxblood); }

.tracewrap { max-height:270px; overflow-y:auto; border:1px solid var(--rule); border-radius:5px; }
table.traces { width:100%%; border-collapse:collapse; }
table.traces thead th { position:sticky; top:0; background:var(--paper); z-index:1; padding-top:7px; }
table.traces th { text-align:left; font-size:10px; text-transform:uppercase; letter-spacing:0.8px;
  color:var(--muted); font-weight:600; padding:0 8px 6px; border-bottom:1px solid var(--rule); }
table.traces td { padding:5px 8px; border-bottom:1px solid var(--rule); font-size:12px; }
table.traces tr { cursor:pointer; }
table.traces tr:hover td { background:var(--panel); }
table.traces tr[aria-selected="true"] td { background:var(--panel); box-shadow:inset 3px 0 0 var(--own); }
.dot { display:inline-block; width:6px; height:6px; border-radius:50%%; background:var(--oxblood); margin-right:6px; }
.dot.ok { background:transparent; }

.tl { margin-top:6px; }
.tlrow { display:grid; grid-template-columns:minmax(140px,26%%) 1fr; gap:10px; align-items:center;
  padding:2px 0; border-radius:3px; cursor:pointer; }
.tlrow:hover { background:var(--panel); }
.tlrow[aria-selected="true"] { background:var(--panel); }
.tlname { font-family:var(--mono); font-size:11px; white-space:nowrap; overflow:hidden; text-overflow:ellipsis; }
.tlname .svc { color:var(--muted); }
.tltrack { position:relative; height:15px; background:linear-gradient(var(--rule),var(--rule)) left center/100%% 1px no-repeat; }
.bar { position:absolute; top:1px; height:13px; border-radius:2px; overflow:hidden; display:flex; min-width:2px; }
.bar .own { background:var(--own); }
.bar .wait { background:var(--wait); }
.bar.culprit { outline:1.5px solid var(--oxblood); outline-offset:1px; }
.bar.culprit .own { background:var(--oxblood); }
.bar.failed { box-shadow:inset 0 0 0 1.5px var(--oxblood); }
.tlrow .t { font-family:var(--mono); font-size:10px; color:var(--muted); }

.detail { margin-top:14px; border:1px solid var(--rule); border-radius:5px; background:var(--panel); padding:12px 14px; }
.detail h3 { font-size:13px; font-family:var(--mono); }
.kv { display:grid; grid-template-columns:auto 1fr; gap:3px 14px; margin-top:9px;
  font-family:var(--mono); font-size:11px; }
.kv dt { color:var(--muted); }
.kv dd { margin:0; word-break:break-all; }

.dist { margin-top:8px; }
.dist svg { display:block; width:100%%; height:46px; }
.dist .cap { font-size:10.5px; color:var(--muted); font-family:var(--mono); margin-top:2px; }

.empty { color:var(--muted); font-style:italic; padding:14px 0; }
footer { padding:14px 22px 26px; color:var(--muted); font-size:11px; border-top:1px solid var(--rule); margin-top:20px; }
</style>
</head>
<body>
<script type="application/json" id="payload">%s</script>
<div id="app"></div>
<script>
(function () {
  var D = JSON.parse(document.getElementById("payload").textContent);
  var app = document.getElementById("app");

  var state = { trace: null, span: null, opFilter: null, services: {} };
  D.services.forEach(function (s) { state.services[s] = true; });

  function esc(s) {
    return String(s == null ? "" : s).replace(/[&<>"]/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c];
    });
  }
  function ms(v) {
    if (v == null) return "—";
    if (v < 1) return (v * 1000).toFixed(0) + "µs";
    if (v < 1000) return v.toFixed(1) + "ms";
    return (v / 1000).toFixed(2) + "s";
  }
  function signed(v) { return (v >= 0 ? "+" : "−") + ms(Math.abs(v)); }

  function visibleTraces() {
    return D.traces.filter(function (t) {
      if (!t.spans.some(function (s) { return state.services[s.service]; })) return false;
      if (state.opFilter) {
        return t.spans.some(function (s) { return s.service + " · " + s.name === state.opFilter; });
      }
      return true;
    });
  }

  function render() {
    var traces = visibleTraces();
    if (state.trace && !traces.some(function (t) { return t.id === state.trace; })) state.trace = null;
    if (!state.trace && traces.length) state.trace = traces[0].id;

    app.innerHTML =
      header() +
      '<main>' +
        '<aside>' + ranking() + services() + '</aside>' +
        '<section class="stage">' + traceTable(traces) + timeline() + detail() + distribution() + '</section>' +
      '</main>' +
      '<footer>Self time is a span&rsquo;s duration minus the wall clock its children covered. ' +
      'A bar that is nearly all <b>waiting</b> did not get slower; something below it did. ' +
      'Generated ' + esc(D.generated) + ' · ' + D.considered + ' operations ranked, ' + D.skipped + ' skipped for thin samples.</footer>';

    wire();
  }

  function header() {
    var v = D.localized
      ? '<div class="verdict"><div class="op">' + esc(D.culprit) + '</div>' +
        '<div class="why">' + esc(topWhy()) + '</div></div>'
      : '<div class="verdict none"><div class="op">No operation explains this window</div>' +
        '<div class="why">Nothing cleared the reporting threshold. The ranking is shown anyway &mdash; the near misses are the useful part.</div></div>';
    return '<header><h1>inquest</h1>' +
      '<div class="sub mono">' + esc(D.window) + '</div>' + v + '</header>';
  }
  function topWhy() {
    var r = D.ranking[0];
    if (!r) return "";
    return r.verdict + " · self time " + ms(r.baseSelfMs) + " → " + ms(r.incSelfMs) +
      " (" + signed(r.selfShiftMs) + "), while its total duration moved " + signed(r.durShiftMs);
  }

  function ranking() {
    if (!D.ranking.length) return '<p class="label">Ranking</p><p class="empty">Nothing to rank.</p>';
    return '<p class="label">Ranking</p>' + D.ranking.map(function (r) {
      var cls = "rank" + (r.culprit ? " culprit" : "") +
        (r.verdict.indexOf("waiting") === 0 ? " waiting" : "");
      return '<div class="' + cls + '" data-op="' + esc(r.op) + '" role="button" tabindex="0" ' +
        'aria-pressed="' + (state.opFilter === r.op) + '">' +
        '<div class="op">' + esc(r.op) + '</div>' +
        '<div class="v">' + esc(r.verdict) + '</div>' +
        '<div class="nums"><span>self <b>' + signed(r.selfShiftMs) + '</b></span>' +
        '<span>dur ' + signed(r.durShiftMs) + '</span>' +
        '<span>z ' + r.z.toFixed(1) + '</span></div></div>';
    }).join("");
  }

  function services() {
    return '<p class="label">Services</p><div class="chips">' + D.services.map(function (s) {
      return '<button class="chip" data-svc="' + esc(s) + '" aria-pressed="' + !!state.services[s] + '">' +
        esc(s) + '</button>';
    }).join("") + '</div>';
  }

  function traceTable(traces) {
    if (!traces.length) return '<p class="label">Traces</p><p class="empty">No trace matches these filters.</p>';
    var rows = traces.slice(0, 200).map(function (t) {
      return '<tr data-trace="' + esc(t.id) + '" aria-selected="' + (state.trace === t.id) + '">' +
        '<td><span class="dot ' + (t.failed ? "" : "ok") + '"></span><span class="mono">' + esc(t.id.slice(0, 12)) + '</span></td>' +
        '<td class="mono">' + esc(t.service) + '</td>' +
        '<td class="mono">' + esc(t.root) + '</td>' +
        '<td class="mono">' + ms(t.durMs) + '</td>' +
        '<td class="mono">' + t.spans.length + '</td></tr>';
    }).join("");
    return '<p class="label">Traces &mdash; ' + traces.length + ' matching, slowest first</p>' +
      '<div class="tracewrap"><table class="traces">' +
      '<thead><tr><th>trace</th><th>entry service</th><th>root span</th><th>duration</th><th>spans</th></tr></thead>' +
      '<tbody>' + rows + '</tbody></table></div>';
  }

  function currentTrace() {
    return D.traces.filter(function (t) { return t.id === state.trace; })[0];
  }

  function timeline() {
    var t = currentTrace();
    if (!t) return "";
    var rows = t.spans.map(function (s, i) {
      var offset = t.durMs > 0 ? (100 * s.offsetMs / t.durMs) : 0;
      var width = t.durMs > 0 ? Math.max(100 * s.durMs / t.durMs, 0.4) : 0;
      var ownPct = s.durMs > 0 ? (100 * s.selfMs / s.durMs) : 100;
      var cls = "bar" + (s.culprit ? " culprit" : "") + (s.failed ? " failed" : "");
      return '<div class="tlrow" data-span="' + i + '" aria-selected="' + (state.span === i) + '">' +
        '<div class="tlname" style="padding-left:' + (s.depth * 12) + 'px" title="' + esc(s.service + " · " + s.name) + '">' +
          '<span class="svc">' + esc(s.service) + '</span> ' + esc(s.name) + '</div>' +
        '<div class="tltrack"><div class="' + cls + '" style="left:' + offset + '%%;width:' + width + '%%">' +
          '<div class="own" style="width:' + ownPct + '%%"></div>' +
          '<div class="wait" style="width:' + (100 - ownPct) + '%%"></div>' +
        '</div></div></div>';
    }).join("");

    return '<p class="label" style="margin-top:22px">Timeline &mdash; ' + esc(t.id.slice(0, 16)) +
      ' · ' + ms(t.durMs) + '</p>' +
      '<div class="legend">' +
        '<span><i class="key own"></i>own work (self time)</span>' +
        '<span><i class="key wait"></i>waiting on something below</span>' +
        '<span><i class="key cul"></i>localized cause</span>' +
      '</div><div class="tl">' + rows + '</div>';
  }

  function detail() {
    var t = currentTrace();
    if (!t || state.span == null || !t.spans[state.span]) return "";
    var s = t.spans[state.span];
    var attrs = Object.keys(s.attrs || {}).sort().map(function (k) {
      return '<dt>' + esc(k) + '</dt><dd>' + esc(s.attrs[k]) + '</dd>';
    }).join("");
    return '<div class="detail"><h3>' + esc(s.service + " · " + s.name) + '</h3>' +
      '<dl class="kv">' +
        '<dt>duration</dt><dd>' + ms(s.durMs) + '</dd>' +
        '<dt>own work</dt><dd>' + ms(s.selfMs) + ' (' +
          (s.durMs > 0 ? (100 * s.selfMs / s.durMs).toFixed(0) : "100") + '%% of its duration)</dd>' +
        '<dt>waiting</dt><dd>' + ms(s.durMs - s.selfMs) + '</dd>' +
        '<dt>status</dt><dd>' + (s.failed ? "ERROR" : "ok") + '</dd>' +
        '<dt>span id</dt><dd>' + esc(s.id) + '</dd>' +
        (attrs ? attrs : "") +
      '</dl></div>';
  }

  // Two overlaid histograms of the same operation's self time, baseline
  // against incident. The ranking asserts a shift in the median; this is the
  // shape that median came from, so the assertion can be checked.
  function distribution() {
    var op = state.opFilter || (D.ranking[0] && D.ranking[0].op);
    var d = D.ops.filter(function (o) { return o.op === op; })[0];
    if (!d || (!d.baseline.length && !d.incident.length)) return "";

    var all = d.baseline.concat(d.incident);
    var hi = Math.max.apply(null, all), lo = Math.min.apply(null, all);
    if (!(hi > lo)) hi = lo + 1;
    var bins = 34, W = 800, H = 40;
    function hist(xs) {
      var h = new Array(bins).fill(0);
      xs.forEach(function (x) {
        var i = Math.floor(bins * (x - lo) / (hi - lo));
        if (i >= bins) i = bins - 1; if (i < 0) i = 0;
        h[i]++;
      });
      return h;
    }
    var a = hist(d.baseline), b = hist(d.incident);
    var peak = Math.max(Math.max.apply(null, a), Math.max.apply(null, b), 1);
    function bars(h, fill, op2) {
      return h.map(function (n, i) {
        var bw = W / bins, bh = H * n / peak;
        return '<rect x="' + (i * bw + 0.6) + '" y="' + (H - bh) + '" width="' + (bw - 1.2) +
          '" height="' + bh + '" fill="' + fill + '" opacity="' + op2 + '"/>';
      }).join("");
    }
    return '<p class="label" style="margin-top:22px">Self time distribution &mdash; ' + esc(op) + '</p>' +
      '<div class="dist"><svg viewBox="0 0 ' + W + ' ' + H + '" preserveAspectRatio="none" role="img" ' +
      'aria-label="baseline and incident self time distributions">' +
      bars(a, "var(--wait)", "1") + bars(b, "var(--oxblood)", "0.75") + '</svg>' +
      '<div class="cap">' + ms(lo) + ' &hellip; ' + ms(hi) +
      ' &nbsp;·&nbsp; grey = baseline (' + d.baseline.length + ' samples), red = incident (' +
      d.incident.length + ')</div></div>';
  }

  function wire() {
    app.querySelectorAll("[data-op]").forEach(function (el) {
      el.addEventListener("click", function () {
        state.opFilter = state.opFilter === el.dataset.op ? null : el.dataset.op;
        state.span = null;
        render();
      });
    });
    app.querySelectorAll("[data-svc]").forEach(function (el) {
      el.addEventListener("click", function () {
        state.services[el.dataset.svc] = !state.services[el.dataset.svc];
        render();
      });
    });
    app.querySelectorAll("[data-trace]").forEach(function (el) {
      el.addEventListener("click", function () {
        state.trace = el.dataset.trace; state.span = null; render();
      });
    });
    app.querySelectorAll("[data-span]").forEach(function (el) {
      el.addEventListener("click", function () {
        var i = +el.dataset.span;
        state.span = state.span === i ? null : i;
        render();
      });
    });
  }

  render();
})();
</script>
</body>
</html>
`
