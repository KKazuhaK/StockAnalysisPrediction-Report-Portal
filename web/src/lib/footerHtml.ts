const FOOTER_TAGS = new Set(['A', 'B', 'BR', 'CODE', 'EM', 'I', 'SMALL', 'SPAN', 'STRONG', 'U'])
const FOOTER_ATTRS: Record<string, Set<string>> = {
  A: new Set(['href', 'target', 'rel', 'title']),
  '*': new Set(['title']),
}
const DROP_WITH_CONTENT = new Set(['SCRIPT', 'STYLE', 'TEMPLATE'])

export function sanitizeFooterHtml(raw: string): string {
  if (!raw.trim()) return ''
  if (typeof DOMParser === 'undefined') return ''
  const doc = new DOMParser().parseFromString(raw, 'text/html')

  const cleanElement = (el: Element) => {
    for (const child of Array.from(el.children)) {
      const tag = child.tagName.toUpperCase()
      if (!FOOTER_TAGS.has(tag)) {
        if (DROP_WITH_CONTENT.has(tag)) {
          child.remove()
        } else {
          cleanElement(child)
          child.replaceWith(...Array.from(child.childNodes))
        }
        continue
      }

      for (const attr of Array.from(child.attributes)) {
        const name = attr.name.toLowerCase()
        const allowed = FOOTER_ATTRS[tag]?.has(name) || FOOTER_ATTRS['*'].has(name)
        if (!allowed || name.startsWith('on')) {
          child.removeAttribute(attr.name)
          continue
        }
        if (tag === 'A' && name === 'href' && !safeHref(attr.value.trim())) {
          child.removeAttribute(attr.name)
        }
      }

      if (tag === 'A') {
        const target = child.getAttribute('target')
        if (target && !['_blank', '_self'].includes(target)) child.removeAttribute('target')
        if (child.getAttribute('target') === '_blank') child.setAttribute('rel', 'noopener noreferrer')
      }

      cleanElement(child)
    }
  }

  cleanElement(doc.body)
  return doc.body.innerHTML
}

function safeHref(href: string): boolean {
  if (!href) return false
  if (href.startsWith('/')) return true
  try {
    const u = new URL(href, window.location.origin)
    return ['http:', 'https:', 'mailto:'].includes(u.protocol)
  } catch {
    return false
  }
}
