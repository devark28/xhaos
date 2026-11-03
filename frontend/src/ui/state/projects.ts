// Projects (server folders agents work in) and the Files panel's tree.
import { update, get, toast, errorText, focusLater } from './store.ts';
import { call, connected } from './backend.ts';
import type { FileEntry, Project } from '../../types.ts';

export const dirKey = (projectId: number, dir: string) => `${projectId}:${dir}`;

export async function refreshProjects() {
  if (!connected()) return;
  const projects = await call<Project[]>('project', 'Projects', get().active);
  update(s => {
    s.projects = projects;
    if (!projects.some(p => p.id === s.attachedProject)) s.attachedProject = null;
  });
}

// A project attached to the composer says where new agents work (without
// one, the server uses the workspace's first project). One at a time:
// attaching another replaces it.
export function toggleProjectAttachment(id: number) {
  update(s => { s.attachedProject = s.attachedProject === id ? null : id; s.mode = 'message'; });
  focusLater('#command');
}
export function detachProject() { update(s => { s.attachedProject = null; }); }
export const projectName = (id?: number | null) => get().projects.find(p => p.id === id)?.name;

export async function addProject(path: string, name: string): Promise<boolean> {
  try {
    const project = await call<Project>('project', 'Add', { workspaceId: get().active, path, name });
    await refreshProjects();
    toast(`Added project ${project.name}.`);
    return true;
  } catch (error) { toast(`Could not add project: ${errorText(error)}`); return false; }
}

export async function removeProject(id: number) {
  try {
    await call('project', 'Remove', { workspaceId: get().active, id });
    update(s => { for (const key of Object.keys(s.files)) if (key.startsWith(`${id}:`)) delete s.files[key]; });
    await refreshProjects();
  } catch (error) { toast(`Could not remove project: ${errorText(error)}`); }
}

async function loadDir(projectId: number, dir: string) {
  const key = dirKey(projectId, dir);
  try {
    const entries = await call<FileEntry[]>('project', 'Files', { workspaceId: get().active, projectId, dir });
    update(s => { s.files[key] = entries; });
  } catch (error) { update(s => { s.files[key] = { error: errorText(error) }; }); }
}

// toggleDir expands or collapses a folder (a project's root has dir "").
// Expanding always reloads it, so the tree reflects the disk.
export function toggleDir(projectId: number, dir: string) {
  const key = dirKey(projectId, dir);
  const open = get().openDirs.has(key);
  update(s => { if (open) s.openDirs.delete(key); else s.openDirs.add(key); });
  if (!open) loadDir(projectId, dir);
}
