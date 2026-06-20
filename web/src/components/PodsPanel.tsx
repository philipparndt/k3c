import type { Machine } from '../types';
import { usePodStream } from '../pods';
import { Sparkline } from './Sparkline';
import { Heatmap } from './Heatmap';
import { C, human } from '../util';

// cpuPct formats a CPU rate (fraction of a core) as a percentage of one core.
function cpuPct(v: number): string {
  return (v * 100).toFixed(v < 0.1 ? 1 : 0) + '%';
}

function last(a: number[]): number {
  return a.length ? a[a.length - 1] : 0;
}

// PodsPanel shows the pods of the selected cluster: a live list with per-pod
// CPU and memory sparklines, plus CPU and memory heatmaps over the recent
// window. When the cluster is not running (no pods stream), it shows an empty
// state rather than erroring.
export function PodsPanel({ machine, onClose }: { machine: Machine; onClose: () => void }) {
  const running = machine.state === 'running';
  const { series, connected } = usePodStream(running ? machine.name : null);
  const pods = series.slice().sort((a, b) => a.name.localeCompare(b.name));

  return (
    <div class="pods">
      <div class="pods-head">
        <span class="pods-title">pods · {machine.name}</span>
        <span class={'live' + (connected ? '' : ' bad')}>
          <span class="pulse" />
          <span class="lbl">{running ? (connected ? 'streaming' : 'connecting…') : 'stopped'}</span>
        </span>
        <button class="act" title="close" onClick={onClose}>
          ✕
        </button>
      </div>

      {!running ? (
        <div class="note">cluster is not running — no pods to show</div>
      ) : pods.length === 0 ? (
        <div class="note">{connected ? 'no pods yet…' : 'connecting…'}</div>
      ) : (
        <>
          <div class="podlist">
            {pods.map((p) => (
              <div class="podrow" key={p.uid}>
                <div class="podname" title={p.name}>
                  {p.name}
                </div>
                <div class="podmetric">
                  <span class="mlbl">cpu {cpuPct(last(p.cpu))}</span>
                  <Sparkline data={p.cpu} color={C.warn} />
                </div>
                <div class="podmetric">
                  <span class="mlbl">mem {human(last(p.mem))}</span>
                  <Sparkline data={p.mem} color={C.cool} />
                </div>
              </div>
            ))}
          </div>
          <div class="heatmaps">
            <Heatmap series={series} metric="cpu" color={C.warn} label="CPU" format={cpuPct} />
            <Heatmap series={series} metric="mem" color={C.cool} label="memory" format={human} />
          </div>
        </>
      )}
    </div>
  );
}
