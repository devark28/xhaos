// Boards for the Draw tab: the workspace's list, the one open, and the actions
// on them. The open board's scene lives in a BoardSession (board-session.ts),
// which the Draw component owns; board events from the stream reach it here.
import { call } from './backend.ts';
import { get, update, toast, errorText } from './store.ts';
import { addFiles } from './composer.ts';
import { isNarrow } from '../narrow.ts';
import { LEGACY_KEY, dataURLFor, markChanged, nextAfterDelete, nextUntitled, noticeAgent, parseLegacyScene } from '../../board.ts';
import { importScene, type BoardCall, type BoardSession, type ServerBoard } from '../../board-session.ts';
import type { BackendEvent, BoardSummary } from '../../types.ts';

let open: BoardSession | null = null;

// setOpenBoard hands the open board's session to the event handler.
export function setOpenBoard(session: BoardSession | null) { open = session; }

export const boardCall: BoardCall = (method, ...args) => call('board', method, ...args);

// refreshBoards loads the workspace's boards and selects one if none is open.
// A drawing kept in this browser by an older version becomes the first board
// (once: the browser's copy is removed after the server has it).
export async function refreshBoards() {
  const ws = get().active;
  try {
    let list: BoardSummary[] = await boardCall('List', ws);
    if (!list.length) {
      const legacy = parseLegacyScene(readLegacy());
      const created: BoardSummary = await boardCall('Create', { workspaceId: ws, name: legacy ? 'Imported drawing' : '' });
      if (legacy) {
        try {
          await importScene(boardCall, ws, created.id, legacy);
        } catch (error) {
          await boardCall('Delete', ws, created.id).catch(() => {}); // a half-filled board would hide the import next time
          throw error;
        }
        try { localStorage.removeItem(LEGACY_KEY); } catch { /* nothing to drop */ }
        toast('The drawing kept in this browser is now a board on the server.');
      }
      list = [created];
    }
    update(s => {
      s.boards = list;
      if (!list.some(b => b.id === s.boardId)) s.boardId = list[0].id;
    });
  } catch (error) {
    toast(`Could not load boards: ${errorText(error)}`);
  }
}

function readLegacy(): string | null {
  try { return localStorage.getItem(LEGACY_KEY); } catch { return null; }
}

// openBoard shows a board in the Draw tab. Phones have no canvas: they only
// list boards (the Boards panel), so there the board is just selected.
// Opening a board clears its "changed" mark.
export function openBoard(id: number) {
  update(s => { s.boardId = id; if (!isNarrow()) s.mode = 'draw'; delete s.boardChanges[id]; });
}

// dismissBoardChange clears a board's "changed" mark without opening it.
export function dismissBoardChange(id: number) {
  update(s => { delete s.boardChanges[id]; });
}

// newBoard makes an empty board, named "Untitled N", and opens it.
export async function newBoard() {
  try {
    const board: BoardSummary = await boardCall('Create', { workspaceId: get().active, name: nextUntitled(get().boards.map(b => b.name)) });
    update(s => { upsert(s.boards, board); s.boardId = board.id; if (!isNarrow()) s.mode = 'draw'; });
  } catch (error) { toast(`Could not create the board: ${errorText(error)}`); }
}

export async function renameBoard(id: number, name: string) {
  try {
    const board: BoardSummary = await boardCall('Rename', { workspaceId: get().active, id, name });
    update(s => { upsert(s.boards, board); });
  } catch (error) { toast(`Could not rename the board: ${errorText(error)}`); }
}

export async function deleteBoard(id: number) {
  try {
    await boardCall('Delete', get().active, id);
    update(s => {
      const next = nextAfterDelete(s.boards, id);
      s.boards = s.boards.filter(b => b.id !== id);
      if (s.boardId === id) s.boardId = next;
      delete s.boardChanges[id];
    });
  } catch (error) { toast(`Could not delete the board: ${errorText(error)}`); }
}

// upsert puts a board in the list (sorted by ID, the picker's order) or
// replaces the entry it already has.
function upsert(list: BoardSummary[], board: BoardSummary) {
  const at = list.findIndex(b => b.id === board.id);
  if (at >= 0) list[at] = board;
  else { list.push(board); list.sort((a, b) => a.id - b.id); }
}

// BoardNotice is a save's changed elements, sent live (board.Notice on the server).
export interface BoardNotice { id: number; source?: string; elements: any[] }

// resyncOpenBoard reloads the open board and merges it into the scene. Notices
// are not replayed, so a page that was off the stream calls this on reconnect.
export function resyncOpenBoard() {
  open?.resync().catch(error => toast(`Could not refresh the board: ${errorText(error)}`));
}

// onBoardNotice hands a save from another page or an agent to the open
// board's session; any other board loads its scene when opened. An agent's
// addition to a board not showing in Draw marks it "changed" in the Boards
// panel (and its tab) until it is opened or the mark is dismissed. Marks live
// in this page only: notices are not replayed, so a reload starts clean.
export function onBoardNotice(n: BoardNotice) {
  const id = Number(n?.id);
  const s = get();
  if (id === s.boardId) open?.notice({ source: n.source, elements: n.elements ?? [] });
  const showing = id === s.boardId && s.mode === 'draw' && !isNarrow();
  const next = markChanged(s.boardChanges, id, noticeAgent(n?.source), showing);
  if (next !== s.boardChanges) update(d => { d.boardChanges = next; });
}

// onBoardEvent applies a stored board event from the stream: list changes go
// to the store. It returns false for other events.
export function onBoardEvent(event: BackendEvent): boolean {
  switch (event.kind) {
    case 'board.created':
    case 'board.renamed':
      update(s => { upsert(s.boards, event.data as BoardSummary); });
      return true;
    case 'board.deleted': {
      const id = Number(event.data?.id);
      update(s => {
        const next = nextAfterDelete(s.boards, id);
        s.boards = s.boards.filter(b => b.id !== id);
        if (s.boardId === id) s.boardId = next;
        delete s.boardChanges[id];
      });
      return true;
    }
    case 'board.saved':
      return true; // stored by earlier builds; saves now come as notices and the scene is in the tables
    default:
      return false;
  }
}

// attachBoard adds a board to the next message: as a PNG picture, or as its
// Excalidraw data (the .excalidraw JSON a model can read and edit). It reads
// the board from the server, after the open board's last edits are saved.
export async function attachBoard(id: number, as: 'png' | 'scene') {
  try {
    await open?.flush();
    const board: ServerBoard = await boardCall('Load', get().active, id);
    const elements = board.elements.filter(e => !e.isDeleted);
    if (!elements.length) { toast('That board is empty. Draw something first.'); return; }
    const files: Record<string, { id: string; mimeType: string; dataURL: string; created: number }> = {};
    for (const image of board.images) {
      const file = await boardCall('Image', get().active, id, image.fileId);
      files[image.fileId] = { id: image.fileId, mimeType: file.mimeType, dataURL: dataURLFor(file.mimeType, file.data), created: image.created };
    }
    const base = `${board.name.replace(/[^\p{L}\p{N}_-]+/gu, '-').replace(/^-+|-+$/g, '') || 'board'}-${stamp()}`;
    const { exportToBlob, serializeAsJSON } = await loadExcalidraw();
    if (as === 'png') {
      const blob = await exportToBlob({ elements: elements as any, appState: { ...board.appState, exportBackground: true } as any, files: files as any, mimeType: 'image/png' });
      attach(new File([blob], `${base}.png`, { type: 'image/png' }), 'an image');
    } else {
      const json = serializeAsJSON(elements as any, board.appState as any, files as any, 'local');
      attach(new File([json], `${base}.excalidraw`, { type: 'application/json' }), 'Excalidraw data');
    }
  } catch (error) { toast(`Could not attach the board: ${errorText(error)}`); }
}

function attach(file: File, what: string) {
  addFiles([file]);
  update(s => { s.mode = 'message'; if (isNarrow()) s.mobileView = 'agents'; });
  toast(`Drawing attached as ${what}. Add a message and send when ready.`);
}

const stamp = () => new Date().toISOString().slice(0, 19).replace(/[:T]/g, '-');

// loadExcalidraw imports the library on first use (it is large). Fonts come
// from our own server, as the Draw tab expects.
async function loadExcalidraw() {
  (window as any).EXCALIDRAW_ASSET_PATH ??= `${location.origin}/dist/excalidraw/`;
  return import('@excalidraw/excalidraw');
}
