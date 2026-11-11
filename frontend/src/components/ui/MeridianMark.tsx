// Meridian's mark: lines of code split by a meridian, with the right side
// dropped half a line. The full mark has seven lines; the compact mark keeps
// four heavier lines so it stays legible at icon and favicon sizes.
// Primary shapes use currentColor and the accent uses .meridian-mark-accent,
// so the tile and its colors are set in CSS.

type Bar = readonly [x: number, y: number, width: number];

const marks = {
  full: {
    height: 6,
    stroke: 3,
    meridian: [10, 90],
    left: [
      [14, 14.5, 31.5],
      [22, 24.5, 23.5],
      [22, 34.5, 23.5],
      [30, 44.5, 15.5],
      [30, 54.5, 15.5],
      [22, 64.5, 23.5],
      [14, 74.5, 31.5],
    ],
    right: [
      [54.5, 19.5, 15.5],
      [54.5, 29.5, 27.5],
      [54.5, 39.5, 9.5],
      [54.5, 49.5, 31.5],
      [54.5, 59.5, 6],
      [54.5, 69.5, 21.5],
      [54.5, 79.5, 11.5],
    ],
  },
  compact: {
    height: 10,
    stroke: 4,
    meridian: [10, 90],
    left: [
      [14, 15, 30],
      [24, 32, 20],
      [24, 49, 20],
      [14, 66, 30],
    ],
    right: [
      [56, 24, 16],
      [56, 41, 30],
      [56, 58, 10],
      [56, 75, 22],
    ],
  },
} satisfies Record<
  string,
  { height: number; stroke: number; meridian: readonly [number, number]; left: Bar[]; right: Bar[] }
>;

export function MeridianMark({
  variant = "compact",
  className,
}: {
  variant?: keyof typeof marks;
  className?: string;
}) {
  const mark = marks[variant];
  const bar = ([x, y, width]: Bar) => (
    <rect key={`${x}-${y}`} x={x} y={y} width={width} height={mark.height} rx={mark.height / 2} />
  );
  return (
    <svg className={className} viewBox="0 0 100 100" aria-hidden="true" focusable="false">
      <g fill="currentColor" opacity="0.5">
        {mark.left.map(bar)}
      </g>
      <g className="meridian-mark-accent">{mark.right.map(bar)}</g>
      <line
        x1="50"
        y1={mark.meridian[0]}
        x2="50"
        y2={mark.meridian[1]}
        stroke="currentColor"
        strokeWidth={mark.stroke}
        strokeLinecap="round"
      />
    </svg>
  );
}
