// The Draw tab: an Excalidraw canvas, loaded on first use (it is large).
// The scene is kept in this browser between visits. A drawing is attached to
// the composer as an image (PNG) or as its Excalidraw scene: the .excalidraw
// JSON, which a model can read and edit like code.
import { useEffect, useMemo, useRef, useState } from 'react';
import * as stylex from '@stylexjs/stylex';
import { Excalidraw, exportToBlob, serializeAsJSON } from '@excalidraw/excalidraw';
import '@excalidraw/excalidraw/index.css';
import { colors } from './theme/tokens.stylex.ts';
import { Button } from './kit.tsx';
import { addFiles } from './state/composer.ts';
import { update, toast, errorText } from './state/store.ts';
import { showMessageComposer } from './state/panels.ts';

// Fonts come from our own server (copied there by scripts/build.mjs).
(window as any).EXCALIDRAW_ASSET_PATH = `${location.origin}/dist/excalidraw/`;

type API = Parameters<NonNullable<Parameters<typeof Excalidraw>[0]['excalidrawAPI']>>[0];
type Scene = { elements: readonly any[]; appState: Record<string, any>; files: Record<string, any> };

// The scene lives here for the session and in localStorage across visits
// (written after a pause in drawing, and when the board closes).
const KEY = 'xhaos-excalidraw-v1';
let latest: Scene | null = null;
function loadScene(): Partial<Scene> | null {
  if (latest) return latest;
  try { return JSON.parse(localStorage.getItem(KEY) || 'null'); } catch { return null; }
}
let saveTimer: ReturnType<typeof setTimeout> | undefined;
function persist() {
  clearTimeout(saveTimer);
  if (!latest) return;
  try { localStorage.setItem(KEY, serializeAsJSON(latest.elements as any, latest.appState as any, latest.files as any, 'local')); }
  catch { /* over quota (large images): the scene lasts for this session */ }
}
function saveScene(scene: Scene) {
  latest = scene;
  clearTimeout(saveTimer);
  saveTimer = setTimeout(persist, 400);
}

const stamp = () => new Date().toISOString().slice(0, 19).replace(/[:T]/g, '-');

export default function Draw() {
  const api = useRef<API | null>(null);
  const [empty, setEmpty] = useState(true);
  const initialData = useMemo(() => {
    const saved = loadScene();
    return { elements: saved?.elements || [], files: saved?.files, appState: { ...(saved?.appState || {}), theme: 'dark' as const, collaborators: new Map() } };
  }, []);
  useEffect(() => setEmpty(!(initialData.elements as any[]).some(e => !e.isDeleted)), [initialData]);
  useEffect(() => persist, []); // save at once when the board closes

  const current = (): Scene | null => {
    const a = api.current;
    if (!a) return null;
    const elements = a.getSceneElements();
    if (!elements.length) { toast('Draw something first.'); return null; }
    return { elements, appState: a.getAppState(), files: a.getFiles() };
  };
  const attach = (file: File, what: string) => {
    addFiles([file]);
    update(s => { s.mode = 'message'; });
    toast(`Drawing attached as ${what}. Add a message and send when ready.`);
  };
  const attachImage = async () => {
    const scene = current();
    if (!scene) return;
    try {
      const blob = await exportToBlob({ elements: scene.elements as any, appState: { ...scene.appState, exportBackground: true }, files: scene.files as any, mimeType: 'image/png' });
      attach(new File([blob], `drawing-${stamp()}.png`, { type: 'image/png' }), 'an image');
    } catch (error) { toast(`Could not export the drawing: ${errorText(error)}`); }
  };
  const attachScene = () => {
    const scene = current();
    if (!scene) return;
    const json = serializeAsJSON(scene.elements as any, scene.appState as any, scene.files as any, 'local');
    attach(new File([json], `drawing-${stamp()}.excalidraw`, { type: 'application/json' }), 'Excalidraw data');
  };

  return (
    <div data-draw {...stylex.props(styles.root)}>
      <div {...stylex.props(styles.bar)}>
        <span {...stylex.props(styles.hint)}>Attach the drawing to your next message as</span>
        <Button id="draw-attach-image" onClick={attachImage} disabled={empty}>Image (PNG)</Button>
        <Button id="draw-attach-scene" onClick={attachScene} disabled={empty}>Excalidraw data</Button>
        <Button id="draw-back" onClick={showMessageComposer} sx={styles.back}>Back to message</Button>
      </div>
      <div {...stylex.props(styles.canvas)}>
        <Excalidraw
          excalidrawAPI={instance => { api.current = instance; }}
          initialData={initialData as any}
          theme="dark"
          onChange={(elements, appState, files) => {
            setEmpty(!elements.some(e => !e.isDeleted));
            saveScene({ elements, appState, files });
          }}
        />
      </div>
    </div>
  );
}

const styles = stylex.create({
  root: { display: 'flex', flexDirection: 'column', flexGrow: 1, minHeight: 0, gap: 6 },
  bar: { display: 'flex', alignItems: 'center', gap: 8, flexShrink: 0, paddingInline: 4, fontSize: 11 },
  hint: { color: colors.textFaint, fontSize: 10 },
  back: { marginLeft: 'auto' },
  canvas: { position: 'relative', flexGrow: 1, minHeight: 0, borderRadius: 4, overflow: 'hidden', borderWidth: 1, borderStyle: 'solid', borderColor: colors.lineFaint },
});
