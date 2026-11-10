// Terminating an agent from its card menu asks first (a stray click on the
// item under "Dangerously allow…" ended an agent). The typed /terminate and
// /remove commands do not ask: typing them is deliberate.
import { flatten } from './model.ts';
import type { Agent } from './types.ts';

// liveSubagents counts the agent's subagents, at any depth, that are not
// terminated yet: terminating the agent stops them too.
export const liveSubagents = (agent: Pick<Agent, 'children'>) =>
  flatten(agent.children || []).filter(child => child.status !== 'terminated').length;

// terminatePrompt is what the dialog says.
export function terminatePrompt(agent: Pick<Agent, 'id' | 'children'>) {
  const n = liveSubagents(agent);
  const subs = n === 0 ? '' : ` and its ${n === 1 ? 'subagent' : `${n} subagents`}`;
  return {
    title: `Terminate agent #${agent.id}?`,
    text: `This stops #${agent.id}${subs}. Its history is kept: you can bring it back from /history.`,
  };
}

// terminateFlow is the dialog's logic: ask opens it for an agent, cancel
// closes it, and only confirm terminates. set stores the asked-about agent
// (null when closed).
export function terminateFlow(asked: () => number | null, set: (id: number | null) => void, terminate: (id: number) => void) {
  return {
    ask: (id: number) => set(id),
    cancel: () => set(null),
    confirm: () => {
      const id = asked();
      set(null);
      if (id !== null) terminate(id);
    },
  };
}
