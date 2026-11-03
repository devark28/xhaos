// Agent-side channel operations for the future backend adapter. The UI only observes.
import type { Agent, Channel, ChannelMessage, Workspace } from './types.ts';

export function createChannel(workspace: Workspace, subject: string, participants: (number | string)[], creatorId: number): Channel {
  const name = subject.trim();
  const members = [...new Set(participants.map(Number))];
  const active = new Set<number>();
  const visit = (list: Agent[]) => list.forEach(agent => { if (agent.archivedAt) return; active.add(agent.id); visit(agent.children || []); });
  visit(workspace.agents);
  if (!name) throw new Error('A channel needs a subject.');
  if (members.length < 2 || members.some(id => !active.has(id))) throw new Error('A channel needs at least two active agents.');
  if (!members.includes(creatorId)) throw new Error('The creator must be a participating agent.');
  workspace.channels ||= [];
  if (workspace.channels.some(channel => channel.subject.toLowerCase() === name.toLowerCase())) throw new Error('That subject already exists.');
  const channel: Channel = { id: crypto.randomUUID(), subject: name, creatorId, participants: members, messages: [] };
  workspace.channels.push(channel);
  return channel;
}

export function appendAgentMessage(channel: Channel, writerId: number, text: string): ChannelMessage {
  if (!channel.participants.includes(writerId)) throw new Error('Only a participating agent can publish here.');
  if (!text.trim()) throw new Error('A message needs content.');
  const message: ChannelMessage = {
    id: crypto.randomUUID(),
    timestamp: new Date().toISOString(),
    writer: writerId,
    readers: channel.participants.filter(id => id !== writerId),
    text: text.trim(),
  };
  channel.messages.push(message);
  return message;
}
