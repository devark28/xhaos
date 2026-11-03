// /allow, /deny, /pause and /resume for prototype (local) agents.
import { activeAgents, transition, type LocalAction } from './model.ts';
import { hasPendingQuestions } from './questions.ts';
import type { Agent, Status } from './types.ts';

type Command = 'allow' | 'deny' | 'pause' | 'resume';
const aliases: Record<string, Command> = { approve: 'allow', decline: 'deny' };
const expected: Record<Command, Status> = { allow: 'waiting', deny: 'waiting', pause: 'running', resume: 'paused' };
const actions: Record<Command, LocalAction> = { allow: 'approve', deny: 'reject', pause: 'pause', resume: 'pause' };

export function applyAgentCommand(agents: Agent[], attached: Set<number>, command: string, argument = '') {
  const action = aliases[command] || command;
  const explicit = argument.trim();
  if (explicit && !/^#?\d+$/.test(explicit)) throw new Error(`Use /${action} with attached agents, or one explicit agent ID.`);
  const targets = activeAgents(agents).filter(agent => (explicit ? agent.id === Number(explicit.replace('#', '')) : attached.has(agent.id)));
  if (!targets.length) throw new Error('Attach an active agent to the composer, or specify its ID.');
  if (!(action in expected)) throw new Error('Unsupported agent action.');
  const want = expected[action as Command];
  const eligible = targets.filter(agent => agent.status === want && !hasPendingQuestions(agent));
  if (!eligible.length) {
    throw new Error(targets.some(hasPendingQuestions) ? 'Answer the agent’s questions in its card; Allow/Deny cannot answer them.' : 'No targeted agents are eligible for this action.');
  }
  eligible.forEach(agent => transition(agent, actions[action as Command]));
  return { applied: eligible.map(agent => agent.id), skipped: targets.filter(agent => !eligible.includes(agent)).map(agent => agent.id) };
}
