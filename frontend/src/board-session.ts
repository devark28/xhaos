// A board open in the Draw tab: it loads the board from the server, sends the
// scene's changes back (debounced), fetches the images the scene needs, and
// merges the changes other pages made. It holds no Excalidraw code: the page
// gives it a SceneHost, so the merge logic can be tested with a fake scene.
import { changedElements, editOf, keepAppState, missingFiles, referencedFiles, sameEdit, dataURLBase64, dataURLFor, type Edit, type SceneElement, type SceneFile } from './board.ts';
import { newId } from './ids.ts';

// pageId names this page in its saves: the server echoes it in the notice for
// each save, so the page skips its own (see BoardSession.notice).
export const pageId = newId();

// BoardCall calls a method of the board service.
export type BoardCall = (method: string, ...args: any[]) => Promise<any>;

// SceneHost is the open scene: the page implements it over Excalidraw.
export interface SceneHost {
  elements(): SceneElement[]; // deleted elements included: they are the merge's tombstones
  appState(): Record<string, unknown>;
  files(): Record<string, SceneFile>;
  merge(remote: SceneElement[]): void; // reconciles remote elements into the scene, Excalidraw's rule
  addFiles(files: SceneFile[]): void;
}

export interface BoardImage { fileId: string; mimeType: string; created: number }
export interface ServerBoard { id: number; name: string; appState: Record<string, unknown>; elements: SceneElement[]; images: BoardImage[] }
export interface SaveResult { updatedAt: string; newer: SceneElement[] }

// pushScene sends elements to a board: the images they use first (each once,
// as tracked by uploaded), then the elements and the app state. The server
// merges the elements and returns the ones it holds newer.
export async function pushScene(
  call: BoardCall,
  workspaceId: string,
  boardId: number,
  scene: { elements: SceneElement[]; appState: Record<string, unknown>; files: Record<string, SceneFile> },
  uploaded: Set<string>,
  source = pageId,
): Promise<SaveResult> {
  for (const fileId of referencedFiles(scene.elements)) {
    if (uploaded.has(fileId)) continue;
    const file = scene.files[fileId];
    if (!file) continue; // the page does not hold it, so it is not a change of its own
    await call('AddImage', { workspaceId, boardId, fileId, created: file.created, data: dataURLBase64(file.dataURL) });
    uploaded.add(fileId);
  }
  return call('Save', { workspaceId, id: boardId, source, elements: scene.elements, appState: keepAppState(scene.appState) });
}

export interface SessionOptions {
  call: BoardCall;
  workspaceId: string;
  boardId: number;
  host: SceneHost;
  onError?: (error: unknown) => void;
  interval?: number; // at most one save per interval while the scene changes; 120 ms by default
  source?: string; // this page's ID in its saves; pageId by default
}

export class BoardSession {
  private synced = new Map<string, Edit>(); // element ID -> the edit the server holds
  private uploaded = new Set<string>(); // image file IDs the server holds
  private images: BoardImage[] = [];
  private fetching = new Set<string>();
  private unavailable = new Set<string>(); // images the server does not have
  private timer: ReturnType<typeof setTimeout> | undefined; // the save that is due, if any
  private queue: Promise<void> = Promise.resolve(); // saves run one at a time
  private readonly o: SessionOptions;

  constructor(options: SessionOptions) {
    this.o = options;
  }

  // load fetches the board. The page draws its scene from the result; images
  // follow through loadImages once the scene exists.
  async load(): Promise<ServerBoard> {
    const board: ServerBoard = await this.o.call('Load', this.o.workspaceId, this.o.boardId);
    this.remember(board.images, board.elements);
    return board;
  }

  // resync reloads the board and merges it into the open scene (after the
  // event stream was down, the page may have missed changes).
  async resync(): Promise<void> {
    const board: ServerBoard = await this.o.call('Load', this.o.workspaceId, this.o.boardId);
    this.remember(board.images, board.elements);
    this.receive(board.elements);
  }

  private remember(images: BoardImage[], elements: SceneElement[]) {
    this.images = images;
    for (const image of images) this.uploaded.add(image.fileId);
    this.synced = new Map(elements.map(e => [e.id, editOf(e)]));
  }

  // receive merges elements another page changed (a live event, or a resync).
  // An element the scene ends up holding at the server's version counts as
  // synced; one the local edit still outranks is sent on the next save.
  receive(remote: SceneElement[]): void {
    if (!remote.length) return;
    this.o.host.merge(remote);
    const now = new Map(this.o.host.elements().map(e => [e.id, e]));
    for (const e of remote) {
      const local = now.get(e.id);
      if (local && sameEdit(editOf(e), local)) this.synced.set(e.id, editOf(e));
    }
    void this.loadImages();
  }

  // notice applies a save another page made (the server's live notice for it).
  // The page's own saves are skipped: their elements are already in the scene.
  notice(n: { source?: string; elements: SceneElement[] }): void {
    if (n.source === (this.o.source ?? pageId)) return;
    this.receive(n.elements);
  }

  // loadImages fetches the images the open scene uses and the page lacks.
  async loadImages(): Promise<void> {
    const have = (id: string) => !!this.o.host.files()[id] || this.fetching.has(id) || this.unavailable.has(id);
    const need = missingFiles(this.o.host.elements(), have);
    if (!need.length) return;
    need.forEach(id => this.fetching.add(id));
    const got = await Promise.all(need.map(async fileId => {
      try {
        const file = await this.o.call('Image', this.o.workspaceId, this.o.boardId, fileId);
        const created = this.images.find(i => i.fileId === fileId)?.created ?? Date.now();
        return { id: fileId, mimeType: file.mimeType, dataURL: dataURLFor(file.mimeType, file.data), created };
      } catch (error) {
        this.unavailable.add(fileId);
        this.o.onError?.(error);
        return null;
      } finally {
        this.fetching.delete(fileId);
      }
    }));
    const files = got.filter((f): f is SceneFile => f !== null);
    if (files.length) this.o.host.addFiles(files);
  }

  // changed is called on every edit to the scene. A save is due one interval
  // after the first edit that finds none pending, and it takes every edit made
  // up to then: while the scene keeps changing (a stroke), the other pages get
  // it about every interval, and the last edit is always saved.
  changed(): void {
    if (this.timer !== undefined) return;
    this.timer = setTimeout(() => { this.timer = undefined; void this.flush(); }, this.o.interval ?? 120);
  }

  // flush saves what changed now, and resolves when the save has run.
  flush(): Promise<void> {
    clearTimeout(this.timer);
    this.timer = undefined;
    this.queue = this.queue.then(() => this.save()).catch(error => this.o.onError?.(error));
    return this.queue;
  }

  // dispose saves what is pending; the session is done after it.
  dispose(): Promise<void> {
    return this.flush();
  }

  private async save(): Promise<void> {
    const host = this.o.host;
    const sent = changedElements(host.elements(), this.synced);
    if (!sent.length) return; // an app-state-only change waits for the next edit
    const result = await pushScene(this.o.call, this.o.workspaceId, this.o.boardId, { elements: sent, appState: host.appState(), files: host.files() }, this.uploaded, this.o.source ?? pageId);
    const newer = new Set(result.newer.map(e => e.id));
    for (const e of sent) if (!newer.has(e.id)) this.synced.set(e.id, editOf(e));
    for (const e of result.newer) this.synced.set(e.id, editOf(e));
    if (result.newer.length) {
      host.merge(result.newer);
      void this.loadImages();
    }
  }
}

// importScene stores a scene that was kept in this browser as a board's first
// content: it returns when the server has all of it.
export async function importScene(call: BoardCall, workspaceId: string, boardId: number, scene: { elements: SceneElement[]; appState: Record<string, unknown>; files: Record<string, SceneFile> }): Promise<void> {
  await pushScene(call, workspaceId, boardId, scene, new Set());
}
