import test from 'node:test';
import assert from 'node:assert/strict';
import { markdown, formatSelection } from './markdown.js';
test('renders headings, emphasis, lists, quotes and language-colored code',()=>{
 const output=markdown('## Task\n\n**Important** and *optional* with `code`.\n\n- one\n- two\n\n> quote\n\n```js\nconst count = 2;\n```');
 for(const part of ['<h2>Task</h2>','<strong>Important</strong>','<em>optional</em>','<code>code</code>','<ul><li>one</li><li>two</li></ul>','<blockquote>quote</blockquote>','syntax-keyword']) assert.ok(output.includes(part),part);
});
test('escapes raw HTML and rejects executable link schemes',()=>{
 const output=markdown('<img src=x onerror=alert(1)>\n\n[unsafe](javascript:alert)\n\n[safe](https://example.com)\n\n```js\n"<script>"\n```');
 assert.ok(!output.includes('<img'));
 assert.ok(!output.includes('<script>'));
 assert.ok(!output.includes('href="javascript:'));
 assert.ok(output.includes('href="https://example.com"'));
});
test('formatting preserves surrounding text and selects the formatted content',()=>{
 const result=formatSelection('first hello last',6,11,'bold');
 assert.equal(result.text,'first **hello** last');
 assert.equal(result.text.slice(result.start,result.end),'hello');
 assert.equal(formatSelection('one\ntwo',0,7,'list').text,'- one\n- two');
});
