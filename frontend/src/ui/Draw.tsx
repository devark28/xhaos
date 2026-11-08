// The Draw tab: an Excalidraw canvas, loaded on first use (it is large). It
// shows the open board (app/board on the server), which saves as the page
// draws, throttled, and follows the changes other pages make. Boards are
// listed, opened and attached in the Boards panel (state/boards.ts).
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import * as stylex from '@stylexjs/stylex';
import { Excalidraw, reconcileElements } from '@excalidraw/excalidraw';
import '@excalidraw/excalidraw/index.css';
import { colors } from './theme/tokens.stylex.ts';
import { useApp, toast, errorText } from './state/store.ts';
import { boardCall, setOpenBoard } from './state/boards.ts';
import { useThemeMode } from './appearance.ts';
import { BoardSession, type SceneHost } from '../board-session.ts';
import type { ServerBoard } from '../board-session.ts';

// Fonts come from our own server (copied there by scripts/build.mjs).
(window as any).EXCALIDRAW_ASSET_PATH = `${location.origin}/dist/excalidraw/`;

type API = Parameters<NonNullable<Parameters<typeof Excalidraw>[0]['excalidrawAPI']>>[0];

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

export default function Draw() {
  const ws = useApp(s => s.active);
  const backed = useApp(s => s.backendVersion !== null);
  const boardId = useApp(s => s.boardId);
  const api = useRef<API | null>(null);
  const session = useRef<BoardSession | null>(null);
  const [board, setBoard] = useState<ServerBoard | null>(null);
  const theme = useThemeMode();
  // The canvas's props are stable: it re-renders only when the board or theme changes, not per stroke.
  const setApi = useCallback((a: API) => { api.current = a; }, []);
  const initialData = useMemo(() => board && { elements: board.elements as any, files: {}, appState: { ...board.appState, collaborators: new Map() } as any }, [board]);
  // Images are looked for when the scene gains elements or a new files object, not on every edit.
  const seen = useRef<{ count: number; files: unknown }>({ count: -1, files: null });
  const onChange = useCallback((elements: readonly unknown[], _appState: unknown, files: unknown) => {
    session.current?.changed();
    if (elements.length !== seen.current.count || files !== seen.current.files) {
      seen.current = { count: elements.length, files };
      void session.current?.loadImages();
    }
  }, []);

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

  if (!backed) {
    return <div data-draw {...stylex.props(styles.root)}><p {...stylex.props(styles.hint)}>Boards are kept on the Xhaos server, which this page is not connected to.</p></div>;
  }

  return (
    <div data-draw {...stylex.props(styles.root)}>
      <div {...stylex.props(styles.canvas)}>
        {board ? (
          <Excalidraw
            key={board.id}
            excalidrawAPI={setApi}
            initialData={initialData}
            theme={theme}
            onChange={onChange}
          />
        ) : (
          <p {...stylex.props(styles.hint, styles.loading)}>{boardId == null ? 'No board is open. Open one in Boards, or make a new one there.' : 'Opening the board…'}</p>
        )}
      </div>
    </div>
  );
}

const styles = stylex.create({
  root: { display: 'flex', flexDirection: 'column', flexGrow: 1, minHeight: 0, gap: 6 },
  hint: { color: colors.textFaint, fontSize: 10 },
  loading: { padding: 12 },
  canvas: { position: 'relative', flexGrow: 1, minHeight: 0, borderRadius: 4, overflow: 'hidden', borderWidth: 1, borderStyle: 'solid', borderColor: colors.lineFaint },
});
