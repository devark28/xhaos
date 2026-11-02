// A tall column scrolls until its bottom reaches the viewport bottom.
// A short column stays at the viewport top. CSS handles all scroll movement.
export function watchStickyColumns(scroller, columns, Observer = globalThis.ResizeObserver) {
  const measure = () => {
    const offsets = columns.map(column => Math.min(0, scroller.clientHeight - 16 - column.offsetHeight));
    columns.forEach((column, index) => column.style.setProperty('--column-sticky-top', `${offsets[index]}px`));
  };
  measure();
  if (!Observer) return () => {};
  const observer = new Observer(measure);
  observer.observe(scroller);
  columns.forEach(column => observer.observe(column));
  return () => observer.disconnect();
}
