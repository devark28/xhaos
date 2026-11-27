import test from 'node:test';
import assert from 'node:assert/strict';
import { viewFromSnapshot, channelsFromBackend } from './backend-view.ts';

// What an MCP client does for the owner shows its token's name.
test('MCP actions are attributed to their token', () => {
  const view = viewFromSnapshot({
    seq: 5, workspace: { id: 'default', name: 'Default' },
    agents: [{ id: 1, title: 'Agent', status: 'running' }],
    transcript: [
      { seq: 1, agentId: 1, kind: 'agent.message', data: { from: 'owner', body: 'Fix the login', via: 'Claude Code' } },
      { seq: 2, agentId: 1, kind: 'agent.message', data: { from: 'owner', body: 'Mine' } },
      { seq: 3, agentId: 1, kind: 'agent.tool', data: { callId: 'q', name: 'ask_user', summary: 'Which?', state: 'done', by: 'owner', via: 'Claude Code', output: 'A' } },
      { seq: 4, agentId: 1, kind: 'agent.tool', data: { callId: 'c', name: 'bash', effect: 'git', summary: 'git commit -m x', state: 'allowed', by: 'owner', via: 'Claude Code', commit: true, run: "cd /p && git commit --no-verify -m 'x'" } },
      { seq: 5, agentId: 1, kind: 'agent.tool', data: { callId: 'd', name: 'bash', effect: 'git', summary: 'git status', state: 'allowed', by: 'owner' } },
    ],
  } as any);
  const items = view.agents[0].activity!;
  assert.equal(items[0].via, 'Claude Code');
  assert.equal(items[1].via, undefined);
  assert.match(items[2].text, /answered via MCP \(Claude Code\)/);
  assert.equal(items[3].text, "Run · allowed via MCP (Claude Code), ran Xhaos's own command for it");
  assert.equal(items[3].command, "cd /p && git commit --no-verify -m 'x'");
  assert.equal(items[4].text, 'Run · allowed');

  const [channel] = channelsFromBackend([{ id: 7, subject: 's', members: [1], creatorId: 1, createdAt: '', messages: [
    { id: 1, channelId: 7, senderId: 0, senderName: 'Claude Code', via: 'mcp', body: 'hi', createdAt: '' },
    { id: 2, channelId: 7, senderId: 0, senderName: 'script', body: 'yo', createdAt: '' },
  ] } as any]);
  assert.deepEqual(channel.messages.map(m => m.writer), ['mcp:Claude Code', 'outside:script']);
});
