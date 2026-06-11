/*
 * LogoMark is the agent-smith brand mark: two narrow, slightly-trapezoidal
 * lenses with a single thin Matrix-green streak at the bottom. Geometry mirrors
 * web/public/favicon.svg. The lenses use `currentColor` so the mark inherits the
 * surrounding text color (instead of the favicon's prefers-color-scheme rule,
 * which would render dark lenses invisibly on the always-dark sidebar); the
 * accent stays Matrix-green.
 */
export function LogoMark({ size = 20, className }: { size?: number; className?: string }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 32 32"
      role="img"
      aria-label="agent-smith logo"
      className={className}
      fill="none"
    >
      <polygon points="3,12 15,12 14,20 4,19" fill="currentColor" />
      <polygon points="17,12 29,12 28,19 18,20" fill="currentColor" />
      <rect x="13.5" y="12" width="5" height="1.6" fill="currentColor" />
      <line x1="4.5" y1="18.4" x2="13.5" y2="19" stroke="#00ff7a" strokeWidth="0.6" strokeLinecap="round" />
      <line x1="18.5" y1="19" x2="27.5" y2="18.4" stroke="#00ff7a" strokeWidth="0.6" strokeLinecap="round" />
    </svg>
  );
}
