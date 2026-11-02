let connection;
function database() {
  if (!globalThis.indexedDB) return Promise.reject(new Error('File storage is unavailable in this browser.'));
  return connection ||= new Promise((resolve, reject) => {
    const request = indexedDB.open('xhaos-files', 1);
    request.onupgradeneeded = () => request.result.createObjectStore('files', { keyPath: 'id' });
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => { connection = undefined; reject(request.error); };
  });
}
export async function storeFiles(files) {
  if (!files.length) return [];
  const db = await database();
  const records = files.map(file => ({ id: crypto.randomUUID(), name: file.name, size: file.size, type: file.type, blob: file }));
  await new Promise((resolve, reject) => {
    const transaction = db.transaction('files', 'readwrite');
    transaction.oncomplete = resolve;
    transaction.onabort = () => reject(transaction.error || new Error('Could not save attachments.'));
    transaction.onerror = () => {};
    records.forEach(record => transaction.objectStore('files').add(record));
  });
  return records.map(({ blob, ...metadata }) => metadata);
}
export async function readFile(id) {
  const db = await database();
  return new Promise((resolve, reject) => {
    const request = db.transaction('files').objectStore('files').get(id);
    request.onsuccess = () => request.result ? resolve(request.result) : reject(new Error('Attachment is missing from browser storage.'));
    request.onerror = () => reject(request.error);
  });
}
export const shouldAttachText = text => text.length >= 4000 || text.split('\n').length >= 40;
export function textFile(text) { return new File([text], `pasted-text-${Date.now()}.txt`, {type:'text/plain'}); }
export function fileSize(size) { return size < 1024 ? `${size} B` : size < 1048576 ? `${(size/1024).toFixed(1)} KB` : `${(size/1048576).toFixed(1)} MB`; }
