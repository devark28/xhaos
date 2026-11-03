import { createRoot } from 'react-dom/client';
import './global.css';
import '../rich-input.ts'; // defines <xhaos-input>
import { installTypingFlow } from '../input-focus.ts';
import { App } from './App.tsx';
import { blobUrls } from './Transcript.tsx';
import { installKeys } from './keys.ts';
import { start } from './state/backend.ts';

createRoot(document.getElementById('app')!).render(<App />);
installKeys();
installTypingFlow(document, window);
window.addEventListener('beforeunload', () => blobUrls.forEach(url => URL.revokeObjectURL(url)));
start();
