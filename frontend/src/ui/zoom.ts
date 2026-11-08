// The text size scales the page with CSS zoom. getBoundingClientRect gives
// screen pixels, while sizes set in CSS on an element are in its own, zoomed
// pixels: divide a measured length by zoomOf(el) before using it as a style.
export function zoomOf(el: HTMLElement): number {
  const w = el.offsetWidth;
  return w ? el.getBoundingClientRect().width / w || 1 : 1;
}
