// SVG path markup for the UI's line icons (24×24 viewBox, stroked).
export const iconPaths = {
  grid: '<rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="3" width="7" height="7" rx="1"/><rect x="3" y="14" width="7" height="7" rx="1"/><rect x="14" y="14" width="7" height="7" rx="1"/>',
  list: '<path d="M4 5h16M4 12h16M4 19h16"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  arrow: '<path d="m9 5 7 7-7 7"/>',
  send: '<path d="m4 12 16-8-6 16-3-7-7-1Z"/>',
  mic: '<rect x="9" y="2" width="6" height="13" rx="3"/><path d="M5 10v2a7 7 0 0 0 14 0v-2M12 19v3m-4 0h8"/>',
  search: '<circle cx="10" cy="10" r="6"/><path d="m15 15 5 5"/>',
  plug: '<path d="M8 2v5m8-5v5M6 7h12v4a6 6 0 0 1-12 0V7Zm6 10v5"/>',
  chat: '<path d="M4 4h16v13H9l-5 4V4Z"/>',
  // A side answer (/btw): a speech bubble with an aside mark.
  aside: '<path d="M4 4h16v13H9l-5 4V4Z"/><path d="M9 9h6M9 12.5h3.5"/>',
  // A board: a sheet with a pen stroke across it.
  board: '<rect x="3" y="4" width="18" height="14" rx="1.5"/><path d="M7 14c2-3 3.5 1 5.5-1.5S16 10 17 9"/><path d="M8 21h8"/>',
  memory: '<path d="M9 3a3 3 0 0 0-3 3 3 3 0 0 0-2 5 3 3 0 0 0 2 5 3 3 0 0 0 3 3m6-16a3 3 0 0 1 3 3 3 3 0 0 1 2 5 3 3 0 0 1-2 5 3 3 0 0 1-3 3M9 3v18m6-18v18M9 8h6m-6 8h6"/>',
  settings: '<path d="M9.2 3.5 9.8 2h4.4l.6 1.5.4 1.6 1.5.9 1.6-.5 1.6.2 2.2 3.8-.8 1.3-1.2 1.1v1.8l1.2 1.1.8 1.3-2.2 3.8-1.6.2-1.6-.5-1.5.9-.4 1.6-.6 1.5H9.8l-.6-1.5-.4-1.6-1.5-.9-1.6.5-1.6-.2-2.2-3.8.8-1.3 1.2-1.1v-1.8l-1.2-1.1-.8-1.3 2.2-3.8 1.6-.2 1.6.5 1.5-.9.4-1.6Z"/><circle cx="12" cy="12" r="3"/>',
  check: '<path d="m5 12 4 4L19 6"/>',
  question: '<circle cx="12" cy="12" r="9"/><path d="M9.6 9.2a2.5 2.5 0 0 1 4.8.8c0 1.7-2.4 2.2-2.4 3.7M12 17.2h.01"/>',
  columns: '<rect x="3" y="4" width="18" height="16" rx="1"/><path d="M9 4v16M15 4v16"/>',
  folder: '<path d="M3 6.5A1.5 1.5 0 0 1 4.5 5H9l2 2h8.5A1.5 1.5 0 0 1 21 8.5v9a1.5 1.5 0 0 1-1.5 1.5h-15A1.5 1.5 0 0 1 3 17.5Z"/>',
  file: '<path d="M6 3h8l4 4v14H6Z"/><path d="M14 3v4h4"/>',
  chip: '<rect x="6" y="6" width="12" height="12" rx="2"/><path d="M9.5 9.5h5v5h-5zM9 2v4M15 2v4M9 18v4M15 18v4M2 9h4M2 15h4M18 9h4M18 15h4"/>',
  close: '<path d="m6 6 12 12M6 18 18 6"/>',
};
export type IconName = keyof typeof iconPaths;
