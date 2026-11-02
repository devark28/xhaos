import test from 'node:test';
import assert from 'node:assert/strict';
import { watchStickyColumns } from './sticky-columns.js';
test('columns stop at their own limit and respond to content and viewport resizing', () => {
 const scroller = { clientHeight: 600 };
 const column = height => ({ offsetHeight: height, style: { setProperty(name, value) { this[name] = value; } } });
 const short = column(200), tall = column(900);
 let resize, disconnected=false;
 const observed=[];
 class Observer {
  constructor(callback) { resize=callback; }
  observe(element) { observed.push(element); }
  disconnect() { disconnected=true; }
 }
 const stop=watchStickyColumns(scroller,[short,tall],Observer);
 assert.deepEqual(observed,[scroller,short,tall]);
 assert.equal(short.style['--column-sticky-top'],'0px');
 assert.equal(tall.style['--column-sticky-top'],'-316px');
 short.offsetHeight=700;
 resize();
 assert.equal(short.style['--column-sticky-top'],'-116px');
 scroller.clientHeight=1000;
 resize();
 assert.equal(short.style['--column-sticky-top'],'0px');
 assert.equal(tall.style['--column-sticky-top'],'0px');
 stop();
 assert.equal(disconnected,true);
});
