// The command center: Message / Draw tabs, the composer form, and a /btw answer.
import { Suspense, lazy, useLayoutEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from 'react';
import * as stylex from '@stylexjs/stylex';
import { bindEditor } from '../editor.ts';
import { composerPlaceholder } from '../composer-target.ts';
import { activeAgents } from '../model.ts';
import { fileSize } from '../attachments.ts';
import { colors, breakpoints, fonts } from './theme/tokens.stylex.ts';
import { Button } from './kit.tsx';
import { Icon } from './Icon.tsx';
import { Badge } from './AgentCard.tsx';
import { useShallow } from 'zustand/react/shallow';
import { useApp, get, toast, workspaceOf } from './state/store.ts';
import {
  modelOptions, currentChoice, setModel, onText, chooseCommand, closeCommandMenu, clearComposer, submitCommand, closeBtw,
  addFiles, removeFile, registerInput, getDraft,
} from './state/composer.ts';
import { detachRecipient } from './state/agents.ts';
import { detachProject } from './state/projects.ts';
import { showMessageComposer, showDraw } from './state/panels.ts';
import { toggleDictation } from './state/dictation.ts';
import { DICTATION_OFF } from '../dictation-format.ts';
import { Waveform } from './Waveform.tsx';
import { DictationStatus, dictateStyles } from './DictationStatus.tsx';
import type { XhaosInput } from '../rich-input.ts';
import { useNarrow } from './narrow.ts';
import { zoomOf } from './zoom.ts';

// JSX knows <xhaos-input> (defined in rich-input.ts).
declare module 'react' {
  // eslint-disable-next-line @typescript-eslint/no-namespace
  namespace JSX {
    interface IntrinsicElements {
      'xhaos-input': React.DetailedHTMLProps<React.HTMLAttributes<XhaosInput>, XhaosInput>;
    }
  }
}

function ModelPicker() {
  // Re-render when the choices can change: providers, the saved choice, the backend.
  useApp(useShallow(s => [s.providers, s.composerModelId, s.backendVersion]));
  const hidden = useApp(s => s.mode !== 'message' || s.selected.size > 0);
  const backend = useApp(s => s.backendVersion !== null);
  const options = modelOptions(), current = currentChoice();
  return (
    <select id="model-pick" name="profile" aria-label="Model for new agents" hidden={hidden}
      value={current?.id || ''} onChange={event => setModel(event.target.value)} {...stylex.props(form.model)}>
      {options.length ? options.map(choice => <option key={choice.id} value={choice.id}>{choice.label}</option>) : <option value="">No model provider</option>}
      {backend && <option value="__providers">Add or edit providers…</option>}
    </select>
  );
}

function RecipientChips() {
  const selected = useApp(s => s.selected);
  // Select stable references; derive arrays outside the selector.
  const agents = activeAgents(useApp(s => workspaceOf(s).agents));
  const chips = [...selected].map(id => agents.find(a => a.id === id)).filter(agent => agent !== undefined);
  const project = useApp(s => s.projects.find(p => p.id === s.attachedProject));
  if (!chips.length && !project) return null;
  return (
    <div id="composer-targets" {...stylex.props(form.targets)}>
      {project && (
        <span data-recipient-project={project.id} title={project.path} {...stylex.props(form.recipient)}>
          <Icon name="folder" size={12} />
          <span {...stylex.props(form.ellipsis)}>{project.name}</span>
          <Button data-detach-project={project.id} aria-label={`Detach project ${project.name}`} onClick={detachProject} sx={form.chipButton}>×</Button>
        </span>
      )}
      {chips.map(agent => (
        <span key={agent.id} data-recipient={agent.id} {...stylex.props(form.recipient)}>
          <Badge agent={agent} />
          <span title={agent.title} {...stylex.props(form.ellipsis)}>#{agent.id} {agent.title}</span>
          <Button data-detach-agent={agent.id} aria-label={`Detach agent ${agent.id}`} onClick={() => detachRecipient(agent.id)} sx={form.chipButton}>×</Button>
        </span>
      ))}
    </div>
  );
}

function PendingFiles() {
  const files = useApp(s => s.pendingFiles);
  if (!files.length) return null;
  return (
    <div data-pending="command" {...stylex.props(form.files)}>
      {files.map((file, index) => (
        <span key={`${file.name}-${index}`} {...stylex.props(form.file)}>
          <span title={file.type || 'File'} {...stylex.props(form.ellipsis)}>{file.name} <small {...stylex.props(form.fileSize)}>{fileSize(file.size)}</small></span>
          <Button data-remove={index} aria-label={`Remove ${file.name}`} onClick={() => removeFile(index)} sx={form.chipButton}>×</Button>
        </span>
      ))}
    </div>
  );
}

// The slash-command menu drops below the composer when there is room, and
// opens upward otherwise.
function CommandMenu() {
  const items = useApp(s => s.menu);
  const menu = useRef<HTMLDivElement>(null);
  const [place, setPlace] = useState({ upward: false, maxHeight: 300 });
  useLayoutEffect(() => {
    const el = menu.current;
    if (!el || !items.length) return;
    const form = el.parentElement!.getBoundingClientRect(), z = zoomOf(el.parentElement!);
    // Room in screen pixels, turned into the menu's own (zoomed) pixels.
    const below = (window.innerHeight - form.bottom - 12) / z, above = (form.top - 12) / z;
    const upward = !(below >= Math.min(160, el.scrollHeight) || below >= above);
    const maxHeight = Math.min(300, upward ? above : below);
    if (upward !== place.upward || maxHeight !== place.maxHeight) setPlace({ upward, maxHeight });
  });
  const onKey = (event: ReactKeyboardEvent<HTMLButtonElement>, index: number) => {
    const buttons = [...event.currentTarget.parentElement!.querySelectorAll('button')];
    if (event.key === 'Escape') { closeCommandMenu(); document.getElementById('command')?.focus(); }
    // Enter (and Tab) complete the command themselves: left to the button,
    // Enter would click it on keydown and then insert a new line into the
    // message box, which has the focus by the time the key's input lands.
    if ((event.key === 'Enter' || event.key === 'Tab') && !event.shiftKey) { event.preventDefault(); chooseCommand(buttons[index].dataset.command!); }
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault();
      buttons[(index + (event.key === 'ArrowDown' ? 1 : -1) + buttons.length) % buttons.length].focus();
    }
  };
  return (
    <div id="command-menu" ref={menu} aria-label="Slash commands" hidden={!items.length} {...stylex.props(form.menu, place.upward && form.menuUp, form.menuHeight(place.maxHeight))}>
      {items.map(([name, description], index) => (
        <button key={name} type="button" data-command={name} onClick={() => chooseCommand(name)} onKeyDown={event => onKey(event, index)} {...stylex.props(form.menuItem)}>
          <code {...stylex.props(form.menuName)}>/{name}</code><span {...stylex.props(form.menuText)}>{description}</span>
        </button>
      ))}
    </div>
  );
}

// The composer's rich-text field is the <xhaos-input> custom element, which
// owns its content; React renders it empty and never touches its children.
let lastEscape = 0;
function onCommandKey(event: KeyboardEvent) {
  const target = event.target as XhaosInput;
  if (event.key === 'Escape') {
    // Each Escape closes one thing; preventDefault keeps keys.ts from closing more.
    if (get().menu.length) { event.preventDefault(); closeCommandMenu(); lastEscape = 0; return; }
    if (get().btw) { event.preventDefault(); closeBtw(); lastEscape = 0; return; }
    // Esc, Esc: clear the composer text (attachments stay).
    if (Date.now() - lastEscape < 450) { lastEscape = 0; if (getDraft() || target.value) { clearComposer(); toast('Composer cleared.'); } return; }
    lastEscape = Date.now(); return;
  }
  lastEscape = 0;
  if (event.key === 'ArrowDown' && get().menu.length) { event.preventDefault(); document.querySelector<HTMLElement>('#command-menu button')?.focus(); }
  if (event.key === 'Enter' && !event.shiftKey) { event.preventDefault(); submitCommand(target.value); }
}

function ComposerForm() {
  const input = useRef<XhaosInput>(null);
  const holder = useRef<HTMLFormElement>(null);
  const files = useRef<HTMLInputElement>(null);
  const [dragging, setDragging] = useState(false);
  const hidden = useApp(s => s.mode !== 'message');
  const sending = useApp(s => s.sending);
  const recording = useApp(s => s.recording);
  const transcribing = useApp(s => s.transcribing);
  const dictationEnabled = useApp(s => s.dictationEnabled);
  const selected = useApp(s => s.selected);
  const projectName = useApp(s => s.projects.find(p => p.id === s.attachedProject)?.name);
  useLayoutEffect(() => {
    registerInput(input.current);
    const unbind = bindEditor(input.current!, holder.current!, {
      addFiles, setText: () => {}, onText: () => onText(input.current!.value), onKeyDown: onCommandKey, onDragging: setDragging, notify: toast,
    });
    return () => { unbind(); registerInput(null); };
  }, []);
  return (
    <form id="command-form" ref={holder} hidden={hidden} inert={sending || undefined} aria-busy={sending}
      onSubmit={event => { event.preventDefault(); submitCommand(input.current!.value); }} {...stylex.props(form.root, dragging && form.dragging)}>
      <RecipientChips />
      <xhaos-input ref={input} id="command" contentEditable="true" role="textbox" aria-multiline="true" aria-label="Agent command"
        data-placeholder={composerPlaceholder([...selected], projectName)} spellCheck="true" suppressContentEditableWarning {...stylex.props(form.input)} />
      <PendingFiles />
      {/* The bottom row: attach and model, the dictation line, then the mic and send. */}
      <div {...stylex.props(form.bar)}>
      <div data-editor="command" {...stylex.props(form.tools)}>
        <Button data-pick title="Attach files" aria-label="Attach files" onClick={() => files.current?.click()} sx={form.pick}>＋</Button>
        <input ref={files} type="file" data-files multiple hidden onChange={event => { addFiles([...(event.target.files || [])]); event.target.value = ''; }} />
        <ModelPicker />
      </div>
      <Waveform />
      <div {...stylex.props(form.actions)}>
        <DictationStatus />
        {dictationEnabled !== null && (
          <Button variant="icon" id="dictate" aria-label={recording ? 'Stop dictation' : transcribing ? 'Transcribing' : 'Dictate command'} aria-pressed={recording} aria-busy={transcribing || undefined}
            aria-disabled={!dictationEnabled || undefined} title={dictationEnabled ? undefined : DICTATION_OFF}
            disabled={transcribing} onClick={toggleDictation} chosen={recording}
            sx={transcribing ? dictateStyles.busy : !dictationEnabled ? dictateStyles.off : undefined}>
            <Icon name="mic" size={16} />
            {transcribing && <span aria-hidden="true" {...stylex.props(dictateStyles.ring)} />}
          </Button>
        )}
        <Button variant="send" type="submit" id="send" aria-label="Send command"><Icon name="send" size={15} /></Button>
      </div>
      </div>
      <CommandMenu />
    </form>
  );
}

// Excalidraw is large, so the Draw tab loads it on first use.
const Draw = lazy(() => import('./Draw.tsx'));
function DrawingPane() {
  const drawing = useApp(s => s.mode === 'draw');
  if (!drawing) return null;
  return (
    <section id="drawing-pane" {...stylex.props(area.drawing)}>
      <Suspense fallback={<p {...stylex.props(aside.muted)}>Loading the drawing board…</p>}><Draw /></Suspense>
    </section>
  );
}

export function ComposerArea() {
  const narrow = useNarrow(); // a phone has no Draw tab: the canvas needs room and a pointer
  const mode = useApp(s => (narrow ? 'message' : s.mode));
  return (
    <div id="composer-area" {...stylex.props(area.root)}>
      <div {...stylex.props(area.switches)}>
        {/* The logo leaves this workspace: this browser is signed out of it
            (its agents keep working) and the workspace picker shows. */}
        <a href="/" id="leave-workspace" aria-label="Leave this workspace (lock it in this browser)" title="Leave this workspace: lock it in this browser and pick another" onClick={leaveWorkspace} {...stylex.props(area.brand)}>x<span {...stylex.props(area.brandTail)}>h</span></a>
        <Button id="show-message" onClick={showMessageComposer} sx={[area.switch, mode === 'message' && area.switchChosen]}>Message</Button>
        {!narrow && <Button id="show-drawing" onClick={showDraw} sx={[area.switch, mode === 'draw' && area.switchChosen]}>Draw</Button>}
      </div>
      <ComposerForm />
      <DrawingPane />
    </div>
  );
}

async function leaveWorkspace(event: React.MouseEvent) {
  if (!get().backendVersion) return; // the prototype: a plain link
  event.preventDefault();
  await fetch('/xhaos/auth/signout', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' }).catch(() => {});
  location.href = '/';
}

const area = stylex.create({
  root: {
    position: 'relative',
    display: 'flex',
    flexDirection: 'column',
    gap: 4,
    paddingTop: 4,
    paddingBottom: 8,
    paddingInline: { default: 16, [breakpoints.narrow]: 10 },
    flexGrow: 1,
    minHeight: 0,
  },
  switches: { display: 'flex', alignItems: 'center', alignSelf: 'stretch', gap: 14, minHeight: 23, flexShrink: 0, paddingRight: 10 },
  brand: { textDecoration: 'none', color: colors.textStrong, fontSize: 22, letterSpacing: -5, fontWeight: 900, lineHeight: 1, paddingRight: 10 },
  brandTail: { color: colors.textMuted },
  switch: { display: 'flex', alignItems: 'center', gap: 7, color: colors.textFaint, fontSize: 10 },
  switchChosen: { color: colors.text },
  drawing: { display: 'flex', flexDirection: 'column', flexGrow: 1, minHeight: 0, gap: 7, overflow: 'hidden' },
});

const form = stylex.create({
  root: {
    position: 'relative',
    width: '100%',
    minHeight: 76,
    flexGrow: 1,
    flexShrink: 0,
    flexBasis: 'auto',
    display: 'flex',
    flexDirection: 'column',
    gap: 6,
    paddingBlock: 9,
    paddingInline: 11,
    borderRadius: 5,
    backgroundColor: 'transparent',
  },
  dragging: { backgroundColor: colors.attached },
  input: { flexGrow: 1, flexShrink: 0, flexBasis: '1.65em', minHeight: '1.65em', overflowY: 'auto', padding: 0, fontFamily: fonts.mono },
  targets: { display: 'flex', gap: 6, flexWrap: 'nowrap', overflow: 'auto', maxHeight: 32, flexShrink: 0 },
  recipient: { display: 'inline-flex', alignItems: 'center', gap: 7, paddingBlock: 4, paddingInline: 7, borderRadius: 3, backgroundColor: colors.selected, fontSize: 10, flexShrink: 0, maxWidth: 270 },
  ellipsis: { whiteSpace: 'nowrap', textOverflow: 'ellipsis', overflow: 'hidden' },
  chipButton: { paddingBlock: 0, paddingInline: 2, color: colors.textMuted, fontSize: 12 },
  files: { display: 'flex', flexWrap: 'nowrap', gap: 5, maxHeight: 26, overflow: 'auto', flexShrink: 0 },
  file: { display: 'inline-flex', alignItems: 'center', gap: 6, backgroundColor: colors.selected, borderRadius: 3, paddingBlock: 3, paddingInline: 6, fontSize: 10, flexShrink: 0, maxWidth: 260 },
  fileSize: { color: colors.textFaint, fontSize: 9, marginLeft: 4 },
  bar: { display: 'flex', alignItems: 'center', minWidth: 0, flexShrink: 0 },
  // At most 60% of the row, so a long model name leaves room for the line.
  tools: { display: 'flex', alignItems: 'center', gap: 3, flexShrink: 1, minWidth: 0, maxWidth: '60%', overflowX: 'auto', scrollbarWidth: 'thin', minHeight: 28 },
  pick: { fontSize: 18, width: 26, height: 26, padding: 0, margin: 0, color: colors.textDim },
  model: { minWidth: 0, maxWidth: '100%', textOverflow: 'ellipsis', marginLeft: 8, fontSize: 10, backgroundColor: 'transparent', color: colors.textSub, borderWidth: 0, paddingBlock: 6, paddingInline: 4 },
  actions: { display: 'flex', gap: 8, alignItems: 'center', flexShrink: 0, marginLeft: 'auto' },
  menu: {
    position: 'absolute',
    left: 0,
    top: 'calc(100% + 5px)',
    width: 'min(420px, 100%)',
    overflow: 'auto',
    backgroundColor: colors.menu,
    borderWidth: 1,
    borderStyle: 'solid',
    borderColor: colors.lineStrong,
    borderRadius: 5,
    boxShadow: `0 10px 25px ${colors.shadow}`,
    zIndex: 30,
    padding: 4,
  },
  menuUp: { top: 'auto', bottom: 'calc(100% + 5px)' },
  menuHeight: (maxHeight: number) => ({ maxHeight }),
  menuItem: {
    display: 'flex',
    gap: 18,
    width: '100%',
    textAlign: 'left',
    padding: 9,
    borderRadius: 3,
    alignItems: 'center',
    borderWidth: 0,
    cursor: 'pointer',
    color: 'inherit',
    fontFamily: 'inherit',
    backgroundColor: { default: 'transparent', ':hover': colors.hover, ':focus': colors.control },
  },
  menuName: { minWidth: 65, color: colors.textStrong, fontSize: 11 },
  menuText: { color: colors.textDim, fontSize: 11 },
});

const aside = stylex.create({
  muted: { color: colors.textFaint, margin: 0, fontSize: 12 },
});
