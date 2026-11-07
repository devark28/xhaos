// Boards for the Draw tab: the workspace's list, the one open, and the actions
// on them. The open board's scene lives in a BoardSession (board-session.ts),
// which the Draw component owns; board events from the stream reach it here.
import { call } from './backend.ts';
import { get, update, toast, errorText } from './store.ts';
import { LEGACY_KEY, nextAfterDelete, parseLegacyScene } from '../../board.ts';
import { importScene, type BoardCall, type BoardSession } from '../../board-session.ts';
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

export function selectBoard(id: number) {
  update(s => { s.boardId = id; });
}

export async function createBoard() {
  try {
    const board: BoardSummary = await boardCall('Create', { workspaceId: get().active, name: '' });
    update(s => { upsert(s.boards, board); s.boardId = board.id; });
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

// onBoardEvent applies a board event from the stream: list changes go to the
// store, and a change to the open board's scene goes to its session. It
// returns false for other events.
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
      });
      return true;
    }
    case 'board.saved':
      if (Number(event.data?.id) === get().boardId) open?.receive(event.data.elements ?? []);
      return true;
    default:
      return false;
  }
}
