import type { Agent } from './types.ts';

// createAttentionTracker returns a function that reports agents that started
// waiting since its last call (per workspace), so each is announced once.
export function createAttentionTracker() {
  let previous = new Set<string>();
  return (workspaceId: string, agents: Agent[]): number[] => {
    const pending: number[] = [];
    const visit = (list: Agent[]) => list.forEach(agent => {
      if (agent.archivedAt) return;
      if (agent.status === 'waiting') pending.push(agent.id);
      visit(agent.children || []);
    });
    visit(agents);
    const current = new Set(pending.map(id => `${workspaceId}:${id}`));
    const newlyWaiting = pending.filter(id => !previous.has(`${workspaceId}:${id}`));
    previous = current;
    return newlyWaiting;
  };
}

// agentPath lists the IDs from the top-level agent down to id ([] if absent).
export function agentPath(agents: Agent[], id: number): number[] {
  for (const agent of agents) {
    if (agent.id === id) return [agent.id];
    const path = agentPath(agent.children || [], id);
    if (path.length) return [agent.id, ...path];
  }
  return [];
}
