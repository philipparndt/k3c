import type { State } from '../types';
import { C, human } from '../util';

function Spark({ data }: { data: number[] }) {
  const n = data.length;
  if (n < 2) return <svg class="spark" viewBox="0 0 160 22" preserveAspectRatio="none" />;
  const max = Math.max(1, ...data);
  const pts = data.map((v, i) => `${i * (160 / (n - 1))},${21 - (v / max) * 19}`).join(' ');
  return (
    <svg class="spark" viewBox="0 0 160 22" preserveAspectRatio="none">
      <polyline fill="none" stroke={C.cool} stroke-width="1.5" points={pts} />
      <polyline fill={C.cool + '22'} points={`0,22 ${pts} 160,22`} />
    </svg>
  );
}

export function Stats({ state, spark }: { state: State; spark: number[] }) {
  const net = state.net;
  const cache = state.cache;
  const running = state.machines.filter((m) => m.state === 'running').length;
  const paused = state.machines.filter((m) => m.state === 'paused').length;
  return (
    <div class="stats">
      <div class="stat">
        <div class="k">net · {net.cluster || '—'}</div>
        <div class="v">
          {net.hasRate ? `↓ ${human(net.rxRate)}/s  ↑ ${human(net.txRate)}/s` : 'idle'}
        </div>
        <Spark data={spark} />
      </div>
      <div class="stat">
        <div class="k">pull-cache</div>
        <div class="v">
          {cache && cache.enabled ? (
            <>
              {cache.hitPct}% hits <small>· {human(cache.hitBytes)}</small>
            </>
          ) : (
            <small>disabled</small>
          )}
        </div>
      </div>
      <div class="stat">
        <div class="k">machines</div>
        <div class="v">
          {running} running <small>· {paused} paused</small>
        </div>
      </div>
      <div class="stat">
        <div class="k">daemon</div>
        <div class="v">
          {state.daemon.state} <small>· pid {state.daemon.pid}</small>
        </div>
      </div>
    </div>
  );
}
