import test from 'node:test';
import assert from 'node:assert/strict';
import { serializeRichText } from './rich-input.ts';
import { markdown } from './markdown.ts';
const text=value=>({nodeType:3,textContent:value});
const element=(tag,children,style={})=>({nodeType:1,tagName:tag,childNodes:children,style});
test('native bold and italic serialize as model-facing Markdown',()=>{
 assert.equal(serializeRichText(element('B',[text('important')])),'**important**');
 assert.equal(serializeRichText(element('SPAN',[text('important')],{fontWeight:'700'})),'**important**');
 assert.equal(serializeRichText(element('I',[text('quiet')])),'*quiet*');
 const nested=serializeRichText(element('STRONG',[element('EM',[text('both')])]));
 assert.equal(nested,'***both***');
 assert.equal(markdown(nested),'<p><strong><em>both</em></strong></p>');
});
test('rich input shows formatting, escapes text, and drops empty marks and scripts',()=>{
 const output=markdown('**bold** <unsafe>');
 assert.ok(output.includes('<strong>bold</strong>'));
 assert.ok(!output.includes('**bold**'));
 assert.ok(output.includes('&lt;unsafe&gt;'));
 assert.equal(serializeRichText(element('B',[])),'');
 assert.equal(serializeRichText(element('SCRIPT',[text('alert(1)')])),'');
});
