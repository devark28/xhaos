import test from 'node:test';
import assert from 'node:assert/strict';
import { boardRows, changedElements, dataURLBase64, dataURLFor, editOf, keepAppState, missingFiles, nextAfterDelete, nextUntitled, parseLegacyScene, referencedFiles, type SceneElement } from './board.ts';

const el = (id: string, version: number, versionNonce = 1, extra: Partial<SceneElement> = {}): SceneElement => ({ id, type: 'rectangle', version, versionNonce, ...extra });

test('an element is changed when its version or nonce differs from the server’s', () => {
  const synced = new Map([['a', editOf(el('a', 2, 5))], ['b', editOf(el('b', 1, 1))]]);
  const scene = [el('a', 2, 5), el('b', 1, 9), el('c', 1, 1)];
  assert.deepEqual(changedElements(scene, synced).map(e => e.id), ['b', 'c']);
});

test('a deleted element still counts as a change until the server has the tombstone', () => {
  const synced = new Map([['a', editOf(el('a', 1))]]);
  const tomb = el('a', 2, 3, { isDeleted: true });
  assert.deepEqual(changedElements([tomb], synced).map(e => e.id), ['a']);
  assert.deepEqual(changedElements([tomb], new Map([['a', editOf(tomb)]])), []);
});

test('referenced files are the images the live elements use, once each', () => {
  const scene = [
    el('i1', 1, 1, { type: 'image', fileId: 'f1' }),
    el('i2', 1, 1, { type: 'image', fileId: 'f1' }),
    el('i3', 1, 1, { type: 'image', fileId: 'f2', isDeleted: true }),
    el('r', 1, 1, { fileId: 'not-an-image' }),
  ];
  assert.deepEqual(referencedFiles(scene), ['f1']);
  assert.deepEqual(missingFiles(scene, id => id === 'f1'), []);
  assert.deepEqual(missingFiles(scene, () => false), ['f1']);
});

test('a data URL gives its base64 payload and back', () => {
  const url = dataURLFor('image/png', 'iVBORw0KGgo=');
  assert.equal(url, 'data:image/png;base64,iVBORw0KGgo=');
  assert.equal(dataURLBase64(url), 'iVBORw0KGgo=');
  assert.throws(() => dataURLBase64('https://example.com/a.png'));
});

test('a legacy scene is read only when it has elements', () => {
  assert.equal(parseLegacyScene(null), null);
  assert.equal(parseLegacyScene('not json'), null);
  assert.equal(parseLegacyScene('{"elements":[]}'), null);
  const scene = parseLegacyScene(JSON.stringify({ elements: [{ id: 'a' }, 7, null], appState: { zoom: { value: 1 } }, files: { f: { id: 'f' } } }));
  assert.deepEqual(scene?.elements.map(e => e.id), ['a']);
  assert.deepEqual(scene?.appState, { zoom: { value: 1 } });
  assert.deepEqual(Object.keys(scene?.files ?? {}), ['f']);
});

test('the app state kept per board leaves out what cannot be stored', () => {
  const kept = keepAppState({ viewBackgroundColor: '#fff', zoom: { value: 2 }, collaborators: new Map(), selectedElementIds: { a: true }, theme: 'dark' });
  assert.deepEqual(kept, { viewBackgroundColor: '#fff', zoom: { value: 2 } });
  assert.doesNotThrow(() => JSON.stringify(kept));
});

test('deleting the open board picks its neighbour', () => {
  const list = [{ id: 1 }, { id: 2 }, { id: 3 }].map(b => ({ ...b, name: '', createdAt: '', updatedAt: '' }));
  assert.equal(nextAfterDelete(list, 2), 3);
  assert.equal(nextAfterDelete(list, 3), 2);
  assert.equal(nextAfterDelete(list.slice(0, 1), 1), null);
});

test('a new board is named Untitled one above the highest Untitled number in use', () => {
  assert.equal(nextUntitled([]), 'Untitled 1');
  assert.equal(nextUntitled(['Sketch', 'Untitled 1']), 'Untitled 2');
  assert.equal(nextUntitled(['Untitled 2', 'Untitled 7', 'Untitled 3']), 'Untitled 8');
  assert.equal(nextUntitled(['Untitled 4']), 'Untitled 5');
  assert.equal(nextUntitled(['Untitled', 'My Untitled 9', 'untitled 5']), 'Untitled 1');
});

test('the Boards panel marks only the open board', () => {
  const rows = boardRows([{ id: 1, name: 'A' }, { id: 2, name: 'B' }], 2);
  assert.deepEqual(rows.map(r => [r.id, r.open]), [[1, false], [2, true]]);
  assert.deepEqual(boardRows([{ id: 1, name: 'A' }], null).map(r => r.open), [false]);
  assert.deepEqual(boardRows([], 3), []);
});
