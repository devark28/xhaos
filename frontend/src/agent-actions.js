import { activeAgents, transition } from './model.js';
import { hasPendingQuestions } from './questions.js';
export function applyAgentCommand(agents, attached, command, argument='') {
  const action=({approve:'allow',decline:'deny'})[command]||command;
  const explicit=argument.trim();
  if(explicit&&!/^#?\d+$/.test(explicit))throw new Error(`Use /${action} with attached agents, or one explicit agent ID.`);
  const targets=activeAgents(agents).filter(agent=>explicit?agent.id===Number(explicit.replace('#','')):attached.has(agent.id));
  if(!targets.length)throw new Error('Attach an active agent to the composer, or specify its ID.');
  const expected={allow:'waiting',deny:'waiting',pause:'running',resume:'paused'}[action];
  if(!expected)throw new Error('Unsupported agent action.');
  const eligible=targets.filter(agent=>agent.status===expected&&!hasPendingQuestions(agent));
  if(!eligible.length)throw new Error(targets.some(hasPendingQuestions)?'Answer the agent’s questions in its card; Allow/Deny cannot answer them.':'No targeted agents are eligible for this action.');
  eligible.forEach(agent=>transition(agent,({allow:'approve',deny:'reject',resume:'pause'})[action]||action));
  return {applied:eligible.map(agent=>agent.id),skipped:targets.filter(agent=>!eligible.includes(agent)).map(agent=>agent.id)};
}
