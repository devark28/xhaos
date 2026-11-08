import test from 'node:test';
import assert from 'node:assert/strict';
import { dialogKey } from './dialog-keys.ts';

const key = (k: string, extra: Partial<KeyboardEvent> = {}) => ({ key: k, shiftKey: false, ctrlKey: false, metaKey: false, altKey: false, isComposing: false, ...extra });

test('Tab and Shift+Tab cycle through the buttons and wrap, never leaving the dialog', () => {
  assert.deepEqual(dialogKey(key('Tab'), 0, 2), { kind: 'focus', index: 1 });
  assert.deepEqual(dialogKey(key('Tab'), 1, 2), { kind: 'focus', index: 0 });
  assert.deepEqual(dialogKey(key('Tab', { shiftKey: true }), 0, 2), { kind: 'focus', index: 1 });
  assert.deepEqual(dialogKey(key('Tab', { shiftKey: true }), 1, 2), { kind: 'focus', index: 0 });
  assert.deepEqual(dialogKey(key('Tab'), 0, 1), { kind: 'focus', index: 0 });
});

test('Tab from outside the buttons goes to the first, Shift+Tab to the last', () => {
  assert.deepEqual(dialogKey(key('Tab'), -1, 2), { kind: 'focus', index: 0 });
  assert.deepEqual(dialogKey(key('Tab', { shiftKey: true }), -1, 2), { kind: 'focus', index: 1 });
  assert.deepEqual(dialogKey(key('Tab'), -1, 0), { kind: 'focus', index: -1 });
});

test('Escape cancels wherever focus is', () => {
  assert.deepEqual(dialogKey(key('Escape'), 1, 2), { kind: 'cancel' });
  assert.deepEqual(dialogKey(key('Escape'), -1, 2), { kind: 'cancel' });
});

test('Enter on a button is left to the browser (it clicks that button); elsewhere it confirms', () => {
  assert.deepEqual(dialogKey(key('Enter'), 0, 2), { kind: 'default' });
  assert.deepEqual(dialogKey(key('Enter'), 1, 2), { kind: 'default' });
  assert.deepEqual(dialogKey(key('Enter'), -1, 2), { kind: 'confirm' });
});

test('modified keys, IME composition and other keys are left alone', () => {
  assert.deepEqual(dialogKey(key('Enter', { ctrlKey: true }), -1, 2), { kind: 'default' });
  assert.deepEqual(dialogKey(key('Tab', { altKey: true }), 0, 2), { kind: 'default' });
  assert.deepEqual(dialogKey(key('Enter', { isComposing: true }), -1, 2), { kind: 'default' });
  assert.deepEqual(dialogKey(key('a'), 0, 2), { kind: 'default' });
});
