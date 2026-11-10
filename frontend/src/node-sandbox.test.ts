import test from 'node:test';
import assert from 'node:assert/strict';
import { sandboxBadge } from './ui/node-sandbox.ts';

test('the badge names what confines a node\'s commands', () => {
  assert.deepEqual(
    [sandboxBadge({ root: '/w', landlock: 6, sandbox: 'landlock' }), sandboxBadge({ root: '/w', landlock: 0, sandbox: 'bwrap' })].map(b => [b.text, b.danger]),
    [['Landlock 6', false], ['bubblewrap', false]]);
  assert.match(sandboxBadge({ root: '/w', landlock: 0, sandbox: 'bwrap' }).tip, /this kernel has no Landlock/);
  assert.match(sandboxBadge({ root: '/w', landlock: 4, sandbox: 'bwrap' }).tip, /chosen with --sandbox bwrap/);
});

test('unsandboxed and no sandbox are red, and say how to fix it', () => {
  const off = sandboxBadge({ root: '/w', landlock: 0, unsandboxed: true, sandbox: 'off' });
  assert.equal(off.text, 'unsandboxed');
  assert.ok(off.danger);
  const none = sandboxBadge({ root: '/w', landlock: 0, sandbox: 'none', sandboxNote: 'this kernel has no Landlock; bubblewrap: bwrap is not installed (apt install bubblewrap)' });
  assert.equal(none.text, 'no sandbox: runs no commands');
  assert.ok(none.danger);
  assert.match(none.tip, /bwrap is not installed/);
  assert.match(none.tip, /apt install bubblewrap/);
});

test('older nodes without `sandbox` still get the right badge', () => {
  assert.equal(sandboxBadge({ root: '/w', landlock: 4 }).kind, 'landlock');
  assert.equal(sandboxBadge({ root: '/w', landlock: 0 }).kind, 'none');
  assert.equal(sandboxBadge({ root: '/w', landlock: 3, unsandboxed: true }).kind, 'off');
});
