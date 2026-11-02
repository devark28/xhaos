import test from 'node:test';
import assert from 'node:assert/strict';
import { highlight, highlightDiff, languageFor } from './highlight.js';
test('source is escaped even inside colored strings', () => {
 const output=highlight('const html = "<img src=x onerror=alert(1)>";', 'javascript');
 assert.ok(output.includes('syntax-keyword'));
 assert.ok(output.includes('syntax-string'));
 assert.ok(output.includes('&lt;img'));
 assert.ok(!output.includes('<img'));
});
test('language detection and diff coloring preserve escaped source', () => {
 assert.equal(languageFor('src/event.rs'),'rust');
 assert.equal(languageFor('frontend/src/store.js'),'javascript');
 assert.equal(languageFor('', '{ "ok": true }'),'json');
 const output=highlightDiff('+ pub value: u64,\n- <old>', 'rust');
 assert.ok(output.includes('syntax-addition'));
 assert.ok(output.includes('syntax-keyword'));
 assert.ok(output.includes('syntax-deletion'));
 assert.ok(output.includes('&lt;old&gt;'));
});
