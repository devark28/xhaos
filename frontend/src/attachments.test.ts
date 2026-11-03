import test from 'node:test';
import assert from 'node:assert/strict';
import { shouldAttachText, textFile, storeFiles } from './attachments.ts';
test('large pastes become text files; ordinary messages stay inline',async()=>{
 assert.equal(shouldAttachText('A short message'),false);
 assert.equal(shouldAttachText('x'.repeat(4000)),true);
 assert.equal(shouldAttachText(Array(40).fill('line').join('\n')),true);
 const file=textFile('full pasted content');
 assert.equal(file.type,'text/plain');
 assert.match(file.name,/\.txt$/);
 assert.equal(await file.text(),'full pasted content');
});
test('empty submissions do not need file storage; unavailable storage fails explicitly',async()=>{
 assert.deepEqual(await storeFiles([]),[]);
 if(!globalThis.indexedDB)await assert.rejects(storeFiles([textFile('data')]),/storage is unavailable/);
});
