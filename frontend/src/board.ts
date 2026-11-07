// Logic for the Draw tab's boards that needs no browser: which scene elements
// differ from what the server holds, which images a scene needs, and how an
// image travels to and from the server.
import type { BoardSummary } from './types.ts';

// The scene as Excalidraw keeps it. Only the fields this module reads.
export interface SceneElement {
  id: string;
  type?: string;
  version?: number;
  versionNonce?: number;
  fileId?: string | null;
  isDeleted?: boolean;
}

export interface SceneFile {
  id: string;
  mimeType: string;
  dataURL: string;
  created: number;
}

// An element's edit: its version and tie-breaking nonce. Two elements with the
// same edit are the same edit.
export interface Edit { version: number; nonce: number }
export const editOf = (e: SceneElement): Edit => ({ version: e.version ?? 0, nonce: e.versionNonce ?? 0 });

// sameEdit reports whether element e is the edit held. It compares numbers and
// allocates nothing: a save runs it over the whole scene.
export const sameEdit = (held: Edit | undefined, e: SceneElement): boolean =>
  held !== undefined && held.version === (e.version ?? 0) && held.nonce === (e.versionNonce ?? 0);

// changedElements returns the elements whose edit differs from the one last
// synced for their id (synced maps id to its edit). Deleted elements count:
// their tombstones are part of the merge.
export function changedElements(elements: readonly SceneElement[], synced: ReadonlyMap<string, Edit>): SceneElement[] {
  return elements.filter(e => !sameEdit(synced.get(e.id), e));
}

// referencedFiles lists the image file IDs the live elements use, once each.
// A deleted image no longer needs its file.
export function referencedFiles(elements: readonly SceneElement[]): string[] {
  const out = new Set<string>();
  for (const e of elements) if (e.type === 'image' && e.fileId && !e.isDeleted) out.add(e.fileId);
  return [...out];
}

// missingFiles is the referenced files the page does not hold yet.
export function missingFiles(elements: readonly SceneElement[], have: (id: string) => boolean): string[] {
  return referencedFiles(elements).filter(id => !have(id));
}

// dataURLBase64 returns the base64 payload of a data URL: what the server
// takes for an image's bytes.
export function dataURLBase64(dataURL: string): string {
  const at = dataURL.indexOf(',');
  if (!dataURL.startsWith('data:') || at < 0) throw new Error('an image is not a data URL');
  return dataURL.slice(at + 1);
}

// dataURLFor builds the data URL the scene uses from the server's bytes.
export const dataURLFor = (mimeType: string, base64: string) => `data:${mimeType};base64,${base64}`;

// The legacy scene: the Draw tab kept it in this browser before boards moved
// to the server. It is imported once and then removed.
export const LEGACY_KEY = 'xhaos-excalidraw-v1';

export interface LegacyScene { elements: SceneElement[]; appState: Record<string, unknown>; files: Record<string, SceneFile> }

// parseLegacyScene reads the stored scene; null if there is none to keep.
export function parseLegacyScene(raw: string | null): LegacyScene | null {
  if (!raw) return null;
  let data: any;
  try { data = JSON.parse(raw); } catch { return null; }
  if (!data || !Array.isArray(data.elements) || !data.elements.some((e: any) => e && typeof e.id === 'string')) return null;
  return {
    elements: data.elements.filter((e: any) => e && typeof e.id === 'string'),
    appState: data.appState && typeof data.appState === 'object' ? data.appState : {},
    files: data.files && typeof data.files === 'object' ? data.files : {},
  };
}

// nextAfterDelete picks the board to show when the open one goes: the next
// one down the list, else the previous, else none.
export function nextAfterDelete(list: readonly BoardSummary[], deletedId: number): number | null {
  const at = list.findIndex(b => b.id === deletedId);
  if (at < 0) return list[0]?.id ?? null;
  const rest = list.filter(b => b.id !== deletedId);
  return rest[Math.min(at, rest.length - 1)]?.id ?? null;
}

// The app state a board keeps: its view and export settings. Everything else
// (selection, tools, collaborators, which holds a Map) stays in the browser.
const KEPT_APP_STATE = ['viewBackgroundColor', 'gridSize', 'gridStep', 'gridModeEnabled', 'zenModeEnabled', 'exportBackground', 'exportWithDarkMode', 'exportScale', 'scrollX', 'scrollY', 'zoom'] as const;

export function keepAppState(appState: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const key of KEPT_APP_STATE) if (appState[key] !== undefined) out[key] = appState[key];
  return out;
}

// nextUntitled names a new board: "Untitled N", where N is one more than the
// highest such number among the names in use (1 when there is none).
export function nextUntitled(names: readonly string[]): string {
  let highest = 0;
  for (const name of names) {
    const m = /^Untitled (\d+)$/.exec(name.trim());
    if (m) highest = Math.max(highest, Number(m[1]));
  }
  return `Untitled ${highest + 1}`;
}

// boardRows lists the boards for the Boards panel, each marked open or not.
export function boardRows<T extends { id: number }>(list: readonly T[], openId: number | null): (T & { open: boolean })[] {
  return list.map(b => ({ ...b, open: b.id === openId }));
}
