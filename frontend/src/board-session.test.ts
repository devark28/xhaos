import test from 'node:test';
import assert from 'node:assert/strict';
import { BoardSession, importScene, type BoardCall, type SceneHost } from './board-session.ts';
import type { SceneElement, SceneFile } from './board.ts';

const PNG_B64 = 'iVBORw0KGgo=';
const png = (fileId: string, created = 1): SceneFile => ({ id: fileId, mimeType: 'image/png', dataURL: `data:image/png;base64,${PNG_B64}`, created });
const el = (id: string, version: number, versionNonce = 1, extra: Record<string, unknown> = {}): SceneElement => ({ id, type: 'rectangle', version, versionNonce, ...extra } as SceneElement);

// fakeServer keeps one board, merging saves the way the Go side does: a higher
// version wins; on a tie the lower versionNonce wins.
function fakeServer() {
  const elements = new Map<string, SceneElement>();
  const images = new Map<string, { mimeType: string; data: string; created: number }>();
  const calls: { method: string; args: any[] }[] = [];
  let failNextSave = false;
  const call: BoardCall = async (method, ...args) => {
    calls.push({ method, args });
    switch (method) {
      case 'Load':
        return { id: 1, name: 'B', appState: {}, elements: [...elements.values()], images: [...images].map(([fileId, i]) => ({ fileId, mimeType: i.mimeType, created: i.created })) };
      case 'AddImage': {
        const [req] = args;
        images.set(req.fileId, { mimeType: 'image/png', data: req.data, created: req.created });
        return null;
      }
      case 'Image': {
        const img = images.get(args[2]);
        if (!img) throw new Error('not found');
        return { fileId: args[2], mimeType: img.mimeType, data: img.data };
      }
      case 'Save': {
        if (failNextSave) { failNextSave = false; throw new Error('network down'); }
        const newer: SceneElement[] = [];
        for (const raw of args[0].elements as SceneElement[]) {
          const held = elements.get(raw.id);
          if (held && (held.version! > raw.version! || held.version === raw.version && held.versionNonce! < raw.versionNonce!)) newer.push(held);
          else elements.set(raw.id, raw);
        }
        return { updatedAt: 'now', newer };
      }
    }
    throw new Error(`unexpected ${method}`);
  };
  return { call, elements, images, calls, failNextSave: () => { failNextSave = true; } };
}

// fakeHost is the open scene: the page's Excalidraw, reduced to its data.
function fakeHost(initial: SceneElement[] = []) {
  let elements = [...initial];
  const files: Record<string, SceneFile> = {};
  const host: SceneHost = {
    elements: () => elements,
    appState: () => ({ viewBackgroundColor: '#fff' }),
    files: () => files,
    merge(remote) {
      const by = new Map(elements.map(e => [e.id, e]));
      for (const r of remote) {
        const l = by.get(r.id);
        if (!l || l.version! < r.version! || l.version === r.version && l.versionNonce! > r.versionNonce!) by.set(r.id, r);
      }
      elements = [...by.values()];
    },
    addFiles(list) { for (const f of list) files[f.id] = f; },
  };
  return { host, get elements() { return elements; }, set(list: SceneElement[]) { elements = list; }, files };
}

const flushed = () => new Promise(resolve => setTimeout(resolve, 0));

test('load returns the board and its images are fetched once the scene shows them', async () => {
  const server = fakeServer();
  server.elements.set('a', el('a', 1, 1, { type: 'image', fileId: 'f1' }));
  server.images.set('f1', { mimeType: 'image/png', data: PNG_B64, created: 42 });
  const scene = fakeHost([]);
  const session = new BoardSession({ call: server.call, workspaceId: 'default', boardId: 1, host: scene.host });
  const board = await session.load();
  assert.equal(board.elements.length, 1);
  scene.set(board.elements); // the page mounts the scene with them
  await session.loadImages();
  assert.equal(scene.files.f1?.dataURL, `data:image/png;base64,${PNG_B64}`);
  assert.equal(scene.files.f1?.created, 42);
});

test('a save sends only what changed, and the images it uses first, once', async () => {
  const server = fakeServer();
  server.elements.set('a', el('a', 1));
  const scene = fakeHost([el('a', 1)]);
  const session = new BoardSession({ call: server.call, workspaceId: 'default', boardId: 1, host: scene.host, interval: 5 });
  await session.load();
  scene.files.f1 = png('f1');
  scene.set([el('a', 1), el('b', 1, 1, { type: 'image', fileId: 'f1' })]);
  await session.flush();
  assert.deepEqual(server.calls.map(c => c.method), ['Load', 'AddImage', 'Save']);
  assert.equal(server.calls[2].args[0].elements.map((e: SceneElement) => e.id).join(), 'b');
  // Another edit later: the image is not sent again.
  scene.set([el('a', 1), el('b', 2, 1, { type: 'image', fileId: 'f1' })]);
  await session.flush();
  assert.equal(server.calls.filter(c => c.method === 'AddImage').length, 1);
  assert.equal(server.calls.filter(c => c.method === 'Save').length, 2);
});

test('a save with nothing changed makes no call', async () => {
  const server = fakeServer();
  server.elements.set('a', el('a', 1));
  const scene = fakeHost([el('a', 1)]);
  const session = new BoardSession({ call: server.call, workspaceId: 'default', boardId: 1, host: scene.host });
  await session.load();
  session.changed(); // the page reports a change that merged nothing
  await session.flush();
  assert.equal(server.calls.filter(c => c.method === 'Save').length, 0);
});

test('a stale local edit is replaced by the newer stored one, and not sent again', async () => {
  const server = fakeServer();
  server.elements.set('a', el('a', 5, 1, { text: 'other tab' }));
  const scene = fakeHost([el('a', 5, 1, { text: 'other tab' })]);
  const session = new BoardSession({ call: server.call, workspaceId: 'default', boardId: 1, host: scene.host });
  await session.load();
  // Edited on a page still at version 2 of a (the server holds 5): the edit is stale.
  scene.set([el('a', 3, 1, { text: 'this tab, edited' })]);
  await session.flush();
  assert.equal(scene.elements[0].text, 'other tab', 'the newer stored version won');
  const saves = server.calls.filter(c => c.method === 'Save').length;
  await session.flush();
  assert.equal(server.calls.filter(c => c.method === 'Save').length, saves, 'nothing left to send');
});

test('a remote change merges into the scene and does not come back as a save', async () => {
  const server = fakeServer();
  const scene = fakeHost([el('a', 1)]);
  const session = new BoardSession({ call: server.call, workspaceId: 'default', boardId: 1, host: scene.host });
  await session.load();
  session.receive([el('a', 3, 1, { text: 'from another tab' })]);
  assert.equal(scene.elements[0].version, 3);
  session.changed();
  await session.flush();
  assert.equal(server.calls.filter(c => c.method === 'Save').length, 0);
});

test('a burst of edits within one interval makes one save', async () => {
  const server = fakeServer();
  const scene = fakeHost([]);
  const session = new BoardSession({ call: server.call, workspaceId: 'default', boardId: 1, host: scene.host, interval: 10 });
  await session.load();
  for (let i = 1; i <= 3; i++) {
    scene.set([el('a', i)]);
    session.changed();
  }
  await new Promise(resolve => setTimeout(resolve, 40));
  await flushed();
  assert.equal(server.calls.filter(c => c.method === 'Save').length, 1);
  assert.equal(server.elements.get('a')?.version, 3);
});

test('while the scene keeps changing, saves go out about once per interval, and the last edit is saved', async () => {
  const server = fakeServer();
  const scene = fakeHost([]);
  const session = new BoardSession({ call: server.call, workspaceId: 'default', boardId: 1, host: scene.host, interval: 20 });
  await session.load();
  // A stroke: an edit every 2 ms for about 200 ms.
  for (let i = 1; i <= 100; i++) {
    scene.set([el('a', i)]);
    session.changed();
    await new Promise(resolve => setTimeout(resolve, 2));
  }
  await new Promise(resolve => setTimeout(resolve, 60));
  const saves = server.calls.filter(c => c.method === 'Save').length;
  assert.ok(saves >= 4, `saves during the stroke: ${saves}`); // live, not one at the end
  assert.ok(saves <= 16, `saves during the stroke: ${saves}`); // no more than one per interval
  assert.equal(server.elements.get('a')?.version, 100, 'the last edit is on the server');
});

test('a failed save is reported and tried again on the next flush', async () => {
  const server = fakeServer();
  const scene = fakeHost([]);
  const errors: unknown[] = [];
  const session = new BoardSession({ call: server.call, workspaceId: 'default', boardId: 1, host: scene.host, onError: e => errors.push(e) });
  await session.load();
  scene.set([el('a', 1)]);
  server.failNextSave();
  await session.flush();
  assert.equal(errors.length, 1);
  assert.equal(server.elements.has('a'), false);
  await session.flush();
  assert.equal(server.elements.has('a'), true);
});

test('an image the server does not have is asked for once, not on every merge', async () => {
  const server = fakeServer();
  const scene = fakeHost([]);
  const errors: unknown[] = [];
  const session = new BoardSession({ call: server.call, workspaceId: 'default', boardId: 1, host: scene.host, onError: e => errors.push(e) });
  await session.load();
  session.receive([el('a', 1, 1, { type: 'image', fileId: 'gone' })]);
  await flushed();
  session.receive([el('a', 2, 1, { type: 'image', fileId: 'gone' })]);
  await flushed();
  assert.equal(server.calls.filter(c => c.method === 'Image').length, 1);
  assert.equal(errors.length, 1);
});

test('importing a browser scene saves its elements and images as a board', async () => {
  const server = fakeServer();
  await importScene(server.call, 'default', 1, { elements: [el('a', 1, 1, { type: 'image', fileId: 'f1' })], appState: { zoom: { value: 1 }, collaborators: new Map() }, files: { f1: png('f1') } });
  assert.deepEqual(server.calls.map(c => c.method), ['AddImage', 'Save']);
  assert.equal(server.elements.size, 1);
  assert.equal(server.images.has('f1'), true);
});
