// Phones: at most 600px wide, the page shows one view at a time (the agents,
// or one panel) with a navigation bar at the bottom (App, SidePanel).
import { useEffect, useState } from 'react';

const query = typeof matchMedia === 'undefined' ? null : matchMedia('(max-width: 600px)');

export const isNarrow = () => Boolean(query?.matches);

export function useNarrow() {
  const [narrow, setNarrow] = useState(isNarrow);
  useEffect(() => {
    if (!query) return;
    const change = () => setNarrow(query.matches);
    query.addEventListener('change', change);
    return () => query.removeEventListener('change', change);
  }, []);
  return narrow;
}
