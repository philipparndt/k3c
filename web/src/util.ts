export const C = {
  good: '#42C883',
  warn: '#E2C04A',
  cool: '#56B2F2',
  dim: '#6b6b7c',
  bad: '#F2637E',
  accent: '#7D79F6',
};

export function stateColor(s: string): string {
  return s === 'running' ? C.good : s === 'paused' ? C.warn : s === 'suspended' ? C.cool : C.dim;
}

export function dotClass(s: string): string {
  return s === 'running' ? 'd-good' : s === 'paused' ? 'd-warn' : s === 'suspended' ? 'd-cool' : 'd-dim';
}

export function human(b: number): string {
  b = +b || 0;
  return b >= 1e9
    ? (b / 1e9).toFixed(1) + ' GB'
    : b >= 1e6
      ? (b / 1e6).toFixed(1) + ' MB'
      : b >= 1e3
        ? (b / 1e3).toFixed(1) + ' kB'
        : b.toFixed(0) + ' B';
}
