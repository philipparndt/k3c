import type { PodSeries } from '../types';

// MAX_ROWS caps how many pods a heatmap draws, so a busy cluster stays legible;
// pods are sorted by their share of the total so the heaviest are always shown.
const MAX_ROWS = 30;

function avg(a: number[]): number {
  if (a.length === 0) return 0;
  let s = 0;
  for (const v of a) s += v;
  return s / a.length;
}

// Heatmap renders a pods × ticks grid for one metric ("cpu" or "mem"). Cell
// color intensity scales with the value relative to the busiest cell, and each
// pod's row HEIGHT is proportional to that pod's share of the cluster's total
// computed resource (sum of per-pod averages) — so a pod using more of the
// cluster occupies proportionally more of the heatmap.
export function Heatmap({
  series,
  metric,
  color,
  label,
  format,
}: {
  series: PodSeries[];
  metric: 'cpu' | 'mem';
  color: string;
  label: string;
  format: (v: number) => string;
}) {
  const rows = series
    .map((p) => ({ p, vals: p[metric], weight: avg(p[metric]) }))
    .filter((r) => r.vals.length > 0)
    .sort((a, b) => b.weight - a.weight)
    .slice(0, MAX_ROWS);

  if (rows.length === 0) return null;

  const total = rows.reduce((s, r) => s + r.weight, 0) || 1;
  const cellMax = Math.max(1e-9, ...rows.flatMap((r) => r.vals));
  const ticks = Math.max(...rows.map((r) => r.vals.length));

  return (
    <div class="heat">
      <div class="heat-title">
        {label} <small>· {rows.length} pods · row size = share of total</small>
      </div>
      <div class="heat-grid">
        {rows.map((r) => (
          <div class="heat-row" key={r.p.uid} style={{ flexGrow: r.weight / total }}>
            <div class="heat-label" title={r.p.name}>
              {r.p.name}
            </div>
            <div class="heat-cells">
              {/* right-align short histories so the latest tick is flush right */}
              {Array.from({ length: ticks - r.vals.length }).map((_, i) => (
                <div class="heat-cell" key={'pad' + i} />
              ))}
              {r.vals.map((v, i) => (
                <div
                  class="heat-cell"
                  key={i}
                  title={format(v)}
                  style={{ background: color, opacity: 0.08 + (v / cellMax) * 0.92 }}
                />
              ))}
            </div>
            <div class="heat-val">{format(r.weight)}</div>
          </div>
        ))}
      </div>
    </div>
  );
}
