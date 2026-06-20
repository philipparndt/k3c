#!/usr/bin/env bash
# Generate a self-contained interactive HTML report from a benchmark run.
#
#   ./report.sh [results-dir]     (default: newest results/<run>)
#
# Produces <results-dir>/report.html — no external/CDN deps (offline-friendly),
# data embedded inline. Open it in a browser.
set -euo pipefail

BENCH_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RESULTS_DIR="${RESULTS_DIR:-$BENCH_ROOT/results}"
DIR="${1:-$(ls -dt "$RESULTS_DIR"/*/ 2>/dev/null | head -1)}"
[ -n "${DIR:-}" ] && [ -f "$DIR/results.jsonl" ] || { echo "no results.jsonl found (dir: ${DIR:-none})" >&2; exit 1; }

OUT="$DIR/report.html"
AGG="$(mktemp)"; ENVJSON="$DIR/env.json"; [ -f "$ENVJSON" ] || ENVJSON=/dev/null

# aggregate means per (benchmark, variant, metric, engine)
jq -s '
  group_by([.benchmark,.variant,.metric,.engine])
  | map({benchmark:.[0].benchmark, variant:.[0].variant, metric:.[0].metric,
         engine:.[0].engine, unit:.[0].unit,
         mean:(map(.value)|add/length), n:length})
' "$DIR/results.jsonl" > "$AGG"

cat > "$OUT" <<'HTML'
<!doctype html>
<html lang="en"><head><meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>k3c benchmark report</title>
<style>
  :root{--bg:#14141c;--panel:#1d1d28;--line:#2c2c3a;--txt:#cdcdd8;--dim:#6b6b7c;
    --accent:#7d79f6;--good:#42c883;--mono:"SFMono-Regular",ui-monospace,Menlo,monospace}
  *{box-sizing:border-box}
  body{margin:0;background:radial-gradient(1200px 700px at 50% -10%,#1c1c2b,var(--bg) 60%);
    color:var(--txt);font-family:var(--mono);padding:24px;line-height:1.5}
  h1{font-size:22px;margin:0 0 4px}h1 .a{color:var(--accent)}
  .meta{color:var(--dim);font-size:12px;margin-bottom:16px}
  .meta b{color:var(--txt);font-weight:400}
  .bar-tools{display:flex;gap:8px;flex-wrap:wrap;align-items:center;margin:14px 0}
  .btn{font-family:var(--mono);font-size:12px;color:var(--txt);background:var(--panel);
    border:1px solid var(--line);border-radius:7px;padding:6px 11px;cursor:pointer}
  .btn.on{border-color:var(--accent);color:var(--accent);background:#23233a}
  .legend{margin-left:auto;display:flex;gap:12px;flex-wrap:wrap}
  .lg{display:flex;align-items:center;gap:6px;font-size:12px;cursor:pointer;user-select:none}
  .lg .sw{width:11px;height:11px;border-radius:3px}.lg.off{opacity:.35}
  .grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(360px,1fr));gap:14px}
  .card{background:var(--panel);border:1px solid var(--line);border-radius:12px;padding:14px 16px}
  .card h3{margin:0 0 2px;font-size:13px}.card .sub{color:var(--dim);font-size:11px;margin-bottom:10px}
  .row{display:flex;align-items:center;gap:8px;margin:6px 0}
  .row .name{width:74px;flex:none;font-size:12px;color:var(--dim)}
  .track{flex:1;background:#0f0f17;border-radius:5px;overflow:hidden;height:22px;position:relative}
  .fill{display:block;height:100%;border-radius:5px;transition:width .25s;min-width:2px}
  .row .val{width:78px;flex:none;text-align:right;font-size:12px}
  .win{color:var(--good)}.note{color:var(--dim);font-size:11px;margin-top:12px}
</style></head><body>
<h1><span class="a">k3c</span> benchmark report</h1>
<div class="meta" id="meta"></div>
<div class="bar-tools"><span style="font-size:12px;color:var(--dim)">metric:</span>
  <span id="filters"></span>
  <span class="legend" id="legend"></span>
</div>
<div class="grid" id="grid"></div>
<div class="note">Bars scaled to the slowest per card; <b style="color:#42c883">green = best</b>, <b style="color:#56b2f2">blue = others</b> (lower is better). ★ marks the best. Click a legend item to toggle an engine. Δ is relative to k3c.</div>
<script id="data" type="application/json">/*DATA*/</script>
<script id="env" type="application/json">/*ENV*/</script>
<script>
const DATA=JSON.parse(document.getElementById('data').textContent);
const ENV=JSON.parse(document.getElementById('env').textContent);
const COLORS={k3c:'#7d79f6',orbstack:'#42c883',rancher:'#e2c04a',k3d:'#56b2f2'};
const ORDER=['k3c','orbstack','rancher','k3d'];
const hidden=new Set();
let family='all';

function fmt(v,u){ if(u==='ms') return v>=1000?(v/1000).toFixed(1)+'s':Math.round(v)+'ms';
  if(u==='mW') return v>=1000?(v/1000).toFixed(2)+'W':Math.round(v)+'mW'; return Math.round(v)+(u||''); }
function fam(u){ return u==='mW'?'power':(u==='ms'?'time':'other'); }

function meta(){
  const e=ENV||{};
  document.getElementById('meta').innerHTML =
   `<b>${e.chip||'?'}</b> · macOS <b>${e.macos||'?'}</b> · run <b>${e.run||'?'}</b>`
   +(e.iterations?` · <b>${e.iterations}</b> iters`:'')
   +(e.power_window_s?` · power window <b>${e.power_window_s}s</b>`:'');
}
function engines(){ return ORDER.filter(e=>DATA.some(d=>d.engine===e)); }

function legend(){
  const el=document.getElementById('legend'); el.innerHTML='';
  engines().forEach(e=>{
    const s=document.createElement('span'); s.className='lg'+(hidden.has(e)?' off':'');
    s.innerHTML=`<span class="sw" style="background:#5a5a6b"></span>${e}`;
    s.onclick=()=>{hidden.has(e)?hidden.delete(e):hidden.add(e);render();};
    el.appendChild(s);
  });
}
function filters(){
  const fams=['all',...[...new Set(DATA.map(d=>fam(d.unit)))].filter(f=>f!=='other')];
  const el=document.getElementById('filters'); el.innerHTML='';
  fams.forEach(f=>{ const b=document.createElement('button');
    b.className='btn'+(f===family?' on':''); b.textContent=f;
    b.onclick=()=>{family=f;render();}; el.appendChild(b); });
}
function key(d){return d.benchmark+' · '+d.variant+' · '+d.metric;}

function render(){
  legend(); filters();
  const grid=document.getElementById('grid'); grid.innerHTML='';
  const cards={};
  DATA.filter(d=>family==='all'||fam(d.unit)===family)
      .forEach(d=>{(cards[key(d)]=cards[key(d)]||[]).push(d);});
  Object.keys(cards).sort().forEach(k=>{
    const rows=cards[k].filter(d=>!hidden.has(d.engine));
    if(!rows.length) return;
    const unit=rows[0].unit, max=Math.max(...rows.map(r=>r.mean));
    const best=Math.min(...rows.map(r=>r.mean));
    const k3c=(cards[k].find(d=>d.engine==='k3c')||{}).mean;
    const c=document.createElement('div'); c.className='card';
    c.innerHTML=`<h3>${cards[k][0].benchmark}</h3><div class="sub">${cards[k][0].variant} · ${cards[k][0].metric}</div>`;
    ORDER.filter(e=>rows.some(r=>r.engine===e)).forEach(e=>{
      const r=rows.find(x=>x.engine===e); const w=Math.max(2,(r.mean/max)*100);
      const win=r.mean===best;
      let delta=''; if(k3c&&e!=='k3c'){const x=r.mean/k3c; delta=` <span style="color:var(--dim)">(${x.toFixed(2)}×)</span>`;}
      const row=document.createElement('div'); row.className='row';
      const bar = win ? '#42c883' : '#56b2f2';
      row.innerHTML=`<span class="name">${e}</span>
        <span class="track"><span class="fill" style="width:${w}%;background:${bar}" title="${fmt(r.mean,unit)}"></span></span>
        <span class="val${win?' win':''}">${win?'★ ':''}${fmt(r.mean,unit)}${delta}</span>`;
      c.appendChild(row);
    });
    grid.appendChild(c);
  });
}
meta(); render();
</script></body></html>
HTML

python3 - "$OUT" "$AGG" "$ENVJSON" <<'PY'
import sys
out,agg,envf=sys.argv[1:4]
s=open(out).read()
s=s.replace('/*DATA*/', (open(agg).read().strip() or '[]'))
try: e=open(envf).read().strip() or '{}'
except OSError: e='{}'
s=s.replace('/*ENV*/', e)
open(out,'w').write(s)
PY
rm -f "$AGG"
echo "$OUT"
