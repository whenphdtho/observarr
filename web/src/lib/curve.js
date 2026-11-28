// monotonePath joins points [[x, y], ...] (x strictly increasing) with monotone cubic
// interpolation in x (Fritsch–Carlson, the same idea as d3's curveMonotoneX): each point's tangent
// comes from the slopes of its neighbouring segments, weighted by their widths, and is limited so
// the curve never overshoots between two points. Each segment's Bézier control points sit at one
// third and two thirds of the segment's own width, so the path never runs backwards in time
// however unevenly the samples are spaced. It returns an SVG path ("M… C…"), or '' for fewer
// than two points. A pair of points is a straight segment.
export function monotonePath(pts) {
  const n = pts.length;
  if (n < 2) return '';
  const f = (v) => v.toFixed(1);
  let d = `M${f(pts[0][0])},${f(pts[0][1])}`;
  for (const [c1, c2, p] of segments(pts)) d += `C${f(c1[0])},${f(c1[1])} ${f(c2[0])},${f(c2[1])} ${f(p[0])},${f(p[1])}`;
  return d;
}

// monotoneBack is the same curve walked from the last point to the first, without the move to its start:
// it closes a band whose upper edge monotonePath drew left to right ("…L last" + monotoneBack(lower)).
export function monotoneBack(pts) {
  const n = pts.length;
  if (n < 2) return '';
  const f = (v) => v.toFixed(1);
  const segs = segments(pts);
  let d = '';
  for (let i = segs.length - 1; i >= 0; i--) {
    const [c1, c2] = segs[i];
    const p = pts[i];
    d += `C${f(c2[0])},${f(c2[1])} ${f(c1[0])},${f(c1[1])} ${f(p[0])},${f(p[1])}`;
  }
  return d;
}

// segments returns, for each pair of neighbouring points, the cubic's two control points and its end.
function segments(pts) {
  const n = pts.length;
  if (n === 2) return [[lerp(pts[0], pts[1], 1 / 3), lerp(pts[0], pts[1], 2 / 3), pts[1]]];

  const h = [];
  const s = [];
  for (let i = 0; i < n - 1; i++) {
    h[i] = pts[i + 1][0] - pts[i][0];
    s[i] = h[i] > 0 ? (pts[i + 1][1] - pts[i][1]) / h[i] : 0;
  }

  const t = new Array(n);
  for (let i = 1; i < n - 1; i++) {
    // Zero at a local extremum or a flat neighbour; otherwise a width-weighted harmonic mean
    // of the two slopes, which keeps the tangent between them (no overshoot).
    if (s[i - 1] * s[i] <= 0) {
      t[i] = 0;
    } else {
      const w1 = 2 * h[i] + h[i - 1];
      const w2 = h[i] + 2 * h[i - 1];
      t[i] = (w1 + w2) / (w1 / s[i - 1] + w2 / s[i]);
    }
  }
  t[0] = endTangent(h[0], h[1], s[0], s[1]);
  t[n - 1] = endTangent(h[n - 2], h[n - 3], s[n - 2], s[n - 3]);

  const out = [];
  for (let i = 0; i < n - 1; i++) {
    const [x0, y0] = pts[i];
    const [x1, y1] = pts[i + 1];
    const dx = h[i] / 3;
    out.push([[x0 + dx, y0 + t[i] * dx], [x1 - dx, y1 - t[i + 1] * dx], [x1, y1]]);
  }
  return out;
}

function lerp(a, b, k) {
  return [a[0] + (b[0] - a[0]) * k, a[1] + (b[1] - a[1]) * k];
}

// endTangent is the one-sided three-point estimate for an end point, limited the same way as the
// inner tangents: zero if it points against the end segment, at most three times its slope.
function endTangent(h0, h1, s0, s1) {
  if (!(h0 + h1 > 0)) return 0;
  const t = ((2 * h0 + h1) * s0 - h0 * s1) / (h0 + h1);
  if (Math.sign(t) !== Math.sign(s0)) return 0;
  if (Math.sign(s0) !== Math.sign(s1) && Math.abs(t) > Math.abs(3 * s0)) return 3 * s0;
  return t;
}
