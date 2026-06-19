import { useLayoutEffect, useState } from 'preact/hooks';
import type { Flows } from '../app';
import { C } from '../util';

type Kind = 'struct' | 'egress' | 'pull';
type Edge = { from: string; to: string; kind: Kind };
type Box = { l: number; t: number; w: number; h: number };
type Item = Edge & { d: string; color: string; i: number };

function color(kind: Kind): string {
  return kind === 'pull' ? C.accent : kind === 'egress' ? C.cool : C.dim;
}

function box(el: HTMLElement, stage: DOMRect): Box {
  const r = el.getBoundingClientRect();
  return { l: r.left - stage.left, t: r.top - stage.top, w: r.width, h: r.height };
}

// route returns an orthogonal poly-line (with a single mid elbow) between two
// boxes: it exits the face pointing at the target and enters the opposite face.
// Aligned boxes collapse to a straight run; offset boxes get a clean right
// angle instead of a sweeping S-curve.
function route(a: Box, b: Box): number[][] {
  const acx = a.l + a.w / 2;
  const acy = a.t + a.h / 2;
  const bcx = b.l + b.w / 2;
  const bcy = b.t + b.h / 2;
  const dx = bcx - acx;
  const dy = bcy - acy;
  if (Math.abs(dy) >= Math.abs(dx)) {
    const sy = dy > 0 ? a.t + a.h : a.t;
    const ty = dy > 0 ? b.t : b.t + b.h;
    const my = (sy + ty) / 2;
    return [[acx, sy], [acx, my], [bcx, my], [bcx, ty]];
  }
  const sx = dx > 0 ? a.l + a.w : a.l;
  const tx = dx > 0 ? b.l : b.l + b.w;
  const mx = (sx + tx) / 2;
  return [[sx, acy], [mx, acy], [mx, bcy], [tx, bcy]];
}

// roundedPath draws the poly-line with rounded corners (radius r).
function roundedPath(pts: number[][], r: number): string {
  if (pts.length < 2) return '';
  const dist = (p: number[], q: number[]) => Math.hypot(q[0] - p[0], q[1] - p[1]);
  let d = `M${pts[0][0]},${pts[0][1]}`;
  for (let i = 1; i < pts.length - 1; i++) {
    const p = pts[i - 1];
    const c = pts[i];
    const n = pts[i + 1];
    const d1 = Math.min(r, dist(p, c) / 2);
    const d2 = Math.min(r, dist(c, n) / 2);
    if (d1 < 0.5 || d2 < 0.5) continue; // collinear / zero-length corner
    const u1 = [(c[0] - p[0]) / dist(p, c), (c[1] - p[1]) / dist(p, c)];
    const u2 = [(n[0] - c[0]) / dist(c, n), (n[1] - c[1]) / dist(c, n)];
    const e = [c[0] - u1[0] * d1, c[1] - u1[1] * d1];
    const s = [c[0] + u2[0] * d2, c[1] + u2[1] * d2];
    d += ` L${e[0]},${e[1]} Q${c[0]},${c[1]} ${s[0]},${s[1]}`;
  }
  const last = pts[pts.length - 1];
  d += ` L${last[0]},${last[1]}`;
  return d;
}

function vmName(id: string): string {
  return id.startsWith('vm-') ? id.slice(3) : '';
}

// active decides whether an edge carries data right now, from measured signals.
// Pulls route VM -> cache -> internet; other VM traffic is egress (VM ->
// daemon -> internet). During a pull, that VM's bytes count as the pull, not
// egress, so egress is suppressed for it.
function active(e: Edge, f: Flows): boolean {
  if (e.kind === 'struct') return false;
  if (e.kind === 'pull') {
    if (e.from === 'n-cache') return f.pulls; // cache -> internet
    return f.pulls && !!f.traffic[vmName(e.from)]; // vm -> cache
  }
  // egress
  if (e.from === 'n-daemon') {
    // any VM with traffic that is not currently a pull
    return !f.pulls && Object.values(f.traffic).some(Boolean);
  }
  return !f.pulls && !!f.traffic[vmName(e.from)]; // vm -> daemon
}

export function Edges({
  sig,
  machineNames,
  flows,
}: {
  sig: string;
  machineNames: string[];
  flows: Flows;
}) {
  const [vb, setVb] = useState('0 0 0 0');
  const [items, setItems] = useState<Item[]>([]);

  useLayoutEffect(() => {
    function recompute() {
      const stage = document.getElementById('stage');
      if (!stage) return;
      const sr = stage.getBoundingClientRect();
      setVb(`0 0 ${stage.clientWidth} ${stage.clientHeight}`);

      const edges: Edge[] = [
        { from: 'n-daemon', to: 'n-runtime', kind: 'struct' },
        { from: 'n-daemon', to: 'n-internet', kind: 'egress' },
        { from: 'n-cache', to: 'n-internet', kind: 'pull' },
      ];
      for (const n of machineNames) {
        edges.push({ from: 'n-runtime', to: 'vm-' + n, kind: 'struct' });
        edges.push({ from: 'vm-' + n, to: 'n-cache', kind: 'pull' });
        edges.push({ from: 'vm-' + n, to: 'n-daemon', kind: 'egress' });
      }

      const out: Item[] = [];
      edges.forEach((e, i) => {
        const A = document.getElementById(e.from) as HTMLElement | null;
        const B = document.getElementById(e.to) as HTMLElement | null;
        if (!A || !B || A.style.display === 'none' || B.style.display === 'none') return;
        out.push({ ...e, d: roundedPath(route(box(A, sr), box(B, sr)), 18), color: color(e.kind), i });
      });
      setItems(out);
    }
    recompute();
    window.addEventListener('resize', recompute);
    return () => window.removeEventListener('resize', recompute);
  }, [sig]);

  return (
    <svg class="edges" viewBox={vb} preserveAspectRatio="none">
      {items.map((it) => {
        const on = active(it, flows);
        const dur = 2.2 + (it.i % 3) * 0.5 + 's';
        const begin = 0.9 + (it.i % 2) * 0.4 + 's';
        return (
          <g key={it.i}>
            <path
              id={'p' + it.i}
              d={it.d}
              fill="none"
              stroke={it.color}
              stroke-opacity={on ? '0.55' : it.kind === 'struct' ? '0.12' : '0.16'}
              stroke-width="2"
              stroke-dasharray="5 6"
            />
            {/* particles only when real data flows; stable keys let Preact reuse
                the SMIL nodes so continuous flow does not jitter */}
            {on && (
              <circle r="3" fill={it.color}>
                <animateMotion dur={dur} repeatCount="indefinite">
                  <mpath href={'#p' + it.i} />
                </animateMotion>
              </circle>
            )}
            {on && (
              <circle r="3" fill={it.color}>
                <animateMotion dur={dur} begin={begin} repeatCount="indefinite">
                  <mpath href={'#p' + it.i} />
                </animateMotion>
              </circle>
            )}
          </g>
        );
      })}
    </svg>
  );
}
