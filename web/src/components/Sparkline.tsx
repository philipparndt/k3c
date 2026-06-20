// Sparkline draws a compact filled line of recent values, scaled to its own max
// so a pod's trend is legible regardless of absolute magnitude.
export function Sparkline({ data, color, w = 120, h = 22 }: { data: number[]; color: string; w?: number; h?: number }) {
  const n = data.length;
  if (n < 2) return <svg class="pspark" viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none" />;
  const max = Math.max(1e-9, ...data);
  const pts = data.map((v, i) => `${i * (w / (n - 1))},${h - 1 - (v / max) * (h - 2)}`).join(' ');
  return (
    <svg class="pspark" viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none">
      <polyline fill="none" stroke={color} stroke-width="1.5" points={pts} />
      <polyline fill={color + '22'} points={`0,${h} ${pts} ${w},${h}`} />
    </svg>
  );
}
