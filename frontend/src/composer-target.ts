// What submitting the composer does. The composer is the only way to create
// agents: no recipients means "new agent", recipients mean "message them".

// resolveSubmit maps composer text and attached recipient IDs to an intent:
//   {kind:'create', prompt, parentId}  new agent (parentId 0) or subagent
//   {kind:'message', agentIds, body}   owner message to existing agents
//   {kind:'command', text}             any other slash command
//   {kind:'focus'}                     bare /new: just focus the composer
//   {kind:'error', message} | {kind:'none'}
export type Intent =
  | { kind: 'create'; prompt: string; parentId: number }
  | { kind: 'message'; agentIds: number[]; body: string }
  | { kind: 'command'; text: string }
  | { kind: 'focus' }
  | { kind: 'error'; message: string }
  | { kind: 'none' };

export function resolveSubmit(text: string, recipients: number[] = []): Intent {
  const intent = resolve(text, recipients);
  if (intent.kind === 'message') intent.body = unescapeMentions(intent.body);
  if (intent.kind === 'create') intent.prompt = unescapeMentions(intent.prompt);
  return intent;
}
function resolve(text: string, recipients: number[]): Intent {
  text = text.trim();
  if (!text) return { kind: 'none' };
  const newCommand = text.match(/^\/new(?:\s+([\s\S]*))?$/);
  if (newCommand) {
    const prompt = (newCommand[1] || '').trim();
    if (recipients.length > 1) return { kind: 'error', message: 'Attach one agent to create its subagent, or detach all to create a new agent.' };
    return prompt ? { kind: 'create', prompt, parentId: recipients[0] || 0 } : { kind: 'focus' };
  }
  if (text.startsWith('/')) return { kind: 'command', text };
  const tell = text.match(/^tell\s+(?:agent\s+)?#?(\d+)\s*[:,]?\s+([\s\S]+)$/i);
  if (!recipients.length && tell) return { kind: 'message', agentIds: [Number(tell[1])], body: tell[2] };
  if (recipients.length) return { kind: 'message', agentIds: [...recipients], body: text };
  const prompt = text.replace(/^(?:start|create) (?:a )?new agent\s*(?:with (?:a )?task)?\s*:?\s*/i, '') || text;
  return { kind: 'create', prompt, parentId: 0 };
}

export const composerPlaceholder = (recipients: number[]): string => recipients.length
  ? `Message ${recipients.map(id => '#' + id).join(', ')} — or /new to add a subagent, / for commands`
  : 'New agent — describe the task, or / for commands';

// Typing #12 followed by a space attaches agent 12 (when it exists) and
// removes the mention from the text; \#12 keeps it as text. Only a mention
// just typed at the end counts, and never inside a slash command.
export function takeMention(text: string, exists: (id: number) => boolean): { id: number; text: string } | null {
  if (text.startsWith('/')) return null;
  const m = text.match(/(^|[\s\u00a0])#(\d+)[\s\u00a0]$/);
  if (!m || !exists(Number(m[2]))) return null;
  return { id: Number(m[2]), text: text.slice(0, m.index! + m[1].length) };
}
export const unescapeMentions = (text: string): string => text.replace(/\\#(?=\d)/g, '#');
