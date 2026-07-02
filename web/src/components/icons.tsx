// Hand-drawn SVG icons (no emoji): sun/moon/auto for theme switching, plus the brand (candlestick) mark in the top-left corner.
// All use currentColor so they follow the text color/theme; size is 1em so they track fontSize.

type IconProps = { style?: React.CSSProperties; className?: string }

const base = (extra?: React.CSSProperties): React.SVGProps<SVGSVGElement> => ({
  viewBox: '0 0 24 24',
  width: '1em',
  height: '1em',
  focusable: false,
  'aria-hidden': true,
  style: { display: 'inline-block', verticalAlign: '-0.15em', ...extra },
})

// Light mode: sun
export function SunIcon({ style, className }: IconProps) {
  return (
    <svg {...base(style)} className={className} fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round">
      <circle cx="12" cy="12" r="4" />
      <path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" />
    </svg>
  )
}

// Dark mode: moon
export function MoonIcon({ style, className }: IconProps) {
  return (
    <svg {...base(style)} className={className} fill="currentColor">
      <path d="M21 12.8A9 9 0 1 1 11.2 3 7 7 0 0 0 21 12.8z" />
    </svg>
  )
}

// Follow system: half-filled circle (auto/contrast)
export function AutoIcon({ style, className }: IconProps) {
  return (
    <svg {...base(style)} className={className} fill="none" stroke="currentColor" strokeWidth={2}>
      <circle cx="12" cy="12" r="9" />
      <path d="M12 3a9 9 0 0 1 0 18z" fill="currentColor" stroke="none" />
    </svg>
  )
}

// Brand: candlestick / line chart (replaces 📈). Uses currentColor by default; pass style.color to apply a theme color.
export function BrandIcon({ style, className }: IconProps) {
  return (
    <svg
      {...base(style)}
      className={className}
      fill="none"
      stroke="currentColor"
      strokeWidth={2}
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      <path d="M4 4v16h16" />
      <path d="M7 14.5l3.5-4 3 3L20 7" />
      <circle cx="20" cy="7" r="1.4" fill="currentColor" stroke="none" />
    </svg>
  )
}
