import test from 'node:test';
import assert from 'node:assert/strict';
import { historySummary, resultLabel } from './history-format.ts';

test('historySummary says what the history holds', () => {
  assert.equal(historySummary(0, 0), 'No past sessions imported yet. Import them with -history-import (see the guide).');
  assert.equal(historySummary(1, 0), '1 passage from past sessions');
  assert.equal(historySummary(1234, 0), '1,234 passages from past sessions');
  assert.equal(historySummary(40, 1), '40 passages from past sessions · 1 with a secret removed');
});

test('resultLabel shows the project, the day, the session and the role', () => {
  assert.equal(resultLabel('demo-app', '2026-03-05T08:00:00Z', 'sess-beta', 'user'), 'demo-app · 2026-03-05 · sess-beta · user');
  assert.equal(resultLabel('', '', 's', 'assistant'), 'unknown project · s · assistant');
});
