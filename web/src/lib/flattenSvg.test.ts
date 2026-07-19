import { describe, expect, it } from 'vitest'
import { flattenMermaidSVG } from './flattenSvg'

describe('flattenMermaidSVG', () => {
  it('turns computed presentation into URL-free native SVG geometry', () => {
    const host = document.createElement('div')
    host.innerHTML = `<svg xmlns="http://www.w3.org/2000/svg" width="100%" viewBox="0 0 100 50" onload="bad()">
      <style>.node { fill: rgb(240, 240, 255); stroke: rgb(80, 60, 160); }</style>
      <defs>
        <filter id="shadow"><feDropShadow stdDeviation="2"/></filter>
        <marker id="arrow"><path d="M 0 0 L 10 5 L 0 10 z"/></marker>
      </defs>
      <path id="edge" d="M 10 10 L 90 40" marker-end="url(#arrow)" style="stroke: rgb(80, 60, 160)"/>
      <rect class="node" style="fill: rgb(240, 240, 255); stroke: rgb(80, 60, 160)" data-id="a" x="1" y="1" width="98" height="48"/>
      <text class="node" x="10" y="25">中文</text>
      <foreignObject><div>unsafe</div></foreignObject>
    </svg>`
    document.body.appendChild(host)
    const edge = host.querySelector('#edge') as SVGPathElement
    Object.defineProperty(edge, 'getTotalLength', { value: () => 100 })
    Object.defineProperty(edge, 'getPointAtLength', {
      value: (distance: number) => ({ x: 10 + distance * 0.8, y: 10 + distance * 0.3 }),
    })

    const flattened = flattenMermaidSVG(host.querySelector('svg') as SVGSVGElement)

    expect(flattened).toContain('fill="rgb(240, 240, 255)"')
    expect(flattened).toContain('stroke="rgb(80, 60, 160)"')
    expect(flattened).toContain('中文')
    expect(flattened).toContain('width="100"')
    expect(flattened).toContain('height="50"')
    expect(flattened).not.toContain('<style')
    expect(flattened).not.toContain('<filter')
    expect(flattened).not.toContain('feDropShadow')
    expect(flattened).not.toContain('<marker')
    expect(flattened).not.toContain('<defs')
    expect(flattened).not.toContain('marker-end')
    expect(flattened).toContain('points="-5,-4 5,0 -5,4"')
    expect(flattened).not.toContain('class=')
    expect(flattened).not.toContain('foreignObject')
    expect(flattened).not.toContain('onload')
    expect(flattened).not.toContain('data-id')
    host.remove()
  })
})
