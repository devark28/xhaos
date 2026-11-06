// Settings → Skills: workspace skills (uploaded .skill files) and the skills
// found in each project's .claude/skills or .xhaos/skills, each with a
// switch. Agents see the enabled ones by name and description, and load
// them with skill_read (package skill).
import { useEffect, useRef, useState, type DragEvent } from 'react';
import * as stylex from '@stylexjs/stylex';
import { colors } from './theme/tokens.stylex.ts';
import { Button } from './kit.tsx';
import { Toggle } from './AppearanceSettings.tsx';
import { fileSize } from '../attachments.ts';
import { call } from './state/backend.ts';
import { useApp, toast, errorText, get } from './state/store.ts';

interface Skill { name: string; description: string; source: 'workspace' | 'project'; projectId?: number; folder?: string; enabled: boolean; files: number; size: number }
interface Listing { workspace: Skill[]; projects: { projectId: number; project: string; skills: Skill[] }[] }

const toBase64 = (buf: ArrayBuffer) => {
  const bytes = new Uint8Array(buf);
  let s = '';
  for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(s);
};
function download(name: string, base64: string) {
  const bytes = Uint8Array.from(atob(base64), c => c.charCodeAt(0));
  const url = URL.createObjectURL(new Blob([bytes], { type: 'application/zip' }));
  const a = Object.assign(document.createElement('a'), { href: url, download: name });
  document.body.append(a); a.click(); a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 5000);
}

export function Skills() {
  const version = useApp(s => s.skillsVersion), ws = useApp(s => s.active);
  const [list, setList] = useState<Listing | null>(null), [dragging, setDragging] = useState(false), [busy, setBusy] = useState(false);
  const input = useRef<HTMLInputElement>(null);
  const load = async () => { try { setList(await call<Listing>('skill', 'Skills', ws)); } catch (error) { toast(`Could not load skills: ${errorText(error)}`); } };
  useEffect(() => { void load(); }, [version, ws]);
  const upload = async (files: FileList | File[]) => {
    setBusy(true);
    for (const file of [...files]) {
      try {
        const k = await call<Skill>('skill', 'Upload', { workspaceId: get().active, fileName: file.name, data: toBase64(await file.arrayBuffer()) });
        toast(`Added skill ${k.name}.`);
      } catch (error) { toast(`${file.name}: ${errorText(error)}`); }
    }
    setBusy(false); void load();
  };
  const toggle = async (k: Skill, on: boolean) => {
    try { await call('skill', 'SetEnabled', { workspaceId: get().active, projectId: k.projectId || 0, name: k.name, enabled: on }); void load(); }
    catch (error) { toast(errorText(error)); }
  };
  const exportSkill = async (k: Skill) => {
    try { const f = await call<{ name: string; data: string }>('skill', 'Export', { workspaceId: get().active, projectId: k.projectId || 0, name: k.name }); download(f.name, f.data); }
    catch (error) { toast(errorText(error)); }
  };
  const remove = async (k: Skill) => {
    if (!confirm(`Delete skill ${k.name}?`)) return;
    try { await call('skill', 'Delete', { workspaceId: get().active, name: k.name }); void load(); } catch (error) { toast(errorText(error)); }
  };
  const onDrop = (e: DragEvent) => { e.preventDefault(); setDragging(false); if (e.dataTransfer.files.length) void upload(e.dataTransfer.files); };
  if (!list) return <p {...stylex.props(s.muted)}>Loading…</p>;
  const row = (k: Skill) => (
    <div key={`${k.projectId || 0}:${k.name}`} data-skill={k.name} data-skill-project={k.projectId || 0} {...stylex.props(s.row, !k.enabled && s.off)}>
      <div {...stylex.props(s.head)}>
        <Toggle name={`skill-${k.projectId || 0}-${k.name}`} label={k.name} on={k.enabled} onChange={on => toggle(k, on)} />
      </div>
      <p {...stylex.props(s.desc)}>{k.description}</p>
      <div {...stylex.props(s.meta)}>
        <span>{k.files} file{k.files === 1 ? '' : 's'} · {fileSize(k.size)}{k.folder ? ` · ${k.folder}` : ''}</span>
        <span {...stylex.props(s.actions)}>
          <Button data-skill-export={k.name} onClick={() => exportSkill(k)}>{k.source === 'project' ? 'Export .skill' : 'Download'}</Button>
          {k.source === 'workspace' && <Button data-skill-delete={k.name} onClick={() => remove(k)}>Delete</Button>}
        </span>
      </div>
    </div>
  );
  return (
    <div data-skills {...stylex.props(s.root)}>
      <p {...stylex.props(s.muted)}>Agents see each enabled skill's name and description and load it when a task calls for it. Skills use Claude Code's format: a folder with a SKILL.md, packed as a .skill (zip) file.</p>
      <div data-skill-drop onDragOver={e => { e.preventDefault(); setDragging(true); }} onDragLeave={() => setDragging(false)} onDrop={onDrop} {...stylex.props(s.drop, dragging && s.dropOn)}>
        <span>{busy ? 'Adding…' : 'Drop .skill files here, or'}</span>
        <Button data-skill-pick onClick={() => input.current?.click()}>Choose files</Button>
        <input ref={input} type="file" accept=".skill,.zip" multiple hidden data-skill-input onChange={e => { if (e.target.files?.length) void upload(e.target.files); e.target.value = ''; }} />
      </div>
      <h4 {...stylex.props(s.group)}>Workspace</h4>
      {list.workspace.length ? list.workspace.map(row) : <p {...stylex.props(s.muted)}>None uploaded yet.</p>}
      {list.projects.map(p => (
        <div key={p.projectId} data-skill-group={p.projectId}>
          <h4 {...stylex.props(s.group)}>{p.project} <span {...stylex.props(s.groupNote)}>project skills, for its agents</span></h4>
          {p.skills.map(row)}
        </div>
      ))}
    </div>
  );
}

const s = stylex.create({
  root: { display: 'flex', flexDirection: 'column', gap: 6, fontSize: 11 },
  muted: { margin: 0, color: colors.textFaint, fontSize: 10.5, lineHeight: 1.5 },
  drop: { display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 8, paddingBlock: 12, borderWidth: 1, borderStyle: 'dashed', borderColor: colors.lineStrong, borderRadius: 5, color: colors.textMuted },
  dropOn: { borderColor: colors.brand, backgroundColor: colors.brandSoft },
  group: { margin: 0, marginTop: 8, fontSize: 10.5, fontWeight: 600, color: colors.textSoft },
  groupNote: { fontWeight: 400, color: colors.textGhost, marginLeft: 6 },
  row: { display: 'flex', flexDirection: 'column', gap: 3, paddingBlock: 7, paddingInline: 10, borderRadius: 5, borderWidth: 1, borderStyle: 'solid', borderColor: colors.lineFaint, backgroundColor: colors.sunken },
  off: { opacity: 0.6 },
  head: { fontFamily: 'ui-monospace, monospace' },
  desc: { margin: 0, color: colors.textMuted, lineHeight: 1.45 },
  meta: { display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8, fontSize: 10, color: colors.textGhost },
  actions: { display: 'flex', gap: 6 },
});
