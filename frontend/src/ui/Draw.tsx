// The Draw tab: an Excalidraw canvas, loaded on first use (it is large). Its
// boards live on the server (app/board): the picker lists the workspace's,
// and the open one saves as the page draws, debounced, and follows the
// changes other pages make. A drawing is attached to the composer as an image
// (PNG) or as its Excalidraw scene: the .excalidraw JSON, which a model can
// read and edit like code.
import { useEffect, useRef, useState } from 'react';
import * as stylex from '@stylexjs/stylex';
import { Excalidraw, exportToBlob, reconcileElements, serializeAsJSON } from '@excalidraw/excalidraw';
import '@excalidraw/excalidraw/index.css';
import { colors } from './theme/tokens.stylex.ts';
import { Button } from './kit.tsx';
import { addFiles } from './state/composer.ts';
import { useApp, update, toast, errorText } from './state/store.ts';
import { showMessageComposer } from './state/panels.ts';
import { boardCall, createBoard, deleteBoard, refreshBoards, renameBoard, selectBoard, setOpenBoard } from './state/boards.ts';
import { useThemeMode } from './appearance.ts';
import { BoardSession, type SceneHost } from '../board-session.ts';
import type { ServerBoard } from '../board-session.ts';

// Fonts come from our own server (copied there by scripts/build.mjs).
(window as any).EXCALIDRAW_ASSET_PATH = `${location.origin}/dist/excalidraw/`;

type API = Parameters<NonNullable<Parameters<typeof Excalidraw>[0]['excalidrawAPI']>>[0];
type Scene = { elements: readonly any[]; appState: Record<string, any>; files: Record<string, any> };

// hostOf adapts the Excalidraw instance to the board session.
function hostOf(api: { current: API | null }): SceneHost {
  return {
    elements: () => (api.current?.getSceneElementsIncludingDeleted() ?? []) as any,
    appState: () => (api.current?.getAppState() ?? {}) as any,
    files: () => (api.current?.getFiles() ?? {}) as any,
    merge: remote => {
      const a = api.current;
      if (!a) return;
      const local = a.getSceneElementsIncludingDeleted();
      a.updateScene({ elements: reconcileElements(local as any, remote as any, a.getAppState() as any) as any });
    },
    addFiles: files => api.current?.addFiles(files as any),
  };
}

const stamp = () => new Date().toISOString().slice(0, 19).replace(/[:T]/g, '-');

export default function Draw() {
  const ws = useApp(s => s.active);
  const backed = useApp(s => s.backendVersion !== null);
  const boards = useApp(s => s.boards);
  const boardId = useApp(s => s.boardId);
  const api = useRef<API | null>(null);
  const session = useRef<BoardSession | null>(null);
  const [board, setBoard] = useState<ServerBoard | null>(null);
  const [empty, setEmpty] = useState(true);
  const theme = useThemeMode();

  useEffect(() => { if (backed) void refreshBoards(); }, [backed, ws]);

  // Opening a board: load it, and close its session (saving what is pending) when another opens.
  useEffect(() => {
    if (boardId == null || !backed) { setBoard(null); return; }
    let live = true;
    const s = new BoardSession({
      call: boardCall,
      workspaceId: ws,
      boardId,
      host: hostOf(api),
      onError: error => toast(`Could not save the board: ${errorText(error)}`),
    });
    session.current = s;
    setOpenBoard(s);
    api.current = null;
    setBoard(null);
    s.load()
      .then(loaded => { if (live) setBoard(loaded); })
      .catch(error => { if (live) toast(`Could not open the board: ${errorText(error)}`); });
    return () => {
      live = false;
      setOpenBoard(null);
      session.current = null;
      void s.dispose();
    };
  }, [backed, ws, boardId]);

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

  const openSummary = boards.find(b => b.id === boardId);
  const rename = () => {
    if (!openSummary) return;
    const name = prompt('Rename the board', openSummary.name);
    if (name && name.trim() && name.trim() !== openSummary.name) void renameBoard(openSummary.id, name);
  };
  const remove = () => {
    if (!openSummary) return;
    if (!confirm(`Delete the board "${openSummary.name}"? Its drawing is removed from the server.`)) return;
    void deleteBoard(openSummary.id);
  };

  if (!backed) {
    return <div data-draw {...stylex.props(styles.root)}><p {...stylex.props(styles.hint)}>Boards are kept on the Xhaos server, which this page is not connected to.</p></div>;
  }

  return (
    <div data-draw {...stylex.props(styles.root)}>
      <div {...stylex.props(styles.bar)}>
        <select id="draw-board" aria-label="Board" value={boardId ?? ''} onChange={e => selectBoard(Number(e.target.value))} disabled={!boards.length} {...stylex.props(styles.select)}>
          {boards.map(b => <option key={b.id} value={b.id}>{b.name}</option>)}
        </select>
        <Button id="draw-new-board" onClick={() => void createBoard()}>New board</Button>
        <Button id="draw-rename-board" onClick={rename} disabled={!openSummary}>Rename</Button>
        <Button id="draw-delete-board" onClick={remove} disabled={!openSummary}>Delete</Button>
      </div>
      <div {...stylex.props(styles.bar)}>
        <span {...stylex.props(styles.hint)}>Attach the drawing to your next message as</span>
        <Button id="draw-attach-image" onClick={attachImage} disabled={empty || !board}>Image (PNG)</Button>
        <Button id="draw-attach-scene" onClick={attachScene} disabled={empty || !board}>Excalidraw data</Button>
        <Button id="draw-back" onClick={showMessageComposer} sx={styles.back}>Back to message</Button>
      </div>
      <div {...stylex.props(styles.canvas)}>
        {board ? (
          <Excalidraw
            key={board.id}
            excalidrawAPI={a => { api.current = a; }}
            initialData={{ elements: board.elements as any, files: {}, appState: { ...board.appState, collaborators: new Map() } as any }}
            theme={theme}
            onChange={(elements) => {
              setEmpty(!elements.some(e => !e.isDeleted));
              session.current?.changed();
              // The scene fills in after the canvas mounts, so the images it
              // uses are asked for here; a call with nothing missing does nothing.
              void session.current?.loadImages();
            }}
          />
        ) : (
          <p {...stylex.props(styles.hint, styles.loading)}>{boardId == null && boards.length === 0 ? 'Loading boards…' : 'Opening the board…'}</p>
        )}
      </div>
    </div>
  );
}

const styles = stylex.create({
  root: { display: 'flex', flexDirection: 'column', flexGrow: 1, minHeight: 0, gap: 6 },
  bar: { display: 'flex', alignItems: 'center', gap: 8, flexShrink: 0, paddingInline: 4, fontSize: 11, flexWrap: 'wrap' },
  select: { maxWidth: 220, fontSize: 12, padding: '4px 6px', borderRadius: 4, borderWidth: 1, borderStyle: 'solid', borderColor: colors.lineFaint, backgroundColor: 'transparent', color: 'inherit' },
  hint: { color: colors.textFaint, fontSize: 10 },
  loading: { padding: 12 },
  back: { marginLeft: 'auto' },
  canvas: { position: 'relative', flexGrow: 1, minHeight: 0, borderRadius: 4, overflow: 'hidden', borderWidth: 1, borderStyle: 'solid', borderColor: colors.lineFaint },
});
