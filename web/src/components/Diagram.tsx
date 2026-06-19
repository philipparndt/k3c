import type { State, Machine } from '../types';
import type { Flows } from '../app';
import { Node, DaemonNode, CacheNode, VmNode } from './Node';
import { Edges } from './Edges';

export function Diagram(props: {
  state: State;
  flows: Flows;
  busy: Record<string, boolean>;
  onAction: (m: Machine, action: string) => void;
}) {
  const { state, flows, busy, onAction } = props;
  const machines = state.machines || [];
  const runtimeState = machines.some((m) => m.state === 'running')
    ? 'running'
    : machines.length
      ? 'stopped'
      : 'unknown';
  const hasCache = (state.daemon.listeners || []).some((l) => l.name === 'pull-cache');
  const sig = machines.map((m) => m.name + ':' + m.kind).join('|') + '|' + (hasCache ? 'c' : '');

  return (
    <div class="stageScroll">
      <div class="stage" id="stage">
        <Edges sig={sig} machineNames={machines.map((m) => m.name)} flows={flows} />
        {/* the egress spine (internet -> daemon -> runtime -> VMs) runs straight
            up the middle; the pull-cache sits off to the side so the pull path
            is distinct and no egress line crosses behind it */}
        <div class="layers">
          <div class="body">
            <div class="spine">
              <div class="row">
                {/* external boundary — not a k3c component, so no state color */}
                <div id="n-internet" class="node external">
                  <div class="title">
                    <span class="globe">☁</span>
                    <span>internet</span>
                  </div>
                  <div class="meta">reached via the host · DNS · CA trust · corporate VPN</div>
                </div>
              </div>
              <div class="row">
                <DaemonNode daemon={state.daemon} />
              </div>
              <div class="row">
                <Node id="n-runtime" state={runtimeState} title="container runtime">
                  <div class="meta">apple Virtualization.framework</div>
                </Node>
              </div>
              <div class="row vms">
                {machines.map((m) => (
                  <VmNode key={m.name} m={m} busy={!!busy[m.name]} onAction={onAction} />
                ))}
              </div>
            </div>
            {hasCache && (
              <div class="side">
                <CacheNode cache={state.cache} />
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
