const svgCache = new Map()

function inlineSVGs() {
  document.querySelectorAll('img[src$=".svg"]:not([data-inlined])').forEach(img => {
    const url = img.src
    img.dataset.inlined = "true"

    if (svgCache.has(url)) {
      replaceWithSvg(img, svgCache.get(url))
      return
    }

    fetch(url)
      .then(res => res.text())
      .then(svgText => {
        replaceWithSvg(img, svgText)
        svgCache.set(url, svgText); // Store in cache
      })
      .catch(console.error)
  })
}

function replaceWithSvg(img, svgText) {
  // Parse safely (prevents XSS)
  const parser = new DOMParser()
  const svgDoc = parser.parseFromString(svgText, 'image/svg+xml')
  const svg = svgDoc.querySelector('svg')

  if (!svg) return

  svg.querySelectorAll('[fill]:not([fill="none"])').forEach(el => {
    el.setAttribute('fill', 'currentColor');
  })

  img.replaceWith(svg)
}
inlineSVGs()
