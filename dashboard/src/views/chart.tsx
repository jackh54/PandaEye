// Peak players per day: a single-series column chart in inline SVG. Colours come from CSS custom
// properties (classes only, no inline styles, so the CSP holds); every column has a native <title> tooltip
// on a full-height hit target, and the page offers the same data as a table.

export interface DayPoint {
  day: string;
  peakPlayers: number;
}

function niceStep(max: number, ticks: number): number {
  const raw = Math.max(1, max) / ticks;
  const mag = 10 ** Math.floor(Math.log10(raw));
  const norm = raw / mag;
  const step = norm <= 1 ? 1 : norm <= 2 ? 2 : norm <= 5 ? 5 : 10;
  return Math.max(1, step * mag);
}

function columnPath(x: number, y: number, w: number, base: number): string {
  const h = base - y;
  if (h <= 0) return "";
  const r = Math.min(4, w / 2, h);
  return `M${x.toFixed(1)},${base.toFixed(1)}V${(y + r).toFixed(1)}Q${x.toFixed(1)},${y.toFixed(1)} ${(x + r).toFixed(1)},${y.toFixed(1)}H${(x + w - r).toFixed(1)}Q${(x + w).toFixed(1)},${y.toFixed(1)} ${(x + w).toFixed(1)},${(y + r).toFixed(1)}V${base.toFixed(1)}Z`;
}

export function PeakPlayersChart(props: { days: DayPoint[]; limit: number | null; title: string }) {
  const W = 720;
  const H = 260;
  const m = { top: 20, right: 16, bottom: 30, left: 44 };
  const pw = W - m.left - m.right;
  const ph = H - m.top - m.bottom;
  const days = props.days;
  const maxVal = Math.max(1, ...days.map((d) => d.peakPlayers), props.limit ?? 0);
  const step = niceStep(maxVal * 1.1, 4);
  const top = Math.ceil((maxVal * 1.1) / step) * step;
  const y = (v: number) => m.top + ph - (v / top) * ph;
  const base = m.top + ph;
  const n = Math.max(days.length, 1);
  const band = pw / n;
  const barW = Math.max(1, Math.min(24, band - 2));
  const ticks: number[] = [];
  for (let v = 0; v <= top + 1e-9; v += step) ticks.push(v);
  const labelIdx = days.length <= 1 ? [0] : [0, Math.floor((days.length - 1) / 2), days.length - 1];
  const peak = days.reduce((a, d) => Math.max(a, d.peakPlayers), 0);

  return (
    <figure class="chart">
      <svg viewBox={`0 0 ${W} ${H}`} role="img" aria-labelledby="usage-chart-title usage-chart-desc" class="chart-svg" preserveAspectRatio="xMidYMid meet">
        <title id="usage-chart-title">{props.title}</title>
        <desc id="usage-chart-desc">
          {days.length
            ? `${days.length} days from ${days[0]!.day} to ${days[days.length - 1]!.day}; highest daily peak ${peak} players${
                props.limit ? `; free tier limit ${props.limit}` : ""
              }.`
            : "No usage recorded yet."}
        </desc>
        <g class="chart-grid">
          {ticks.map((t) => (
            <line x1={m.left} x2={W - m.right} y1={y(t)} y2={y(t)} />
          ))}
        </g>
        <g class="chart-axis-labels">
          {ticks.map((t) => (
            <text x={m.left - 8} y={y(t) + 4} text-anchor="end">
              {String(t)}
            </text>
          ))}
          {days.length
            ? labelIdx.map((i, k) => (
                <text
                  x={m.left + band * i + band / 2}
                  y={H - 8}
                  text-anchor={k === 0 && labelIdx.length > 1 ? "start" : k === labelIdx.length - 1 && labelIdx.length > 1 ? "end" : "middle"}
                >
                  {days[i]!.day}
                </text>
              ))
            : null}
        </g>
        <g class="chart-series">
          {days.map((d, i) => (
            <path d={columnPath(m.left + band * i + (band - barW) / 2, y(d.peakPlayers), barW, base)} />
          ))}
        </g>
        <line class="chart-baseline" x1={m.left} x2={W - m.right} y1={base} y2={base} />
        {props.limit ? (
          <g class="chart-limit">
            <line x1={m.left} x2={W - m.right} y1={y(props.limit)} y2={y(props.limit)} stroke-dasharray="6 4" />
            <text x={W - m.right} y={y(props.limit) - 6} text-anchor="end">
              {`Free tier limit: ${props.limit}`}
            </text>
          </g>
        ) : null}
        <g class="chart-hits">
          {days.map((d, i) => (
            <rect x={m.left + band * i} y={m.top} width={band} height={ph}>
              <title>{`${d.day}: ${d.peakPlayers} peak players`}</title>
            </rect>
          ))}
        </g>
        {!days.length ? (
          <text x={W / 2} y={H / 2} text-anchor="middle" class="chart-empty">
            No usage recorded yet
          </text>
        ) : null}
      </svg>
    </figure>
  );
}
