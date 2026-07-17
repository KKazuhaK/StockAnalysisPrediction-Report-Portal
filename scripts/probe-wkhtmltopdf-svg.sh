#!/bin/sh
set -eu

for tool in wkhtmltopdf pdftoppm; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "Required probe tool is missing: $tool" >&2
    exit 2
  fi
done

probe_dir=$(mktemp -d)
trap 'rm -rf "$probe_dir"' EXIT HUP INT TERM

cat >"$probe_dir/svg.html" <<'EOF'
<!doctype html>
<meta charset="utf-8">
<style>html,body{margin:0;padding:0}</style>
<svg xmlns="http://www.w3.org/2000/svg" width="420" height="220" viewBox="0 0 420 220">
  <rect x="20" y="20" width="380" height="180" fill="#f5f7fa" stroke="#185fa5" stroke-width="4"/>
  <path d="M40 160 L120 115 L200 140 L280 65 L380 90" fill="none" stroke="#d4380d" stroke-width="6"/>
  <text x="40" y="55" font-family="WenQuanYi Zen Hei" font-size="24" fill="#1a1a1a">SVG_PROBE 图表</text>
</svg>
EOF

cat >"$probe_dir/blank.html" <<'EOF'
<!doctype html>
<meta charset="utf-8">
<style>html,body{margin:0;padding:0}</style>
EOF

render_pdf() {
  input=$1
  output=$2
  wkhtmltopdf -q --encoding utf-8 \
    --disable-javascript --disable-plugins --no-images --disable-external-links \
    --disable-local-file-access \
    --margin-top 14mm --margin-bottom 16mm --margin-left 14mm --margin-right 14mm \
    - "$output" <"$input"
}

render_pdf "$probe_dir/svg.html" "$probe_dir/svg.pdf"
render_pdf "$probe_dir/blank.html" "$probe_dir/blank.pdf"

pdftoppm -singlefile -r 96 "$probe_dir/svg.pdf" "$probe_dir/svg" >/dev/null 2>&1
pdftoppm -singlefile -r 96 "$probe_dir/blank.pdf" "$probe_dir/blank" >/dev/null 2>&1

if cmp -s "$probe_dir/svg.ppm" "$probe_dir/blank.ppm"; then
  echo "Inline SVG probe failed: the rasterized page is identical to a blank page." >&2
  exit 1
fi

echo "Inline SVG probe passed under the production wkhtmltopdf flags."
