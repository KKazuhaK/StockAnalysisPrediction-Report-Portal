const SVG_NAMESPACE = 'http://www.w3.org/2000/svg'

const presentationAttributes: Array<[string, string]> = [
  ['fill', 'fill'],
  ['fill-opacity', 'fill-opacity'],
  ['fill-rule', 'fill-rule'],
  ['stroke', 'stroke'],
  ['stroke-width', 'stroke-width'],
  ['stroke-opacity', 'stroke-opacity'],
  ['stroke-linecap', 'stroke-linecap'],
  ['stroke-linejoin', 'stroke-linejoin'],
  ['stroke-miterlimit', 'stroke-miterlimit'],
  ['stroke-dasharray', 'stroke-dasharray'],
  ['stroke-dashoffset', 'stroke-dashoffset'],
  ['opacity', 'opacity'],
  ['color', 'color'],
  ['font-family', 'font-family'],
  ['font-size', 'font-size'],
  ['font-weight', 'font-weight'],
  ['font-style', 'font-style'],
  ['text-anchor', 'text-anchor'],
  ['dominant-baseline', 'dominant-baseline'],
  ['alignment-baseline', 'alignment-baseline'],
]

const removableElements = new Set([
  'style',
  'script',
  'foreignobject',
  'filter',
  'fedropshadow',
  'fegaussianblur',
  'feoffset',
  'femerge',
  'femergenode',
])

function normalizedPresentationValue(name: string, value: string): string | null {
  const trimmed = value.trim()
  if (!trimmed) return null
  if ((name === 'fill' || name === 'stroke') && trimmed.toLowerCase().startsWith('url(')) return null
  return trimmed
}

interface SVGGeometryLike extends SVGElement {
  getTotalLength?: () => number
  getPointAtLength?: (distance: number) => { x: number; y: number }
}

function appendArrowhead(
  original: SVGGeometryLike,
  copy: SVGElement,
  atStart: boolean,
  color: string,
) {
  if (!original.getTotalLength || !original.getPointAtLength || !copy.parentNode) return
  try {
    const length = original.getTotalLength()
    if (!Number.isFinite(length) || length <= 0) return
    const endpoint = original.getPointAtLength(atStart ? 0 : length)
    const adjacent = original.getPointAtLength(atStart ? Math.min(2, length) : Math.max(0, length - 2))
    const angle =
      (Math.atan2(endpoint.y - adjacent.y, endpoint.x - adjacent.x) * 180) / Math.PI +
      (atStart ? 180 : 0)
    if (![endpoint.x, endpoint.y, adjacent.x, adjacent.y, angle].every(Number.isFinite)) return
    const arrow = copy.ownerDocument.createElementNS(SVG_NAMESPACE, 'polygon')
    arrow.setAttribute('points', '-5,-4 5,0 -5,4')
    arrow.setAttribute('transform', `translate(${endpoint.x} ${endpoint.y}) rotate(${angle})`)
    arrow.setAttribute('fill', color)
    arrow.setAttribute('stroke', color)
    copy.parentNode.insertBefore(arrow, copy.nextSibling)
  } catch {
    // A malformed geometry implementation must not block the safe SVG fallback.
  }
}

// Mermaid relies heavily on a generated <style> block and classes. wkhtmltopdf receives
// charts after that style block has been removed by the SVG security boundary, so copy the
// browser's resolved presentation values onto geometry as plain SVG attributes first.
export function flattenMermaidSVG(svg: SVGSVGElement): string {
  const clone = svg.cloneNode(true) as SVGSVGElement
  const originals = [svg, ...Array.from(svg.querySelectorAll<SVGElement>('*'))]
  const copies = [clone, ...Array.from(clone.querySelectorAll<SVGElement>('*'))]

  originals.forEach((element, index) => {
    const copy = copies[index]
    if (!copy || removableElements.has(element.localName.toLowerCase())) return
    const computed = window.getComputedStyle(element)
    for (const [property, attribute] of presentationAttributes) {
      const value = normalizedPresentationValue(attribute, computed.getPropertyValue(property))
      if (value) copy.setAttribute(attribute, value)
    }
    const arrowColor = computed.getPropertyValue('stroke').trim() || computed.getPropertyValue('fill').trim()
    if (element.hasAttribute('marker-start')) {
      appendArrowhead(element, copy, true, arrowColor)
    }
    if (element.hasAttribute('marker-end')) {
      appendArrowhead(element, copy, false, arrowColor)
    }
    copy.removeAttribute('marker-start')
    copy.removeAttribute('marker-mid')
    copy.removeAttribute('marker-end')
    copy.removeAttribute('clip-path')
    copy.removeAttribute('class')
    copy.removeAttribute('style')
    for (const attribute of Array.from(copy.attributes)) {
      if (attribute.name.startsWith('data-') || attribute.name.startsWith('aria-') || attribute.name === 'role') {
        copy.removeAttribute(attribute.name)
      }
      if (
        attribute.name.startsWith('on') ||
        attribute.name === 'href' ||
        attribute.name === 'xlink:href' ||
        attribute.name.includes(':')
      ) {
        copy.removeAttribute(attribute.name)
      }
      if (
        (attribute.name === 'fill' || attribute.name === 'stroke') &&
        attribute.value.toLowerCase().includes('url(')
      ) {
        copy.removeAttribute(attribute.name)
      }
    }
  })

  clone
    .querySelectorAll(
      'style, script, foreignObject, defs, filter, feDropShadow, feGaussianBlur, feOffset, feMerge, feMergeNode, marker, clipPath, linearGradient, radialGradient, stop',
    )
    .forEach((element) => element.remove())
  const viewBox = clone
    .getAttribute('viewBox')
    ?.trim()
    .split(/[\s,]+/)
    .map(Number)
  if (viewBox?.length === 4 && viewBox.every(Number.isFinite) && viewBox[2] > 0 && viewBox[3] > 0) {
    clone.setAttribute('width', String(viewBox[2]))
    clone.setAttribute('height', String(viewBox[3]))
  }
  clone.setAttribute('xmlns', SVG_NAMESPACE)
  clone.removeAttribute('style')
  clone.removeAttribute('class')
  return new XMLSerializer().serializeToString(clone)
}
