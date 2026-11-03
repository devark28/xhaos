// The Files panel: the workspace's projects, each expandable into its folder
// tree. A project is a folder on the server; agents work inside one.
import { useRef } from 'react';
import * as stylex from '@stylexjs/stylex';
import { fileSize } from '../attachments.ts';
import { colors } from './theme/tokens.stylex.ts';
import { Button } from './kit.tsx';
import { Icon } from './Icon.tsx';
import { useApp } from './state/store.ts';
import { addProject, dirKey, removeProject, toggleDir, toggleProjectAttachment } from './state/projects.ts';
import { onAttachKey, onToggleClick } from './clicks.ts';
import type { Project } from '../types.ts';

function Tree({ project, dir, depth }: { project: Project; dir: string; depth: number }) {
  const listing = useApp(s => s.files[dirKey(project.id, dir)]);
  const openDirs = useApp(s => s.openDirs);
  if (!listing) return <div {...stylex.props(styles.note, styles.indent(depth))}>Loading…</div>;
  if ('error' in listing) return <div {...stylex.props(styles.note, styles.error, styles.indent(depth))}>{listing.error}</div>;
  if (!listing.length) return <div {...stylex.props(styles.note, styles.indent(depth))}>Empty folder</div>;
  return <>
    {listing.map(entry => {
      const open = entry.dir && openDirs.has(dirKey(project.id, entry.path));
      return (
        <div key={entry.path}>
          {entry.dir
            ? <button type="button" data-file={entry.path} aria-expanded={open} onClick={() => toggleDir(project.id, entry.path)} {...stylex.props(styles.row, styles.indent(depth))}>
                <span {...stylex.props(styles.chevron, open && styles.chevronOpen)}><Icon name="arrow" size={10} /></span>
                <Icon name="folder" size={13} /><span {...stylex.props(styles.name)}>{entry.name}</span>
              </button>
            : <div data-file={entry.path} title={entry.path} {...stylex.props(styles.row, styles.fileRow, styles.indent(depth))}>
                <span {...stylex.props(styles.chevron)} />
                <Icon name="file" size={13} /><span {...stylex.props(styles.name)}>{entry.name}</span>
                <span {...stylex.props(styles.size)}>{fileSize(entry.size || 0)}</span>
              </div>}
          {open && <Tree project={project} dir={entry.path} depth={depth + 1} />}
        </div>
      );
    })}
  </>;
}

function ProjectRow({ project }: { project: Project }) {
  const open = useApp(s => s.openDirs.has(dirKey(project.id, '')));
  const attached = useApp(s => s.attachedProject === project.id);
  const attach = () => toggleProjectAttachment(project.id);
  const key = `project:${project.id}`;
  const agents = useApp(s => s.workspaces.flatMap(w => w.agents).filter(a => a.projectId === project.id && !a.archivedAt).length);
  return (
    <div data-project={project.id} data-attached={attached || undefined} {...stylex.props(styles.project, attached && styles.attached)}>
      <div {...stylex.props(styles.projectHead)}>
        <button type="button" aria-expanded={open} aria-pressed={attached} aria-keyshortcuts="Alt+Enter"
          title="Click to open; double-click or Ctrl+click (or Alt+Enter) to attach it to the composer, so new agents work here"
          onClick={event => onToggleClick(event, key, () => toggleDir(project.id, ''), attach)} onKeyDown={event => onAttachKey(event, key, attach)}
          {...stylex.props(styles.row, styles.projectButton)}>
          <span {...stylex.props(styles.chevron, open && styles.chevronOpen)}><Icon name="arrow" size={10} /></span>
          <Icon name="folder" size={14} />
          <span {...stylex.props(styles.projectName)}>{project.name}</span>
          <span title={project.path} {...stylex.props(styles.path)}>{project.path}</span>
        </button>
        <span {...stylex.props(styles.count)}>{agents ? `${agents} agent${agents > 1 ? 's' : ''}` : ''}</span>
        {!agents && <Button data-remove-project={project.id} title="Remove from Xhaos (the folder is kept)" onClick={() => removeProject(project.id)} sx={styles.action}>Remove</Button>}
      </div>
      {open && <div {...stylex.props(styles.tree)}><Tree project={project} dir="" depth={0} /></div>}
    </div>
  );
}

export function Files() {
  const backend = useApp(s => s.backendVersion !== null);
  const projects = useApp(s => s.projects);
  const path = useRef<HTMLInputElement>(null);
  const name = useRef<HTMLInputElement>(null);
  if (!backend) return <p {...stylex.props(styles.note)}>Projects are folders on the Go backend. Run scripts/server.sh and open its page.</p>;
  const add = async () => {
    if (!path.current?.value.trim()) { path.current?.focus(); return; }
    if (await addProject(path.current.value, name.current?.value || '')) { path.current.value = ''; if (name.current) name.current.value = ''; }
  };
  return <>
    <p {...stylex.props(styles.intro)}>Each agent works in one project: its tools only reach that folder. Double-click (or Ctrl+click) a project to attach it to the composer; new agents then start there. Agents in different projects coordinate through channels.</p>
    <div {...stylex.props(styles.list)}>{projects.map(p => <ProjectRow key={p.id} project={p} />)}</div>
    <div id="project-form" {...stylex.props(styles.form)}
      onKeyDown={event => { if (event.key === 'Enter' && !event.nativeEvent.isComposing) { event.preventDefault(); add(); } }}>
      <input ref={path} id="project-path" aria-label="Project folder" placeholder="Folder on the server, e.g. ~/code/app" spellCheck="false" autoComplete="off" {...stylex.props(styles.input, styles.grow)} />
      <input ref={name} id="project-name" aria-label="Project name" placeholder="Name (optional)" autoComplete="off" {...stylex.props(styles.input, styles.short)} />
      <Button id="add-project" onClick={add}>Add project</Button>
    </div>
  </>;
}

const styles = stylex.create({
  intro: { fontSize: 10, color: colors.textFaint, marginTop: 0, marginBottom: 10 },
  note: { fontSize: 10, color: colors.textGhost, paddingBlock: 3 },
  error: { color: colors.danger },
  list: { display: 'flex', flexDirection: 'column', gap: 4 },
  attached: { borderColor: colors.brandLine, backgroundColor: colors.attached },
  project: { borderWidth: 1, borderStyle: 'solid', borderColor: colors.lineFaint, borderRadius: 4, backgroundColor: colors.sunken },
  projectHead: { display: 'flex', alignItems: 'center', gap: 6, paddingRight: 6 },
  projectButton: { flexGrow: 1, paddingBlock: 7, paddingInline: 6 },
  projectName: { color: colors.text, fontSize: 12, flexShrink: 0 },
  path: { color: colors.textGhost, fontSize: 10, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' },
  count: { color: colors.textFaint, fontSize: 10, flexShrink: 0 },
  tree: { borderTopWidth: 1, borderTopStyle: 'solid', borderTopColor: colors.lineFaint, paddingBlock: 4, paddingInline: 6, maxHeight: 420, overflow: 'auto', scrollbarWidth: 'thin' },
  row: {
    display: 'flex',
    alignItems: 'center',
    gap: 6,
    width: '100%',
    minWidth: 0,
    paddingBlock: 2,
    borderWidth: 0,
    borderRadius: 3,
    backgroundColor: { default: 'transparent', ':hover': colors.hover },
    color: colors.textSoft,
    fontFamily: 'inherit',
    fontSize: 11,
    textAlign: 'left',
    cursor: 'pointer',
  },
  fileRow: { cursor: 'default', color: colors.textMuted },
  indent: (depth: number) => ({ paddingLeft: 4 + depth * 14 }),
  chevron: { display: 'inline-flex', width: 10, flexShrink: 0, color: colors.textFaint },
  chevronOpen: { transform: 'rotate(90deg)' },
  name: { minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' },
  size: { marginLeft: 'auto', paddingLeft: 8, flexShrink: 0, fontSize: 9, color: colors.textGhost },
  action: { whiteSpace: 'nowrap', flexShrink: 0 },
  form: { display: 'flex', gap: 6, alignItems: 'center', marginTop: 12 },
  input: { minWidth: 0, backgroundColor: colors.sunken, borderWidth: 0, borderRadius: 3, color: colors.text, paddingBlock: 7, paddingInline: 8, fontSize: 11, fontFamily: 'inherit' },
  grow: { flexGrow: 1 },
  short: { width: 120 },
});
