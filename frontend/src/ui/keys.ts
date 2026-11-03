// Page-wide keyboard and pointer behavior, installed once at startup.
import { S, closePanel, closeBtw, focusComposer, focusNewAgent, showMessageComposer } from './store.ts';

const openMenus = () => [...document.querySelectorAll<HTMLDetailsElement>('[data-agent-menu][open]')];

export function installKeys() {
  // Clicking anywhere else closes open agent menus.
  window.addEventListener('click', event => {
    if (!(event.target as Element).closest('[data-agent-menu]')) openMenus().forEach(menu => { menu.open = false; });
  });
  window.addEventListener('keydown', event => {
    // Escape closes the topmost open thing: an agent menu, then a panel, then
    // the /btw answer. Fields that use Escape themselves call preventDefault.
    if (event.key === 'Escape' && !event.defaultPrevented) {
      const menus = openMenus();
      if (menus.length) { menus.forEach(menu => { menu.open = false; }); menus[0].querySelector('summary')?.focus({ preventScroll: true }); return; }
      if (S.panel) { closePanel(); focusComposer(); return; }
      if (S.btw) { closeBtw(); return; }
    }
    // Ctrl/Cmd+Alt+N: a new agent. Ctrl/Cmd+K: back to the message composer.
    if (event.key.toLowerCase() === 'n' && (event.ctrlKey || event.metaKey) && event.altKey) { event.preventDefault(); focusNewAgent(); }
    if ((event.ctrlKey || event.metaKey) && event.key === 'k') { event.preventDefault(); showMessageComposer(); }
  });
}
