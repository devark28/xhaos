import test from 'node:test';
import assert from 'node:assert/strict';
import { markdown, formatSelection } from './markdown.ts';
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

test('GitHub tables render as tables, with alignment and inline Markdown in cells', () => {
  const html = markdown('Plan:\n\n| Need | Choice |\n|---|--:|\n| IdP | **Ory Hydra** (`a|b`) |\n| UI | your app |\n\nAfter.');
  assert.match(html, /<table><thead><tr><th>Need<\/th><th style="text-align:right">Choice<\/th><\/tr><\/thead><tbody><tr><td>IdP<\/td><td style="text-align:right"><strong>Ory Hydra<\/strong> \(<code>a\|b<\/code>\)<\/td><\/tr>/);
  assert.match(html, /<p>After\.<\/p>$/);
  assert.equal(markdown('a | b\nnot a table'), '<p>a | b<br>not a table</p>');
  assert.ok(!markdown('a | b\n---').includes('<table>'), 'a rule under a line with a pipe is not a table');
});

test('agent links only where asked, never inside code or entities', () => {
  const md = 'see #12 and `#3`, it\'s #4.';
  assert.ok(!markdown(md).includes('data-agent-link'));
  const html = markdown(md, { agentLinks: true });
  assert.ok(html.includes('<a href="#" data-agent-link="12">#12</a>'));
  assert.ok(html.includes('<a href="#" data-agent-link="4">#4</a>'));
  assert.ok(html.includes('<code>#3</code>'));
  assert.ok(html.includes('&#39;'));
  assert.ok(!markdown('a#5 b/#6').includes('data-agent-link'));
});
