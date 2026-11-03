// An agent's transcript: messages, tool calls, approvals, and live output.
import { useLayoutEffect, useRef, useState } from 'react';
import * as stylex from '@stylexjs/stylex';
import { readFile, fileSize } from '../attachments.ts';
import { markdown } from '../markdown.ts';
import { highlight, highlightDiff, languageFor } from '../highlight.ts';
import { colors, fonts, breakpoints } from './theme/tokens.stylex.ts';
import { Button } from './kit.tsx';
import { WorkingLine } from './Working.tsx';
import { DecisionButtons } from './Decisions.tsx';
import { toast } from './state/store.ts';
import type { Agent, Attachment, Entry as EntryData } from '../types.ts';

const html = (value: string) => ({ __html: value });

// ---- attachments -----------------------------------------------------------
export const blobUrls = new Map<string, string>();
async function loadAttachment(id: string) {
  const file = await readFile(id);
  if (!blobUrls.has(id)) blobUrls.set(id, URL.createObjectURL(file.blob));
  return { ...file, url: blobUrls.get(id)! };
}

function FileItem({ file }: { file: Attachment }) {
  const [image, setImage] = useState<{ url: string; name: string } | null>(null);
  const download = async () => {
    try {
      const loaded = await loadAttachment(file.id);
      const link = document.createElement('a'); link.href = loaded.url; link.download = loaded.name; link.click();
    } catch (error) { toast((error as Error).message); }
  };
  const toggleImage = async () => {
    if (image) { setImage(null); return; }
    try { setImage(await loadAttachment(file.id)); } catch (error) { toast((error as Error).message); }
  };
  return (
    <div {...stylex.props(styles.file)}>
      <Button data-download={file.id} onClick={download} sx={styles.fileButton}>{file.name} <small {...stylex.props(styles.fileSize)}>{fileSize(file.size)}</small> ↓</Button>
      {/^image\/(png|jpeg|webp|gif|avif)$/.test(file.type) && <Button data-image={file.id} aria-expanded={Boolean(image)} onClick={toggleImage} sx={styles.fileButton}>View image</Button>}
      <div data-image-slot={file.id} hidden={!image}>{image && <img src={image.url} alt={image.name} {...stylex.props(styles.image)} />}</div>
    </div>
  );
}

export function AttachedFiles({ files = [] }: { files?: Attachment[] }) {
  return files.length ? <div {...stylex.props(styles.files)}>{files.map(file => <FileItem key={file.id} file={file} />)}</div> : null;
}

// ---- entries ---------------------------------------------------------------
const kindNames: Record<string, string> = { user: 'you', assistant: 'agent', tool: 'tool', diff: 'edit', approval: 'approval', system: 'system' };

function Entry({ entry, agent }: { entry: EntryData; agent: Agent }) {
  const kind = kindNames[entry.kind] ? entry.kind : 'assistant';
  if (kind === 'tool' || kind === 'diff') {
    const output = kind === 'diff' ? highlightDiff(entry.output || '', languageFor(entry.command || '')) : highlight(entry.output || '', languageFor('', entry.output || ''));
    return (
      <details data-entry={kind} data-call={entry.callId} {...stylex.props(styles.tool)}>
        <summary {...stylex.props(styles.summary)}>
          <span {...stylex.props(styles.kind)}>{kindNames[kind]}</span>
          <span {...stylex.props(styles.toolTitle)}>{entry.text}</span>
          <code {...stylex.props(styles.command)} dangerouslySetInnerHTML={html(highlight(entry.command || '', kind === 'diff' ? 'text' : 'bash'))} />
        </summary>
        <pre {...stylex.props(styles.pre)}><code dangerouslySetInnerHTML={html(output)} /></pre>
      </details>
    );
  }
  if (kind === 'approval') {
    const code = entry.language === 'diff' ? highlightDiff(entry.command || '', languageFor(entry.command || '')) : highlight(entry.command || '', entry.language || 'javascript');
    return (
      <div data-entry="approval" data-call={entry.callId} {...stylex.props(styles.request)}>
        <span {...stylex.props(styles.kind)}>approval</span>
        <p {...stylex.props(styles.requestText)}>{entry.text}</p>
        <pre {...stylex.props(styles.pre)}><code dangerouslySetInnerHTML={html(code)} /></pre>
        {entry.output && <p {...stylex.props(styles.requestContext)}>{entry.output}</p>}
        {entry.callId && <div {...stylex.props(styles.requestActions)}><DecisionButtons agent={agent} callId={entry.callId} /></div>}
      </div>
    );
  }
  const label = kind === 'user' ? 'Your message' : kind === 'system' ? 'Status update' : 'Agent response';
  return (
    <div data-entry={kind} aria-label={label} {...stylex.props(styles.entry, kind === 'user' && styles.user, kind === 'system' && styles.system)}>
      <div className="markdown-body" dangerouslySetInnerHTML={html(markdown(entry.text))} />
      <AttachedFiles files={entry.attachments} />
    </div>
  );
}

// Follow the bottom: a transcript opens scrolled to its end and keeps up with
// new output while the reader is at the bottom. Scrolling up stops following
// until they scroll back down; sending a message jumps back to it.
function useFollow() {
  const ref = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const el = ref.current!;
    let follow = true;
    const toEnd = () => { el.scrollTop = el.scrollHeight; };
    const onScroll = () => { follow = el.scrollHeight - el.scrollTop - el.clientHeight < 24; };
    const watch = new MutationObserver(records => {
      if (records.some(r => [...r.addedNodes].some(n => (n as HTMLElement).dataset?.entry === 'user'))) follow = true;
      if (follow) toEnd();
    });
    toEnd();
    el.addEventListener('scroll', onScroll, { passive: true });
    watch.observe(el, { childList: true, subtree: true, characterData: true });
    return () => { el.removeEventListener('scroll', onScroll); watch.disconnect(); };
  }, []);
  return ref;
}

export function Transcript({ agent, nested = false }: { agent: Agent; nested?: boolean }) {
  const entries = agent.activity || [{ kind: 'assistant', text: agent.message || '' }];
  const ref = useFollow();
  return (
    <div ref={ref} data-transcript={agent.id} aria-label={`Agent ${agent.id} activity`} {...stylex.props(styles.transcript, nested && styles.nested)}>
      {agent.demo && <div {...stylex.props(styles.sample)}>Sample transcript · commands are illustrative</div>}
      {entries.map((entry, index) => <Entry key={entry.callId || index} entry={entry} agent={agent} />)}
      {(agent.notes || []).map((note, index) => (
        <div key={`note-${index}`} data-entry="user" aria-label="Your message" {...stylex.props(styles.entry, styles.user)}>
          <div className="markdown-body" dangerouslySetInnerHTML={html(markdown(note))} />
        </div>
      ))}
      {agent.live && (
        <div data-entry="live" aria-live="polite" aria-label="Agent reply in progress" {...stylex.props(styles.entry, styles.live)}>
          <div className="markdown-body" dangerouslySetInnerHTML={html(markdown(agent.live))} />
        </div>
      )}
      <WorkingLine agent={agent} />
    </div>
  );
}

const styles = stylex.create({
  transcript: {
    fontFamily: fonts.mono,
    fontSize: 11,
    lineHeight: 1.65,
    maxHeight: { default: 360, [breakpoints.narrow]: 300 },
    overflow: 'auto',
    overflowWrap: 'anywhere',
    scrollbarWidth: 'thin',
    scrollbarColor: `${colors.lineStrong} ${colors.card}`,
  },
  nested: { paddingTop: 3, paddingInline: 9, paddingBottom: 9 },
  sample: { fontFamily: 'inherit', fontSize: 9, color: '#727272', marginBottom: 9 },
  entry: { display: 'block', paddingBlock: 8 },
  user: { backgroundColor: '#2a2a2a', borderRadius: 3, paddingBlock: 9, paddingInline: 11, marginTop: 8, marginBottom: 12, color: '#e0e0e0' },
  system: { paddingBlock: 6, fontSize: 10, color: '#808080' },
  live: { opacity: 0.85 },
  kind: { display: 'inline-block', fontSize: 9, color: '#808080', marginRight: 9 },
  tool: { marginBlock: 12 },
  summary: { cursor: 'pointer', paddingBlock: 2, fontFamily: 'inherit', fontSize: 10, lineHeight: 1.7, color: colors.textMuted },
  toolTitle: { color: '#b8b8b8' },
  command: { display: 'block', paddingLeft: 12, color: '#d1d1d1', whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', fontSize: 10, fontFamily: 'inherit' },
  pre: {
    whiteSpace: 'pre-wrap',
    overflowWrap: 'anywhere',
    marginBlock: 5,
    marginInline: 0,
    paddingLeft: 12,
    fontFamily: fonts.mono,
    fontSize: 10,
    lineHeight: 1.65,
    color: colors.textMuted,
  },
  request: { marginBlock: 12, borderLeftWidth: 2, borderLeftStyle: 'solid', borderLeftColor: colors.brandLine, paddingLeft: 10 },
  requestText: { marginTop: 4, marginBottom: 8, color: colors.textSoft },
  requestContext: { marginTop: 8, marginBottom: 0, fontSize: 10, color: colors.textDim },
  requestActions: { display: 'flex', alignItems: 'center', gap: 6, marginTop: 8 },
  files: { marginTop: 8, display: 'flex', flexDirection: 'column', gap: 4 },
  file: { minWidth: 0 },
  fileButton: { whiteSpace: 'normal', overflowWrap: 'anywhere', textAlign: 'left', marginRight: 10 },
  fileSize: { color: colors.textFaint, fontSize: 9, marginLeft: 4 },
  image: { display: 'block', maxWidth: '100%', maxHeight: 260, objectFit: 'contain', marginBlock: 8 },
});
