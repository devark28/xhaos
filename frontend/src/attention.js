export function createAttentionTracker() {
  let previous = new Set();
  return (workspaceId, agents) => {
    const pending = [];
    const visit = list => list.forEach(agent => {
      if(agent.archivedAt)return;
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
export function agentPath(agents, id) {
  for (const agent of agents) {
    if (agent.id === id) return [agent.id];
    const path = agentPath(agent.children || [], id);
    if (path.length) return [agent.id, ...path];
  }
  return [];
}
