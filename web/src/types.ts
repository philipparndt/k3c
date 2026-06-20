export interface Listener {
  name: string;
  port: string;
  detail: string;
  up: boolean;
}

export interface Daemon {
  state: string;
  pid: string;
  listeners: Listener[];
}

export interface Machine {
  name: string;
  kind: string; // "" cluster, "docker" sidecar
  state: string;
  ram: string;
  context: string;
  active: boolean;
  rxRate: number;
  txRate: number;
  hasRate: boolean;
}

export interface Cache {
  enabled: boolean;
  hitPct: number;
  hits: number;
  misses: number;
  hitBytes: number;
  missBytes: number;
}

export interface Net {
  cluster: string;
  rxRate: number;
  txRate: number;
  hasRate: boolean;
}

export interface State {
  daemon: Daemon;
  machines: Machine[];
  cache: Cache;
  net: Net;
}

// PodSample is one pod's accounting at a single tick, mirroring cluster.PodSample.
// cpu_usec is cumulative since the pod started; rate is derived client-side.
export interface PodSample {
  name?: string;
  cpu_usec: number;
  mem_ws: number;
  mem_current: number;
}

// PodSnapshot is one sampling tick streamed from /api/pods/stream.
export interface PodSnapshot {
  t_ms: number;
  pods: Record<string, PodSample>; // keyed by pod UID
}

// PodSeries is the derived, render-ready history for one pod over the recent
// window: aligned CPU-rate (bytes? no — usec/s) and memory working-set samples.
export interface PodSeries {
  uid: string;
  name: string;
  cpu: number[]; // CPU rate, fraction of a core (usec-per-usec), per tick
  mem: number[]; // memory working set in bytes, per tick
}
