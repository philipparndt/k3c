export function Legend() {
  return (
    <>
      <div class="legend">
        <span><span class="swatch" style={{ background: 'var(--good)' }} /><b>running</b></span>
        <span><span class="swatch" style={{ background: 'var(--warn)' }} /><b>paused</b></span>
        <span><span class="swatch" style={{ background: 'var(--cool)' }} /><b>suspended</b></span>
        <span><span class="swatch" style={{ background: 'var(--dim)' }} /><b>stopped</b></span>
        <span><span class="swatch" style={{ background: 'var(--bad)' }} /><b>listener down</b></span>
        <span style={{ marginLeft: '6px' }}><span class="flowkey" style={{ borderColor: 'var(--cool)' }} /><b>egress</b></span>
        <span><span class="flowkey" style={{ borderColor: 'var(--accent)' }} /><b>image pulls</b></span>
        <span><span class="flowkey" style={{ borderColor: 'var(--good)' }} /><b>hosts / traffic</b></span>
      </div>
      <div class="note">
        <b>k3c web</b> — live system diagram. Particles flow along each edge to show data direction; nodes &amp;
        frames are colored by state. Use ▶ ⏸ ⏹ on a machine to start, pause, or stop it.
      </div>
    </>
  );
}
