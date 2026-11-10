// The sandbox badge on a node's card: what confines its commands. Nodes
// before the bubblewrap fallback send no `sandbox`; their Landlock ABI and
// --unsandboxed say it then.
export interface SandboxFacts { root: string; landlock: number; unsandboxed?: boolean; sandbox?: string; sandboxNote?: string }
export interface SandboxBadge { kind: 'landlock' | 'bwrap' | 'off' | 'none'; text: string; danger: boolean; tip: string }

export function sandboxBadge(f: SandboxFacts): SandboxBadge {
  const kind = (f.sandbox || (f.unsandboxed ? 'off' : f.landlock > 0 ? 'landlock' : 'none')) as SandboxBadge['kind'];
  switch (kind) {
    case 'landlock':
      return { kind, text: `Landlock ${f.landlock}`, danger: false, tip: `Commands run inside ${f.root}, confined by Landlock.` };
    case 'bwrap':
      return { kind, text: 'bubblewrap', danger: false, tip: `Commands run inside ${f.root}, confined by bubblewrap: they see only that folder, system files and their own processes (this kernel has no Landlock).` };
    case 'off':
      return { kind, text: 'unsandboxed', danger: true, tip: 'Its owner ran it with --unsandboxed: commands run as the xhaos-node user, unconfined, and can reach everything that user can. Only for a machine you can wipe.' };
    default: {
      const why = f.sandboxNote ? ` (${f.sandboxNote})` : f.landlock === 0 ? ' (this kernel has no Landlock)' : '';
      return { kind: 'none', text: 'no sandbox: runs no commands', danger: true, tip: `Neither Landlock nor bubblewrap works here${why}, so the node refuses commands. Its owner can install bubblewrap (sudo apt install bubblewrap, then sudo systemctl restart xhaos-node).` };
    }
  }
}
