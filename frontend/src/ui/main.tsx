import { createRoot } from 'react-dom/client';
import './fonts.css';
import './global.css';
import '../rich-input.ts'; // defines <xhaos-input>
import { installTypingFlow } from '../input-focus.ts';
import { App } from './App.tsx';
import { blobUrls } from './Transcript.tsx';
import { installKeys } from './keys.ts';
import { start } from './state/backend.ts';
import { SignIn, authState } from './SignIn.tsx';
import { applyAppearance } from './appearance.ts';

applyAppearance(); // before the first paint, so there is no flash of the default theme

const root = createRoot(document.getElementById('app')!);
// Served by Xhaos: nothing loads until this browser is signed in.
const auth = await authState();
if (auth && !auth.signedIn) {
  root.render(<SignIn auth={auth} />);
} else {
  root.render(<App />);
  installKeys();
  installTypingFlow(document, window);
  window.addEventListener('beforeunload', () => blobUrls.forEach(url => URL.revokeObjectURL(url)));
  start(auth !== null); // served by Xhaos when the auth endpoint answered
}
