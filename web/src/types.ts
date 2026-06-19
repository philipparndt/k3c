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
