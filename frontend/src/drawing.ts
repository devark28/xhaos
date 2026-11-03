// The sketch pad behind the composer's Draw tab. It renders SVG markup and
// binds its own DOM events; the scene is kept in localStorage.
const esc = (text: unknown): string => String(text).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' } as Record<string, string>)[c]);

type Tool = 'select' | 'pen' | 'rect' | 'ellipse' | 'line' | 'arrow' | 'text' | 'erase';
type Kind = 'pen' | 'rect' | 'ellipse' | 'line' | 'arrow' | 'text';
type Point = [number, number];
export interface Shape {
  type: Kind;
  x: number; y: number; x2?: number; y2?: number;
  width?: number; color?: string;
  points?: Point[];
  text?: string;
}
export interface Scene { elements: Shape[]; background: string }

const kinds: Kind[] = ['pen', 'rect', 'ellipse', 'line', 'arrow', 'text'];

// normalizeScene sanitizes stored or imported data: known shapes only,
// bounded numbers, hex colors, and capped sizes.
export function normalizeScene(value: any): Scene {
  const number = (v: unknown) => (Number.isFinite(Number(v)) ? Math.max(-20000, Math.min(20000, Number(v))) : 0);
  const color = (v: unknown) => (/^#[0-9a-f]{6}$/i.test(String(v || '')) ? String(v) : '#eeeeee');
  const raw: any[] = Array.isArray(value?.elements) ? value.elements : [];
  const elements = raw.slice(0, 2000).filter(e => kinds.includes(e?.type)).map((e): Shape => ({
    type: e.type,
    x: number(e.x), y: number(e.y), x2: number(e.x2 ?? e.x), y2: number(e.y2 ?? e.y),
    width: Math.max(1, Math.min(20, number(e.width) || 3)),
    color: color(e.color),
    ...(e.type === 'pen' ? { points: (Array.isArray(e.points) ? e.points : []).slice(0, 10000).filter((p: unknown) => Array.isArray(p) && p.length >= 2).map(([x, y]: unknown[]) => [number(x), number(y)] as Point) } : {}),
    ...(e.type === 'text' ? { text: String(e.text || '').slice(0, 2000) } : {}),
  }));
  return { elements, background: /^#[0-9a-f]{6}$/i.test(value?.background || '') ? value.background : '#151515' };
}

const key = 'xhaos-drawing-v1';
let scene: Scene = { elements: [], background: '#151515' };
try {
  const saved = JSON.parse(globalThis.localStorage?.getItem(key) || 'null');
  if (saved?.version === 1 && Array.isArray(saved.elements)) scene = normalizeScene(saved);
} catch { /* start empty */ }
let tool: Tool = 'pen', color = '#eeeeee', width = 3;
const undo: string[] = [], redo: string[] = [];
const snapshot = () => JSON.stringify(scene);
const persist = () => { try { globalThis.localStorage?.setItem(key, JSON.stringify({ ...scene, version: 1 })); } catch { /* session only */ } };
const commit = (before: string) => {
  if (before === snapshot()) return;
  undo.push(before);
  if (undo.length > 80) undo.shift();
  redo.length = 0;
  persist();
};
const palette = ['#eeeeee', '#22e58b', '#ffb800', '#389cff', '#ff6262', '#111111'];

function shapeSvg(element: Shape): string {
  const { type, x = 0, y = 0, x2 = x, y2 = y, color = '#eeeeee', width = 3 } = element;
  const stroke = `stroke="${esc(color)}" stroke-width="${Number(width) || 3}" fill="none" stroke-linecap="round" stroke-linejoin="round"`;
  if (type === 'pen') return `<path d="${(element.points || []).map(([px, py], i) => `${i ? 'L' : 'M'}${Number(px)} ${Number(py)}`).join(' ')} l .01 .01" ${stroke}/>`;
  if (type === 'rect') return `<rect x="${Math.min(x, x2)}" y="${Math.min(y, y2)}" width="${Math.abs(x2 - x)}" height="${Math.abs(y2 - y)}" rx="3" ${stroke}/>`;
  if (type === 'ellipse') return `<ellipse cx="${(x + x2) / 2}" cy="${(y + y2) / 2}" rx="${Math.abs(x2 - x) / 2}" ry="${Math.abs(y2 - y) / 2}" ${stroke}/>`;
  if (type === 'text') return `<text x="${x}" y="${y}" fill="${esc(color)}" font-family="sans-serif" font-size="22">${esc(element.text)}</text>`;
  if (type === 'line' || type === 'arrow') {
    const angle = Math.atan2(y2 - y, x2 - x), size = 15;
    const head = type === 'arrow' ? ` M ${x2 - size * Math.cos(angle - 0.5)} ${y2 - size * Math.sin(angle - 0.5)} L ${x2} ${y2} L ${x2 - size * Math.cos(angle + 0.5)} ${y2 - size * Math.sin(angle + 0.5)}` : '';
    return `<path d="M ${x} ${y} L ${x2} ${y2}${head}" ${stroke}/>`;
  }
  return '';
}

export function drawingSvg(value: Scene = scene): string {
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1200 700" width="1200" height="700"><rect width="1200" height="700" fill="${esc(value.background)}"/>${value.elements.map(shapeSvg).join('')}</svg>`;
}

export function drawingView(): string {
  const tools: [Tool, string, string][] = [['select', '↖', 'Move'], ['pen', '✎', 'Pen'], ['rect', '▭', 'Rectangle'], ['ellipse', '◯', 'Ellipse'], ['line', '╱', 'Line'], ['arrow', '↗', 'Arrow'], ['text', 'T', 'Text'], ['erase', '⌫', 'Eraser']];
  return `<div class="drawing-toolbar"><div class="drawing-tool-group">${tools.map(([id, label, title]) => `<button type="button" data-draw-tool="${id}" aria-pressed="${tool === id}" title="${title}" aria-label="${title}">${label}</button>`).join('')}</div>`
    + `<div class="drawing-colors">${palette.map(value => `<button type="button" data-draw-color="${value}" style="--swatch:${value}" aria-pressed="${value === color}" aria-label="Color ${value}"></button>`).join('')}</div>`
    + `<select id="draw-width" aria-label="Stroke width">${[2, 3, 6, 10].map(value => `<option value="${value}" ${value === width ? 'selected' : ''}>${value}px</option>`).join('')}</select>`
    + `<button type="button" id="draw-background" title="Toggle canvas background">◐</button><button type="button" id="draw-undo" title="Undo" aria-label="Undo">↶</button><button type="button" id="draw-redo" title="Redo" aria-label="Redo">↷</button><button type="button" id="draw-clear">Clear</button><button type="button" id="draw-attach" class="draw-attach">Attach sketch</button></div>`
    + `<div class="draw-stage" id="draw-stage"><svg id="draw-surface" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1200 700" tabindex="0" aria-label="Sketch canvas" role="img"><rect width="1200" height="700" fill="${scene.background}"/><g id="draw-elements">${scene.elements.map(shapeSvg).join('')}</g></svg></div>`;
}

function bounds(element: Shape): [number, number, number, number] {
  const xs = element.type === 'pen' ? (element.points || []).map(p => p[0]) : [element.x, element.type === 'text' ? element.x + (element.text || '').length * 13 : (element.x2 ?? element.x)];
  const ys = element.type === 'pen' ? (element.points || []).map(p => p[1]) : [element.y, element.y2 ?? element.y];
  return [Math.min(...xs) - 8, Math.min(...ys) - (element.type === 'text' ? 24 : 8), Math.max(...xs) + 8, Math.max(...ys) + 8];
}

type Gesture = { before: string; tool: Tool; index?: number; x?: number; y?: number; original?: Shape | null; element?: Shape };

export function bindDrawing({ onAttach, onBack, notify }: { onAttach(file: File): void; onBack(): void; notify(message: string): void }) {
  const $ = <T extends Element = HTMLElement>(selector: string) => document.querySelector(selector) as T;
  const svg = $<SVGSVGElement>('#draw-surface');
  if (!svg) return;
  const paint = () => {
    $('#draw-elements').innerHTML = scene.elements.map(shapeSvg).join('');
    svg.querySelector('rect')!.setAttribute('fill', scene.background);
    $('#draw-stage').style.background = scene.background;
    $<HTMLButtonElement>('#draw-undo').disabled = !undo.length;
    $<HTMLButtonElement>('#draw-redo').disabled = !redo.length;
    $<HTMLButtonElement>('#draw-attach').disabled = !scene.elements.length;
  };
  const choose = (next: Tool) => {
    tool = next;
    document.querySelectorAll<HTMLElement>('[data-draw-tool]').forEach(button => button.setAttribute('aria-pressed', String(button.dataset.drawTool === tool)));
  };
  document.querySelectorAll<HTMLElement>('[data-draw-tool]').forEach(button => { button.onclick = () => choose(button.dataset.drawTool as Tool); });
  document.querySelectorAll<HTMLElement>('[data-draw-color]').forEach(button => {
    button.onclick = () => {
      color = button.dataset.drawColor!;
      document.querySelectorAll<HTMLElement>('[data-draw-color]').forEach(b => b.setAttribute('aria-pressed', String(b.dataset.drawColor === color)));
    };
  });
  $<HTMLSelectElement>('#draw-width').onchange = event => { width = Number((event.target as HTMLSelectElement).value); };
  $('#draw-background').onclick = () => {
    const before = snapshot();
    scene.background = scene.background === '#151515' ? '#ffffff' : '#151515';
    if (color === '#eeeeee' && scene.background === '#ffffff') color = '#111111';
    commit(before); paint();
  };
  const step = (from: string[], to: string[]) => {
    if (!from.length) return;
    to.push(snapshot());
    scene = JSON.parse(from.pop()!);
    persist(); paint();
  };
  $('#draw-undo').onclick = () => step(undo, redo);
  $('#draw-redo').onclick = () => step(redo, undo);
  $('#draw-clear').onclick = () => { const before = snapshot(); scene.elements = []; commit(before); paint(); };
  const point = (event: PointerEvent): Point => {
    const p = svg.createSVGPoint(); p.x = event.clientX; p.y = event.clientY;
    const local = p.matrixTransform(svg.getScreenCTM()!.inverse());
    return [Math.max(0, Math.min(1200, local.x)), Math.max(0, Math.min(700, local.y))];
  };
  const hit = ([x, y]: Point) => scene.elements.findLastIndex(element => {
    const [left, top, right, bottom] = bounds(element);
    return x >= left && x <= right && y >= top && y <= bottom;
  });
  let gesture: Gesture | null = null;
  svg.onpointerdown = event => {
    if (event.button !== 0) return;
    event.preventDefault();
    svg.focus({ preventScroll: true });
    const [x, y] = point(event), before = snapshot();
    if (tool === 'text') {
      const input = document.createElement('input'), stage = $('#draw-stage'), rect = stage.getBoundingClientRect();
      input.className = 'draw-text-input'; input.placeholder = 'Text…';
      input.style.left = `${event.clientX - rect.left}px`; input.style.top = `${event.clientY - rect.top - 15}px`;
      stage.append(input); input.focus();
      let done = false;
      const finish = (cancel: boolean) => {
        if (done) return;
        done = true;
        if (!cancel && input.value.trim()) { scene.elements.push({ type: 'text', x, y, text: input.value, color, width }); commit(before); paint(); }
        input.remove();
      };
      input.onkeydown = e => { e.stopPropagation(); if (e.key === 'Enter') finish(false); if (e.key === 'Escape') finish(true); };
      input.onblur = () => finish(false);
      return;
    }
    svg.setPointerCapture(event.pointerId);
    if (tool === 'erase') { gesture = { before, tool }; const index = hit([x, y]); if (index >= 0) scene.elements.splice(index, 1); }
    else if (tool === 'select') { const index = hit([x, y]); gesture = { before, tool, index, x, y, original: index >= 0 ? JSON.parse(JSON.stringify(scene.elements[index])) : null }; }
    else {
      const element: Shape = { type: tool, x, y, x2: x, y2: y, color, width, ...(tool === 'pen' ? { points: [[x, y] as Point] } : {}) };
      scene.elements.push(element);
      gesture = { before, tool, element };
    }
    paint();
  };
  svg.onpointermove = event => {
    if (!gesture) return;
    const [x, y] = point(event);
    if (gesture.tool === 'erase') { const index = hit([x, y]); if (index >= 0) scene.elements.splice(index, 1); }
    else if (gesture.tool === 'select' && gesture.index! >= 0 && gesture.original) {
      const dx = x - gesture.x!, dy = y - gesture.y!, o = gesture.original;
      scene.elements[gesture.index!] = { ...o, x: o.x + dx, y: o.y + dy, x2: (o.x2 ?? o.x) + dx, y2: (o.y2 ?? o.y) + dy, ...(o.points ? { points: o.points.map(([px, py]) => [px + dx, py + dy] as Point) } : {}) };
    } else if (gesture.element) {
      gesture.element.x2 = x; gesture.element.y2 = y;
      if (gesture.tool === 'pen') gesture.element.points!.push([x, y]);
    }
    paint();
  };
  svg.onpointerup = () => { if (gesture) { commit(gesture.before); gesture = null; } };
  svg.onpointercancel = () => { if (gesture) { scene = JSON.parse(gesture.before); gesture = null; paint(); } };
  svg.onkeydown = event => {
    if (event.key === 'Escape') { event.preventDefault(); onBack(); }
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'z') { event.preventDefault(); step(event.shiftKey ? redo : undo, event.shiftKey ? undo : redo); }
  };
  $('#draw-attach').onclick = async () => {
    const button = $<HTMLButtonElement>('#draw-attach');
    button.disabled = true;
    const url = URL.createObjectURL(new Blob([drawingSvg()], { type: 'image/svg+xml' }));
    try {
      const image = new Image(); image.src = url; await image.decode();
      const canvas = document.createElement('canvas'); canvas.width = 1200; canvas.height = 700;
      canvas.getContext('2d')!.drawImage(image, 0, 0);
      const blob = await new Promise<Blob | null>(resolve => canvas.toBlob(resolve, 'image/png'));
      if (!blob) throw new Error('Could not export sketch.');
      onAttach(new File([blob], `sketch-${Date.now()}.png`, { type: 'image/png' }));
    } catch (error) { notify((error as Error).message); button.disabled = false; }
    finally { URL.revokeObjectURL(url); }
  };
  paint();
}
