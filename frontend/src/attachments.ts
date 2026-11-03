import type { Attachment } from './types.ts';

interface StoredFile extends Attachment { blob: Blob }
let connection: Promise<IDBDatabase> | undefined;
function database(): Promise<IDBDatabase> {
  if (!globalThis.indexedDB) return Promise.reject(new Error('File storage is unavailable in this browser.'));
  return connection ||= new Promise((resolve, reject) => {
    const request = indexedDB.open('xhaos-files', 1);
    request.onupgradeneeded = () => request.result.createObjectStore('files', { keyPath: 'id' });
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => { connection = undefined; reject(request.error); };
  });
}
export async function storeFiles(files: File[]): Promise<Attachment[]> {
  if (!files.length) return [];
  const db = await database();
  const records: StoredFile[] = files.map(file => ({ id: crypto.randomUUID(), name: file.name, size: file.size, type: file.type, blob: file }));
  await new Promise<void>((resolve, reject) => {
    const transaction = db.transaction('files', 'readwrite');
    transaction.oncomplete = () => resolve();
    transaction.onabort = () => reject(transaction.error || new Error('Could not save attachments.'));
    transaction.onerror = () => {};
    records.forEach(record => transaction.objectStore('files').add(record));
  });
  return records.map(({ blob, ...metadata }) => metadata);
}
export async function readFile(id: string): Promise<StoredFile> {
  const db = await database();
  return new Promise((resolve, reject) => {
    const request = db.transaction('files').objectStore('files').get(id);
    request.onsuccess = () => request.result ? resolve(request.result) : reject(new Error('Attachment is missing from browser storage.'));
    request.onerror = () => reject(request.error);
  });
}
export const shouldAttachText = (text: string): boolean => text.length >= 4000 || text.split('\n').length >= 40;
export function textFile(text: string): File { return new File([text], `pasted-text-${Date.now()}.txt`, {type:'text/plain'}); }
export function fileSize(size: number): string { return size < 1024 ? `${size} B` : size < 1048576 ? `${(size/1024).toFixed(1)} KB` : `${(size/1048576).toFixed(1)} MB`; }
