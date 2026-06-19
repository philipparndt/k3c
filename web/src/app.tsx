import { useEffect, useRef, useState } from 'preact/hooks';
import type { State, Machine } from './types';
import { Stats } from './components/Stats';
import { Diagram } from './components/Diagram';
import { Legend } from './components/Legend';

export interface Flows {
  pulls: boolean; // image pulls happening (cache activity), within the hold window
  traffic: Record<string, boolean>; // machine name -> has live traffic (held briefly)
}

// FLOW_HOLD keeps a brief burst visible for a moment after it is measured, so a
// short pull does not flash for a single poll and vanish.
const FLOW_HOLD = 2500;

// TRAFFIC_FLOOR is the rate below which traffic is treated as idle background
// chatter (the docker dind VM is never truly silent), so an edge animates only
// on real activity, not constantly.
const TRAFFIC_FLOOR = 1024;

export function App() {
  const [state, setState] = useState<State | null>(null);
  const [live, setLive] = useState(true);
  const [busy, setBusy] = useState<Record<string, boolean>>({});
  const [flows, setFlows] = useState<Flows>({ pulls: false, traffic: {} });
  const spark = useRef<number[]>([]);
  const prevPulls = useRef<number | null>(null);
  const lastPullAt = useRef<number>(0);
  const lastTrafficAt = useRef<Record<string, number>>({});

  async function poll() {
    try {
      const r = await fetch('/api/state', { cache: 'no-store' });
      if (!r.ok) throw new Error('bad status');
      const s: State = await r.json();
      const now = Date.now();
      spark.current.push(s.net.hasRate ? s.net.rxRate : 0);
      if (spark.current.length > 40) spark.current.shift();

      // pulls: a rise in cache hits+misses means images are being pulled now.
      const total = s.cache && s.cache.enabled ? s.cache.hits + s.cache.misses : 0;
      if (prevPulls.current !== null && total > prevPulls.current) lastPullAt.current = now;
      prevPulls.current = total;
      const pulls = now - lastPullAt.current < FLOW_HOLD;

      // per-machine traffic: each VM's own measured rate drives its edges.
      const traffic: Record<string, boolean> = {};
      for (const m of s.machines) {
        if (m.hasRate && (m.rxRate > TRAFFIC_FLOOR || m.txRate > TRAFFIC_FLOOR)) {
          lastTrafficAt.current[m.name] = now;
        }
        traffic[m.name] = now - (lastTrafficAt.current[m.name] || 0) < FLOW_HOLD;
      }
      setFlows({ pulls, traffic });

      setState(s);
      setLive(true);
    } catch {
      setLive(false); // keep last good state on screen
    }
  }

  async function onAction(m: Machine, action: string) {
    setBusy((b) => ({ ...b, [m.name]: true }));
    try {
      await fetch('/api/action', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name: m.name, kind: m.kind, action }),
      });
    } catch {
      /* surfaced via the next poll */
    }
    await poll();
    setBusy((b) => ({ ...b, [m.name]: false }));
  }

  useEffect(() => {
    poll();
    const id = setInterval(poll, 1500);
    return () => clearInterval(id);
  }, []);

  return (
    <div class="wrap">
      <header>
        <span class="brand">k3c</span>
        <span class="sub">· system</span>
        <span class={'live' + (live ? '' : ' bad')}>
          <span class="pulse" />
          <span class="lbl">{live ? 'live' : 'reconnecting…'}</span>
        </span>
      </header>
      {state ? (
        <>
          <Stats state={state} spark={spark.current} />
          <Diagram state={state} flows={flows} busy={busy} onAction={onAction} />
          <Legend />
        </>
      ) : (
        <div class="note">connecting…</div>
      )}
    </div>
  );
}
