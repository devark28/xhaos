import { hydrateDemoActivity } from './demo-activity.ts';
import type { Agent, Workspace } from './types.ts';

export const initialWorkspaces = (): Workspace[] => hydrateDemoActivity([
  { id: 'studio', name: 'Xhaos studio', initials: 'XS', channel: 'product-dev', agents: [
    { id: 11, title: 'Build the workspace foundation', status: 'running', role: 'Engineering', model: 'Claude Sonnet', time: '12m', progress: 64, message: 'Wiring workspace state into the application shell.', children: [
      { id: 14, title: 'Implement workspace persistence', status: 'running', role: 'Implementation', message: 'Adding workspace isolation and local storage.' },
      { id: 15, title: 'Review the storage boundary', status: 'waiting', role: 'Review', message: 'Requests permission to start a review subagent.' }
    ] },
    { id: 12, title: 'Explore the agent orchestration layer', status: 'waiting', role: 'Architecture', model: 'GPT', time: '8m', progress: 38, message: 'Ready to investigate a headless event stream. Approval needed.', children: [
      { id: 16, title: 'Compare event transport options', status: 'complete', role: 'Research', message: 'Transport comparison is ready for review.' }
    ] },
    { id: 13, title: 'Design a calmer command center', status: 'running', role: 'Design', model: 'Claude Sonnet', time: '6m', progress: 42, message: 'Refining agent hierarchy and attention states.', children: [
      { id: 17, title: 'Audit keyboard navigation', status: 'running', role: 'Accessibility', message: 'Checking focus order and disclosure controls.' },
      { id: 18, title: 'Explore compact card layouts', status: 'waiting', role: 'Exploration', message: 'Requests permission to explore an alternative layout.' }
    ] },
    { id: 19, title: 'Map MCP integration boundaries', status: 'paused', role: 'Integrations', model: 'GPT', time: '3m', progress: 20, message: 'Paused. Waiting for orchestration decisions.', children: [] },
    { id: 20, title: 'Document the local development flow', status: 'complete', role: 'Documentation', model: 'Claude Sonnet', time: '4m', progress: 100, message: 'Development notes are ready.', children: [] }
  ] },
  { id: 'personal', name: 'Personal lab', initials: 'PL', channel: 'experiments', agents: [
    { id: 21, title: 'Plan this week’s experiments', status: 'paused', role: 'Planning', model: 'GPT', time: '0m', progress: 0, message: 'Add a task to start exploring.', children: [] }
  ] }
]);
export function flatten(agents: Agent[]): Agent[] { return agents.flatMap(a => [a, ...flatten(a.children || [])]); }

export type LocalAction = 'approve' | 'reject' | 'pause';
export function transition(agent: Agent, action: LocalAction) {
  if(agent.questionBatch?.status==='pending'&&['approve','reject'].includes(action))return;
  const before = agent.status;
  if (action === 'approve' && agent.status === 'waiting') { agent.status = 'running'; agent.message = 'Approved locally. Simulated task is ready to run.'; }
  if (action === 'reject' && agent.status === 'waiting') { agent.status = 'paused'; agent.message = 'Request declined. Task paused.'; }
  if (action === 'pause' && agent.status === 'running') agent.status = 'paused';
  else if (action === 'pause' && agent.status === 'paused') agent.status = 'running';
  if (agent.status !== before && agent.activity) {
    const notes: Record<LocalAction, string> = { approve: 'Approved locally. Task state set to running; this prototype does not execute tools.', reject: 'Request declined locally. Task paused.', pause: agent.status === 'paused' ? 'Paused locally.' : 'Resumed locally. No backend is connected.' };
    agent.activity.push({ kind: 'system', text: notes[action] });
  }
}
export function activeAgents(agents: Agent[]): Agent[] {
  return agents.filter(agent=>!agent.archivedAt).flatMap(agent=>[agent,...activeAgents(agent.children||[])]);
}
export function archiveAgent(agents: Agent[], id: number) {
  const agent=flatten(agents).find(agent=>agent.id===id);
  if(!agent)throw new Error('Agent not found.');
  agent.archivedAt=new Date().toISOString();
}
export function restoreAgent(agents: Agent[], id: number) {
  const visit=(list: Agent[]): boolean=>{
    for(const agent of list){
      if(agent.id===id||visit(agent.children||[])){delete agent.archivedAt;return true;}
    }
    return false;
  };
  if(!visit(agents))throw new Error('Agent not found.');
}
export function terminateAgent(agents: Agent[], id: number) {
  const agent=flatten(agents).find(agent=>agent.id===id);
  if(!agent)throw new Error('Agent not found.');
  const timestamp=new Date().toISOString();
  for(const member of flatten([agent])){
    member.status='terminated';
    member.terminatedAt=timestamp;
    if(member.questionBatch?.status==='pending')member.questionBatch.status='cancelled';
    member.message='Terminated locally. No execution backend is connected.';
    member.activity ||= [];
    member.activity.push({kind:'system',text:member.message});
  }
  agent.archivedAt=timestamp;
}
