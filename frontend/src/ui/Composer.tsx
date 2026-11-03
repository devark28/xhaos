// The command center below the ribbon: Message / Draw / Questions tabs, the
// composer form, a /btw answer, and panels that take its place when open.
import { useEffect, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from 'react';
import * as stylex from '@stylexjs/stylex';
import { bindEditor } from '../editor.ts';
import { drawingView, bindDrawing } from '../drawing.ts';
import { composerPlaceholder } from '../composer-target.ts';
import { markdown } from '../markdown.ts';
import { activeAgents } from '../model.ts';
import { fileSize } from '../attachments.ts';
import { colors, breakpoints } from './theme/tokens.stylex.ts';
import { Button, StatusDot } from './kit.tsx';
import { Icon } from './Icon.tsx';
import { Badge } from './AgentCard.tsx';
import { Panel } from './Panels.tsx';
import { QuestionInbox } from './QuestionInbox.tsx';
import {
  S, bump, toast, workspace, pendingQuestions, modelOptions, currentChoice, setModel, updateCommandMenu, chooseCommand,
  closeCommandMenu, clearComposer, submitCommand, closeBtw, detachRecipient, showMessageComposer, showDraw, showQuestions, dictate,
  addFiles, removeFile,
} from './store.ts';
import type { XhaosInput } from '../rich-input.ts';

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
  const options = modelOptions(), current = currentChoice();
  return (
    <select id="model-pick" name="profile" aria-label="Model for new agents" hidden={S.mode !== 'message' || S.selected.size > 0}
      value={current?.id || ''} onChange={event => setModel(event.target.value)} {...stylex.props(form.model)}>
      {options.length ? options.map(choice => <option key={choice.id} value={choice.id}>{choice.label}</option>) : <option value="">No model provider</option>}
      {S.backend && <option value="__providers">Add or edit providers…</option>}
    </select>
  );
}

function RecipientChips() {
  const agents = activeAgents(workspace().agents);
  const chips = [...S.selected].map(id => agents.find(a => a.id === id)).filter(agent => agent !== undefined);
  if (!chips.length) return null;
  return (
    <div id="composer-targets" {...stylex.props(form.targets)}>
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
  if (!S.pendingFiles.length) return null;
  return (
    <div data-pending="command" {...stylex.props(form.files)}>
      {S.pendingFiles.map((file, index) => (
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
  const menu = useRef<HTMLDivElement>(null);
  const [place, setPlace] = useState({ upward: false, maxHeight: 300 });
  useLayoutEffect(() => {
    const el = menu.current;
    if (!el || !S.menu.length) return;
    const form = el.parentElement!.getBoundingClientRect();
    const below = window.innerHeight - form.bottom - 12, above = form.top - 12;
    const upward = !(below >= Math.min(160, el.scrollHeight) || below >= above);
    const maxHeight = Math.min(300, upward ? above : below);
    if (upward !== place.upward || maxHeight !== place.maxHeight) setPlace({ upward, maxHeight });
  });
  const onKey = (event: ReactKeyboardEvent<HTMLButtonElement>, index: number) => {
    const buttons = [...event.currentTarget.parentElement!.querySelectorAll('button')];
    if (event.key === 'Escape') { closeCommandMenu(); document.getElementById('command')?.focus(); }
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault();
      buttons[(index + (event.key === 'ArrowDown' ? 1 : -1) + buttons.length) % buttons.length].focus();
    }
  };
  return (
    <div id="command-menu" ref={menu} aria-label="Slash commands" hidden={!S.menu.length} {...stylex.props(form.menu, place.upward && form.menuUp, form.menuHeight(place.maxHeight))}>
      {S.menu.map(([name, description], index) => (
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
    if (S.menu.length) { event.preventDefault(); closeCommandMenu(); lastEscape = 0; return; }
    if (S.btw) { event.preventDefault(); closeBtw(); lastEscape = 0; return; }
    // Esc, Esc: clear the composer text (attachments stay).
    if (Date.now() - lastEscape < 450) { lastEscape = 0; if (S.draft || target.value) { clearComposer(); toast('Composer cleared.'); } return; }
    lastEscape = Date.now(); return;
  }
  lastEscape = 0;
  if (event.key === 'ArrowDown' && S.menu.length) { event.preventDefault(); document.querySelector<HTMLElement>('#command-menu button')?.focus(); }
  if (event.key === 'Enter' && !event.shiftKey) { event.preventDefault(); submitCommand(target.value); }
}

function ComposerForm() {
  const input = useRef<XhaosInput>(null);
  const holder = useRef<HTMLFormElement>(null);
  const files = useRef<HTMLInputElement>(null);
  const [dragging, setDragging] = useState(false);
  useLayoutEffect(() => {
    input.current!.value = S.draft;
    return bindEditor(input.current!, holder.current!, {
      addFiles, setText: value => { S.draft = value; }, onText: updateCommandMenu, onKeyDown: onCommandKey, onDragging: setDragging, notify: toast,
    });
  }, []);
  return (
    <form id="command-form" ref={holder} hidden={S.mode !== 'message' || Boolean(S.panel)} inert={S.sending || undefined} aria-busy={S.sending}
      onSubmit={event => { event.preventDefault(); submitCommand(input.current!.value); }} {...stylex.props(form.root, dragging && form.dragging)}>
      <RecipientChips />
      <xhaos-input ref={input} id="command" contentEditable="true" role="textbox" aria-multiline="true" aria-label="Agent command"
        data-placeholder={composerPlaceholder([...S.selected])} spellCheck="true" suppressContentEditableWarning {...stylex.props(form.input)} />
      <PendingFiles />
      <div data-editor="command" {...stylex.props(form.tools)}>
        <Button data-pick title="Attach files" aria-label="Attach files" onClick={() => files.current?.click()} sx={form.pick}>＋</Button>
        <input ref={files} type="file" data-files multiple hidden onChange={event => { addFiles([...(event.target.files || [])]); event.target.value = ''; }} />
        <ModelPicker />
      </div>
      <div {...stylex.props(form.actions)}>
        <Button variant="icon" id="dictate" aria-label="Dictate command" onClick={dictate} chosen={S.recording}><Icon name="mic" size={16} /></Button>
        <Button variant="send" type="submit" id="send" aria-label="Send command"><Icon name="send" size={15} /></Button>
      </div>
      <CommandMenu />
    </form>
  );
}

// The latest /btw answer, above the composer. It is not part of any agent.
function BtwAside() {
  const btw = S.btw;
  if (!btw || S.mode !== 'message' || S.panel) return null;
  return (
    <aside id="btw" aria-live="polite" aria-label="Side answer" {...stylex.props(aside.root)}>
      <div {...stylex.props(aside.heading)}>
        <span {...stylex.props(aside.kind)}>btw</span>
        <span {...stylex.props(aside.question)}>{btw.question}</span>
        <Button variant="icon" id="close-btw" aria-label="Dismiss side answer" onClick={closeBtw}><Icon name="close" size={12} /></Button>
      </div>
      {btw.pending ? <p {...stylex.props(aside.muted)}>Reading the agents…</p>
        : btw.error ? <p {...stylex.props(aside.error)}>Could not answer: {btw.error}</p>
        : <div className="markdown-body" data-answer dangerouslySetInnerHTML={{ __html: markdown(btw.answer || '') }} />}
    </aside>
  );
}

function DrawingPane() {
  const markup = useMemo(() => ({ __html: drawingView() }), []);
  const drawing = S.mode === 'draw';
  useEffect(() => {
    if (!drawing) return;
    bindDrawing({
      onAttach: file => { S.pendingFiles.push(file); S.mode = 'message'; bump(); toast('Sketch attached. Add a message and send when ready.'); },
      onBack: showMessageComposer,
      notify: toast,
    });
  }, [drawing]);
  return drawing
    ? <section key="draw" id="drawing-pane" dangerouslySetInnerHTML={markup} {...stylex.props(area.drawing)} />
    : <section key="idle" id="drawing-pane" hidden />;
}

export function ComposerArea() {
  const pending = pendingQuestions();
  const panelOpen = Boolean(S.panel);
  return (
    <div id="composer-area" {...stylex.props(area.root)}>
      <div {...stylex.props(area.switches)}>
        <Button id="show-message" onClick={showMessageComposer} sx={[area.switch, S.mode === 'message' && area.switchChosen]}>Message</Button>
        <Button id="show-drawing" onClick={showDraw} sx={[area.switch, S.mode === 'draw' && area.switchChosen]}>Draw</Button>
        {pending.length > 0 && (
          <Button id="show-questions" onClick={() => showQuestions(S.questionAgentId)} sx={[area.switch, area.attention, S.mode === 'questions' && area.attentionChosen]}>
            <StatusDot status="waiting" /> Questions · {pending.length}
          </Button>
        )}
      </div>
      <Panel />
      {!panelOpen && <BtwAside />}
      <ComposerForm />
      {!panelOpen && S.mode === 'questions' && <section id="question-inbox" {...stylex.props(area.questions)}><QuestionInbox /></section>}
      {!panelOpen && <DrawingPane />}
    </div>
  );
}

const area = stylex.create({
  root: {
    display: 'flex',
    flexDirection: 'column',
    gap: 4,
    paddingTop: 4,
    paddingBottom: 8,
    paddingInline: { default: 16, [breakpoints.narrow]: 10 },
    flexGrow: 1,
    minHeight: 0,
  },
  switches: { display: 'flex', alignItems: 'center', alignSelf: 'stretch', gap: 14, minHeight: 23, flexShrink: 0, paddingInline: 10 },
  switch: { display: 'flex', alignItems: 'center', gap: 7, color: colors.textFaint, fontSize: 10 },
  switchChosen: { color: colors.text },
  attention: { color: colors.brand },
  attentionChosen: { color: colors.brandStrong },
  questions: { flexGrow: 1, minHeight: 0, overflow: 'hidden', display: 'flex', flexDirection: 'column', paddingTop: 0, paddingInline: 11, paddingBottom: 3 },
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
  input: { flexGrow: 1, flexShrink: 0, flexBasis: '1.65em', minHeight: '1.65em', overflowY: 'auto', padding: 0 },
  targets: { display: 'flex', gap: 6, flexWrap: 'nowrap', overflow: 'auto', maxHeight: 32, flexShrink: 0 },
  recipient: { display: 'inline-flex', alignItems: 'center', gap: 7, paddingBlock: 4, paddingInline: 7, borderRadius: 3, backgroundColor: colors.selected, fontSize: 10, flexShrink: 0, maxWidth: 270 },
  ellipsis: { whiteSpace: 'nowrap', textOverflow: 'ellipsis', overflow: 'hidden' },
  chipButton: { paddingBlock: 0, paddingInline: 2, color: colors.textMuted, fontSize: 12 },
  files: { display: 'flex', flexWrap: 'nowrap', gap: 5, maxHeight: 26, overflow: 'auto', flexShrink: 0 },
  file: { display: 'inline-flex', alignItems: 'center', gap: 6, backgroundColor: '#2a2a2a', borderRadius: 3, paddingBlock: 3, paddingInline: 6, fontSize: 10, flexShrink: 0, maxWidth: 260 },
  fileSize: { color: colors.textFaint, fontSize: 9, marginLeft: 4 },
  tools: { display: 'flex', alignItems: 'center', gap: 3, flexShrink: 0, overflowX: 'auto', scrollbarWidth: 'thin', marginRight: 76, minHeight: 28 },
  pick: { fontSize: 18, width: 26, height: 26, padding: 0, margin: 0, color: colors.textDim },
  model: { maxWidth: '65%', marginLeft: 8, fontSize: 10, backgroundColor: 'transparent', color: '#bbbbbb', borderWidth: 0, paddingBlock: 6, paddingInline: 4 },
  actions: { position: 'absolute', bottom: 9, right: 9, display: 'flex', gap: 8, alignItems: 'center' },
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
    backgroundColor: { default: 'transparent', ':hover': colors.hover, ':focus': '#383838' },
  },
  menuName: { minWidth: 65, color: colors.textStrong, fontSize: 11 },
  menuText: { color: colors.textDim, fontSize: 11 },
});

const aside = stylex.create({
  root: {
    flexGrow: 0,
    flexShrink: 1,
    flexBasis: 'auto',
    minHeight: 40,
    margin: 0,
    paddingBlock: 8,
    paddingInline: 10,
    borderWidth: 1,
    borderStyle: 'solid',
    borderColor: colors.line,
    borderRadius: 8,
    backgroundColor: colors.sunken,
    fontSize: 12,
    overflow: 'auto',
    scrollbarWidth: 'thin',
    scrollbarColor: `${colors.lineStrong} transparent`,
  },
  heading: {
    position: 'sticky',
    top: -8,
    display: 'flex',
    alignItems: 'center',
    gap: 8,
    marginTop: -8,
    marginInline: -10,
    marginBottom: 4,
    paddingTop: 8,
    paddingInline: 10,
    paddingBottom: 4,
    backgroundColor: colors.sunken,
  },
  kind: { fontSize: 9, color: '#808080' },
  question: { flexGrow: 1, minWidth: 0, color: colors.textMuted, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' },
  muted: { color: '#858585', margin: 0 },
  error: { color: colors.danger, margin: 0 },
});
