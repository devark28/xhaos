import test from 'node:test';
import assert from 'node:assert/strict';
import { drawingSvg, normalizeScene } from './drawing.js';
test('sketch exports retain shapes and escape text without external assets',()=>{
 const svg=drawingSvg({background:'#151515',elements:[{type:'rect',x:10,y:20,x2:200,y2:100,color:'#eeeeee',width:3},{type:'arrow',x:30,y:40,x2:80,y2:90,color:'#389cff',width:3},{type:'text',x:10,y:20,text:'<script>alert(1)</script>',color:'#eeeeee'}]});
 assert.ok(svg.includes('<rect x="10" y="20"'));
 assert.ok(svg.includes('&lt;script&gt;'));
 assert.ok(!svg.includes('<script>'));
 assert.ok(svg.includes('viewBox="0 0 1200 700"'));
});
test('persisted scene data cannot inject attributes or network-backed paint',()=>{
 const scene=normalizeScene({background:'url(https://example.com)',elements:[{type:'rect',x:'" onload="bad',color:'url(https://example.com)',width:-3},{type:'script'}]});
 assert.equal(scene.background,'#151515');
 assert.equal(scene.elements.length,1);
 assert.equal(scene.elements[0].x,0);
 assert.equal(scene.elements[0].width,1);
 assert.ok(!drawingSvg(scene).includes('onload='));
 assert.ok(!drawingSvg(scene).includes('https://'));
});
