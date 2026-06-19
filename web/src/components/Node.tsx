import type { ComponentChildren } from 'preact';
import type { Daemon, Cache, Machine } from '../types';
import { stateColor, dotClass, human } from '../util';

function frame(state: string) {
  const col = stateColor(state);
  return { borderColor: col + '66', boxShadow: `0 0 0 1px ${col}22, 0 8px 26px -12px ${col}66` };
}

export function Node(props: { id?: string; state: string; title: string; children?: ComponentChildren }) {
  const col = stateColor(props.state);
  return (
    <div id={props.id} class="node" style={frame(props.state)}>
      <div class="title">
        <span class={'dot ' + dotClass(props.state)} />
        <span style={{ color: col }}>{props.title}</span>
      </div>
      {props.children}
    </div>
  );
}

export function DaemonNode({ daemon }: { daemon: Daemon }) {
  return (
    <Node id="n-daemon" state={daemon.state} title="host daemon">
      <div class="meta">
        {daemon.state} · pid {daemon.pid}
      </div>
      <div class="listeners">
        {(daemon.listeners || []).map((l) => (
          <div class={'lrow' + (l.up ? '' : ' down')} key={l.name + l.port}>
            <span class={'dot ' + (l.up ? 'd-good' : 'd-bad')} />
            <span class="lname">{l.name}</span>
            <span class="lport">:{l.port}</span>
            <span class="ldet">
              {l.detail}
              {l.up ? '' : ' · down'}
            </span>
          </div>
        ))}
      </div>
    </Node>
  );
}

export function CacheNode({ cache }: { cache: Cache }) {
  const enabled = cache && cache.enabled;
  const state = enabled ? 'running' : 'stopped';
  return (
    <Node id="n-cache" state={state} title="pull-cache">
      <div class="meta">
        {enabled
          ? `${cache.hitPct}% hits · cache ${human(cache.hitBytes)} · up ${human(cache.missBytes)}`
          : 'no pulls yet'}
      </div>
    </Node>
  );
}

export function VmNode(props: {
  m: Machine;
  busy: boolean;
  onAction: (m: Machine, action: string) => void;
}) {
  const { m, busy, onAction } = props;
  const kind = m.kind === 'docker' ? 'docker sidecar' : 'k3s';
  const running = m.state === 'running';
  const halted = m.state === 'paused' || m.state === 'suspended';
  const stopped = m.state === 'stopped';
  const detail: string[] = [];
  if (m.ram) detail.push(`mem ${m.ram}`);
  if (m.kind !== 'docker' && m.context) detail.push(`ctx ${m.context}`);
  return (
    <Node id={'vm-' + m.name} state={m.state} title={m.name}>
      <div class="meta">
        {kind} · {m.state}
      </div>
      {detail.length > 0 && <div class="meta">{detail.join(' · ')}</div>}
      <div class="actions">
        <button
          class="act"
          disabled={running || busy}
          title={halted ? 'resume' : 'start'}
          onClick={() => onAction(m, halted ? 'resume' : 'start')}
        >
          ▶
        </button>
        <button class="act" disabled={!running || busy} title="pause" onClick={() => onAction(m, 'pause')}>
          ⏸
        </button>
        <button class="act" disabled={stopped || busy} title="stop" onClick={() => onAction(m, 'stop')}>
          ⏹
        </button>
        {busy && <span class="spin">working…</span>}
      </div>
    </Node>
  );
}
