// An agent's transcript: messages, tool calls, approvals, and live output.
import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import * as stylex from '@stylexjs/stylex';
import { readFile, fileSize, downloadFile } from '../attachments.ts';
import { call, loadOlder } from './state/backend.ts';
import { markdown } from '../markdown.ts';
import { dataURLFor } from '../board.ts';
import { highlight, highlightDiff, languageFor } from '../highlight.ts';
import { colors, fonts, breakpoints } from './theme/tokens.stylex.ts';
import { Button } from './kit.tsx';
import { WorkingLine } from './Working.tsx';
import { DecisionButtons } from './Decisions.tsx';
import { toast, get, useApp } from './state/store.ts';
import { openBoard } from './state/boards.ts';
import { selectPanel } from './state/panels.ts';
import { isNarrow } from './narrow.ts';
import type { Agent, Attachment, Entry as EntryData } from '../types.ts';

const html = (value: string) => ({ __html: value });
const links = { agentLinks: true }; // #12 links to agent 12's card

// ---- attachments -----------------------------------------------------------
export const blobUrls = new Map<string, string>();
async function loadAttachment(id: string, server?: boolean) {
  const file = server ? await downloadFile(call, get().active, id) : await readFile(id);
  if (!blobUrls.has(id)) blobUrls.set(id, URL.createObjectURL(file.blob));
  return { ...file, url: blobUrls.get(id)! };
}

function FileItem({ file }: { file: Attachment }) {
  const [image, setImage] = useState<{ url: string; name: string } | null>(null);
  const download = async () => {
    try {
      const loaded = await loadAttachment(file.id, file.server);
      const link = document.createElement('a'); link.href = loaded.url; link.download = loaded.name; link.click();
    } catch (error) { toast((error as Error).message); }
  };
  const toggleImage = async () => {
    if (image) { setImage(null); return; }
    try { setImage(await loadAttachment(file.id, file.server)); } catch (error) { toast((error as Error).message); }
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

// StagedImage shows the image a waiting board_add_image call will add: a
// small preview of the bytes the server read when the call was made, which
// are what it adds. When the server can make no preview (WebP, or a file it
// can't decode) the card names the file, its type and size instead.
type StagedPreview = { name: string; fileType: string; size: number; mimeType?: string; data?: string; noPreview?: boolean };
function StagedImage({ agentId, callId }: { agentId: number; callId: string }) {
  const [state, setState] = useState<{ file: StagedPreview } | { error: string } | null>(null);
  useEffect(() => {
    let live = true;
    call('board', 'StagedImage', get().active, agentId, callId)
      .then((file: StagedPreview) => { if (live) setState({ file }); })
      .catch((error: unknown) => { if (live) setState({ error: error instanceof Error ? error.message : String(error) }); });
    return () => { live = false; };
  }, [agentId, callId]);
  if (!state) return <p data-staged-image="loading" {...stylex.props(styles.requestContext)}>Loading the image…</p>;
  if ('error' in state) return <p data-staged-image="error" {...stylex.props(styles.requestContext)}>Could not show the image: {state.error}</p>;
  const { file } = state;
  const about = `${file.name} · ${file.fileType} · ${fileSize(file.size)}`;
  if (file.noPreview || !file.mimeType || !file.data) return <p data-staged-image={callId} data-no-preview {...stylex.props(styles.requestContext)}>Image to add: {about} (no preview)</p>;
  return <img data-staged-image={callId} src={dataURLFor(file.mimeType, file.data)} alt={`The image the agent will add: ${about}`} title={about} {...stylex.props(styles.thumb)} />;
}

// ---- entries ---------------------------------------------------------------
const kindNames: Record<string, string> = { user: 'you', assistant: 'agent', tool: 'tool', diff: 'edit', approval: 'approval', system: 'system', channel: 'channel' };

// BoardLine follows a finished board write: "added a note to Board 2", the
// board's current name linking to it in Draw (the Boards panel on a phone,
// which has no canvas).
function BoardLine({ board }: { board: NonNullable<EntryData['board']> }) {
  const current = useApp(s => s.boards.find(b => b.id === board.id));
  const gone = useApp(s => s.boards.length > 0) && !current;
  const name = current?.name ?? board.name;
  const open = () => { openBoard(board.id); if (isNarrow()) selectPanel('boards'); };
  return (
    <p data-board-added={board.id} {...stylex.props(styles.boardLine)}>
      {board.added}{' '}
      {gone
        ? <span>{name} (deleted)</span>
        : <button type="button" data-board-link={board.id} title={isNarrow() ? 'Show in Boards' : 'Open in Draw'} onClick={open} {...stylex.props(styles.boardLink)}>{name}</button>}
    </p>
  );
}

function Entry({ entry, agent }: { entry: EntryData; agent: Agent }) {
  const kind = kindNames[entry.kind] ? entry.kind : 'assistant';
  if (kind === 'tool' || kind === 'diff') {
    const output = kind === 'diff' ? highlightDiff(entry.output || '', languageFor(entry.command || '')) : highlight(entry.output || '', languageFor('', entry.output || ''));
    const details = (
      <details data-entry={kind} data-call={entry.callId} {...stylex.props(styles.tool)}>
        <summary {...stylex.props(styles.summary)}>
          <span {...stylex.props(styles.kind)}>{kindNames[kind]}</span>
          <span {...stylex.props(styles.toolTitle)}>{entry.text}</span>
          <code {...stylex.props(styles.command)} dangerouslySetInnerHTML={html(highlight(entry.command || '', kind === 'diff' ? 'text' : 'bash'))} />
        </summary>
        <pre {...stylex.props(styles.pre)}><code dangerouslySetInnerHTML={html(output)} /></pre>
      </details>
    );
    return entry.board ? <>{details}<BoardLine board={entry.board} /></> : details;
  }
  if (kind === 'approval') {
    const code = entry.language === 'diff' ? highlightDiff(entry.command || '', languageFor(entry.command || '')) : highlight(entry.command || '', entry.language || 'javascript');
    return (
      <div data-entry="approval" data-call={entry.callId} {...stylex.props(styles.request)}>
        <span {...stylex.props(styles.kind)}>approval</span>
        <p {...stylex.props(styles.requestText)}>{entry.text}</p>
        <pre {...stylex.props(styles.pre)}><code dangerouslySetInnerHTML={html(code)} /></pre>
        {entry.stagedImage && entry.callId && <StagedImage agentId={agent.id} callId={entry.callId} />}
        {entry.output && <p {...stylex.props(styles.requestContext)}>{entry.output}</p>}
        {entry.callId && <div {...stylex.props(styles.requestActions)}><DecisionButtons agent={agent} callId={entry.callId} /></div>}
      </div>
    );
  }
  if (kind === 'channel') {
    // "[channel #N "subject"] message from agent #X (title):" then the body.
    const [head, ...rest] = entry.text.split('\n');
    return (
      <div data-entry="channel" aria-label="Message from another agent" {...stylex.props(styles.entry, styles.channel)}>
        <span {...stylex.props(styles.kind)}>channel</span><span {...stylex.props(styles.channelHead)}>{head.replace(/^\[channel /, '#').replace(/\] message from /, ' · from ').replace(/:$/, '')}</span>
        <div className="markdown-body" dangerouslySetInnerHTML={html(markdown(rest.join('\n'), links))} />
      </div>
    );
  }
  const label = kind === 'user' ? 'Your message' : kind === 'system' ? 'Status update' : 'Agent response';
  return (
    <div data-entry={kind} aria-label={label} {...stylex.props(styles.entry, kind === 'user' && styles.user, kind === 'system' && styles.system)}>
      <div className="markdown-body" dangerouslySetInnerHTML={html(markdown(entry.text, links))} />
      <AttachedFiles files={entry.attachments} />
    </div>
  );
}

// atEnd: no transcript entry comes after el (the live reply and the working
// line may).
function atEnd(el: HTMLElement) {
  for (let next = el.nextElementSibling as HTMLElement | null; next; next = next.nextElementSibling as HTMLElement | null) {
    if (next.dataset.entry && next.dataset.entry !== 'live') return false;
  }
  return true;
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
      // A message of yours at the end (just sent), not older ones loading above.
      if (records.some(r => [...r.addedNodes].some(n => (n as HTMLElement).dataset?.entry === 'user' && atEnd(n as HTMLElement)))) follow = true;
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
  // Older history loads as the top comes into view (or on a click), and the
  // reader stays on what they were reading: the distance from the bottom
  // is kept while the older entries go in above.
  const top = useRef<HTMLButtonElement>(null);
  const fromBottom = useRef<number | null>(null);
  const earlier = () => {
    const el = ref.current;
    if (el) fromBottom.current = el.scrollHeight - el.scrollTop;
    void loadOlder(agent.id);
  };
  useEffect(() => {
    const el = ref.current, mark = top.current;
    if (!el || !mark) return;
    const seen = new IntersectionObserver(found => { if (found.some(f => f.isIntersecting)) earlier(); }, { root: el, rootMargin: '300px 0px 0px 0px' });
    seen.observe(mark);
    return () => seen.disconnect();
  }, [agent.older]);
  const firstSeq = entries[0]?.seq;
  useLayoutEffect(() => {
    const el = ref.current;
    if (el && fromBottom.current != null) { el.scrollTop = el.scrollHeight - fromBottom.current; fromBottom.current = null; }
  }, [firstSeq]); // only when older entries went in above
  return (
    <div ref={ref} data-transcript={agent.id} aria-label={`Agent ${agent.id} activity`} {...stylex.props(styles.transcript, nested && styles.nested)}>
      {agent.demo && <div {...stylex.props(styles.sample)}>Sample transcript · commands are illustrative</div>}
      {agent.older != null && <button ref={top} type="button" data-older onClick={earlier} {...stylex.props(styles.older)}>Earlier messages…</button>}
      {entries.map((entry, index) => <Entry key={entry.callId || (entry.seq != null ? `seq-${entry.seq}` : index)} entry={entry} agent={agent} />)}
      {(agent.notes || []).map((note, index) => (
        <div key={`note-${index}`} data-entry="user" aria-label="Your message" {...stylex.props(styles.entry, styles.user)}>
          <div className="markdown-body" dangerouslySetInnerHTML={html(markdown(note, links))} />
        </div>
      ))}
      {agent.live && (
        <div data-entry="live" aria-live="polite" aria-label="Agent reply in progress" {...stylex.props(styles.entry, styles.live)}>
          <div className="markdown-body" dangerouslySetInnerHTML={html(markdown(agent.live, links))} />
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
  boardLine: { margin: 0, paddingBottom: 4, color: colors.textDim },
  boardLink: { backgroundColor: 'transparent', borderWidth: 0, padding: 0, cursor: 'pointer', color: colors.brand, fontFamily: 'inherit', fontSize: 'inherit', textDecoration: { default: 'none', ':hover': 'underline' } },
  nested: { paddingTop: 3, paddingInline: 9, paddingBottom: 9 },
  sample: { fontFamily: 'inherit', fontSize: 9, color: colors.textGhost, marginBottom: 9 },
  older: { display: 'block', marginInline: 'auto', marginBottom: 9, padding: '2px 8px', fontFamily: 'inherit', fontSize: 9, color: colors.textFaint, backgroundColor: 'transparent', borderWidth: 0, cursor: 'pointer', ':hover': { color: colors.text } },
  entry: { display: 'block', paddingBlock: 8 },
  user: { backgroundColor: colors.selected, borderRadius: 3, paddingBlock: 9, paddingInline: 11, marginTop: 8, marginBottom: 12, color: colors.text },
  system: { paddingBlock: 6, fontSize: 10, color: colors.textFaint },
  live: { opacity: 0.85 },
  channel: { borderLeftWidth: 2, borderLeftStyle: 'solid', borderLeftColor: colors.lineStrong, paddingLeft: 10 },
  channelHead: { fontSize: 10, color: colors.textMuted },
  kind: { display: 'inline-block', fontSize: 9, color: colors.textFaint, marginRight: 9 },
  tool: { marginBlock: 12 },
  summary: { cursor: 'pointer', paddingBlock: 2, fontFamily: 'inherit', fontSize: 10, lineHeight: 1.7, color: colors.textMuted },
  toolTitle: { color: colors.textSub },
  command: { display: 'block', paddingLeft: 12, color: colors.textSoft, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', fontSize: 10, fontFamily: 'inherit' },
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
  thumb: { display: 'block', maxWidth: '100%', maxHeight: 160, objectFit: 'contain', marginBlock: 8, borderRadius: 4, borderWidth: 1, borderStyle: 'solid', borderColor: colors.lineFaint },
});
