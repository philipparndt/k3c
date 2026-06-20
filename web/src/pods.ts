import { useEffect, useRef, useState } from 'preact/hooks';
import type { PodSnapshot, PodSeries } from './types';

// WINDOW is how many recent ticks the sparklines and heatmaps render.
const WINDOW = 60;

interface PodHist {
  name: string;
  prevCpu: number; // last cumulative cpu_usec, for rate derivation
  prevT: number; // last tick timestamp (ms)
  cpu: number[]; // derived CPU rate per tick (fraction of a core)
  mem: number[]; // memory working set per tick (bytes)
}

function push(arr: number[], v: number) {
  arr.push(v);
  if (arr.length > WINDOW) arr.shift();
}

// usePodStream subscribes to /api/pods/stream for a cluster over SSE, keeps a
// bounded per-pod ring buffer of recent ticks, and derives each pod's CPU rate
// from the cumulative cpu_usec delta (skipping intervals where the counter
// decreased — a pod restart). It reconnects automatically when a cluster is
// selected; passing null tears the stream down.
export function usePodStream(cluster: string | null): { series: PodSeries[]; connected: boolean } {
  const [series, setSeries] = useState<PodSeries[]>([]);
  const [connected, setConnected] = useState(false);
  const hist = useRef<Map<string, PodHist>>(new Map());

  useEffect(() => {
    hist.current = new Map();
    setSeries([]);
    setConnected(false);
    if (!cluster) return;

    const es = new EventSource('/api/pods/stream?cluster=' + encodeURIComponent(cluster));
    es.onopen = () => setConnected(true);
    es.onerror = () => setConnected(false); // EventSource retries on its own
    es.onmessage = (ev) => {
      let snap: PodSnapshot;
      try {
        snap = JSON.parse(ev.data);
      } catch {
        return;
      }
      const h = hist.current;
      const live = new Set<string>();
      for (const uid in snap.pods) {
        live.add(uid);
        const s = snap.pods[uid];
        let p = h.get(uid);
        if (!p) {
          p = { name: s.name || uid, prevCpu: s.cpu_usec, prevT: snap.t_ms, cpu: [], mem: [] };
          h.set(uid, p);
        }
        p.name = s.name || p.name;
        // CPU rate = Δcpu_usec / Δt (both in µs) → fraction of one core.
        const dt = snap.t_ms - p.prevT;
        let rate = 0;
        if (dt > 0 && s.cpu_usec >= p.prevCpu) rate = (s.cpu_usec - p.prevCpu) / (dt * 1000);
        p.prevCpu = s.cpu_usec;
        p.prevT = snap.t_ms;
        push(p.cpu, rate);
        push(p.mem, s.mem_ws);
      }
      // Drop pods that vanished from the stream (terminated).
      for (const uid of [...h.keys()]) if (!live.has(uid)) h.delete(uid);

      setSeries(
        [...h.entries()].map(([uid, p]) => ({ uid, name: p.name, cpu: [...p.cpu], mem: [...p.mem] })),
      );
    };

    return () => es.close();
  }, [cluster]);

  return { series, connected };
}
