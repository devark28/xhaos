import { test } from 'node:test';
import assert from 'node:assert/strict';
import { ENV_MAX_BYTES, keysSummary, readEnvFile, variablesLabel } from './env-secret.ts';

const file = (name: string, text: string, size = text.length) => ({ name, size, text: async () => text });

test('variables are counted in words', () => {
  assert.equal(variablesLabel(0), '0 variables');
  assert.equal(variablesLabel(1), '1 variable');
  assert.equal(variablesLabel(12), '12 variables');
});

test('key names are summarised, never more than a line', () => {
  assert.equal(keysSummary([]), 'no variables');
  assert.equal(keysSummary(['A', 'B']), 'A, B');
  assert.equal(keysSummary(['A', 'B', 'C', 'D', 'E', 'F']), 'A, B, C, D, E, F');
  assert.equal(keysSummary(['A', 'B', 'C', 'D', 'E', 'F', 'G', 'H', 'I'], 3), 'A, B, C and 6 more');
});

test('a dropped file gives its text', async () => {
  assert.deepEqual(await readEnvFile([file('.env', 'A=1\nB=2\n')]), { text: 'A=1\nB=2\n', name: '.env' });
  assert.deepEqual(await readEnvFile([file('staging.tfvars', 'a = "x"')]), { text: 'a = "x"', name: 'staging.tfvars' });
});

test('a dropped file that cannot be a .env says why', async () => {
  const error = async (files: Parameters<typeof readEnvFile>[0]) => {
    const r = await readEnvFile(files);
    assert.ok('error' in r, 'expected an error');
    return r.error;
  };
  assert.match(await error([]), /No file/);
  assert.match(await error([file('a', 'A=1'), file('b', 'B=2')]), /one file/);
  assert.match(await error([file('big.env', '', ENV_MAX_BYTES + 1)]), /big\.env is 65 KB; a secret is at most 64 KB/);
  assert.match(await error([file('logo.png', 'PNG\0data')]), /logo\.png is not a text file/);
  assert.match(await error([{ name: 'x', size: 1, text: async () => { throw new Error('denied'); } }]), /Could not read x/);
});
