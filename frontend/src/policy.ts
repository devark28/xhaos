// Agent policies (app/policy): what an agent may do without asking you.
// Pure helpers for the Approvals items in an agent's menu (manual, auto,
// dangerously allow; access and commits), the one label on its card, and
// the workspace default in Settings → Agents (manual or auto only).
import { activeAgents } from './model.ts';
import type { Agent, Policy } from './types.ts';

export type PolicyField = keyof Policy;

// Today's default for a fresh workspace (policy.Default on the server).
export const basePolicy: Policy = { access: 'rw', review: 'manual', commit: 'review' };

// The values of each field, from most to least restrictive (policy.Within's order).
const levels: { [K in PolicyField]: Policy[K][] } = {
  access: ['ro', 'rw'],
  review: ['manual', 'auto'],
  commit: ['blocked', 'review', 'allowed'],
};

export interface Option<K extends PolicyField = PolicyField> { value: Policy[K]; label: string; hint: string }

export const fields: { field: PolicyField; label: string; options: Option[] }[] = [
  { field: 'access', label: 'Access', options: [
    { value: 'rw', label: 'Read-write', hint: 'It can change files and run commands in its project.' },
    { value: 'ro', label: 'Read-only', hint: 'It reads and searches; every change and command is refused.' },
  ] },
  { field: 'review', label: 'Review', options: [
    { value: 'manual', label: 'Manual', hint: 'Everything except reads asks you.' },
    { value: 'auto', label: 'Auto', hint: 'File changes and local git run; the classifier decides other commands (asks if the classifier is off).' },
  ] },
  { field: 'commit', label: 'Commits', options: [
    { value: 'blocked', label: 'Blocked', hint: 'Commits and pushes are refused.' },
    { value: 'review', label: 'Review', hint: 'Commits and pushes ask you first.' },
    { value: 'allowed', label: 'Allowed', hint: 'Under auto review, commits run without asking (a push still asks). Only for one agent, never the default.' },
  ] },
];

// isPolicy reports whether v is a policy with known values.
export function isPolicy(v: unknown): v is Policy {
  if (!v || typeof v !== 'object') return false;
  const p = v as Record<string, unknown>;
  return (Object.keys(levels) as PolicyField[]).every(k => (levels[k] as unknown[]).includes(p[k]));
}

// policyText is the whole policy in short: "rw · manual · commits review".
export const policyText = (p: Policy) => `${p.access} · ${p.review} · commits ${p.commit}`;

// How much runs without asking, a ladder from least to most trust: the
// stored review mode (manual or auto), and dangerously allow, a timed
// override on top of it. When danger ends the agent is back on its mode.
export type Approval = 'manual' | 'auto' | 'danger';

// approvalOf is where an agent stands on the ladder; dangerLeftMs is the
// time dangerously allow has left (0 or less: off).
export const approvalOf = (p: Policy, dangerLeftMs: number): Approval => (dangerLeftMs > 0 ? 'danger' : p.review);

// policyLabel is the one label on the card: "manual", "auto", "danger 42m"
// (time left), or "read-only" for an agent that can change nothing.
export function policyLabel(p: Policy, dangerLeftMs = 0, formatLeft: (ms: number) => string = ms => `${Math.ceil(ms / 60000)}m`): string {
  if (dangerLeftMs > 0) return `danger ${formatLeft(dangerLeftMs)}`;
  return p.access === 'ro' ? 'read-only' : p.review;
}

// reviewChoices are the ladder's first two steps in the agent menu, one
// click away (dangerously allow is the third, with its own dialog).
export const reviewChoices: { value: Policy['review']; label: string; hint: string }[] = [
  { value: 'manual', label: 'Manual', hint: 'everything except reads asks you' },
  { value: 'auto', label: 'Auto', hint: 'file changes and local git run; the classifier decides other commands (asks if the classifier is off)' },
];
export const dangerHint = 'everything runs for a while';

// pickReview is what picking Manual or Auto does: it ends dangerously allow
// when that is on, and stores the mode when it differs (null: unchanged).
export function pickReview(p: Policy, dangerOn: boolean, review: Policy['review']): { endDanger: boolean; policy: Policy | null } {
  return { endDanger: dangerOn, policy: p.review === review ? null : { ...p, review } };
}

// optionsFor lists a field's choices; the workspace default never offers
// commits allowed (the server refuses it there too).
export function optionsFor(field: PolicyField, forDefault = false): Option[] {
  const list = fields.find(f => f.field === field)!.options;
  return forDefault && field === 'commit' ? list.filter(o => o.value !== 'allowed') : list;
}

// withField returns p with one field changed.
export const withField = <K extends PolicyField>(p: Policy, field: K, value: Policy[K]): Policy => ({ ...p, [field]: value });

// within reports whether p grants nothing beyond limit (policy.Within).
export const within = (p: Policy, limit: Policy) =>
  (Object.keys(levels) as PolicyField[]).every(k => (levels[k] as string[]).indexOf(p[k]) <= (levels[k] as string[]).indexOf(limit[k]));

// MenuItem is one choice in an agent's policy menu.
export interface MenuItem { field: PolicyField; value: string; label: string; hint: string; chosen: boolean; aboveParent: boolean }

// policyMenu lists the menu's choices for an agent's policy. A choice that
// would put a subagent above its parent is marked: you may still make it.
export function policyMenu(p: Policy, parent?: Policy): { field: PolicyField; label: string; items: MenuItem[] }[] {
  return fields.map(f => ({
    field: f.field,
    label: f.label,
    items: f.options.map(o => ({
      field: f.field, value: o.value, label: o.label, hint: o.hint,
      chosen: p[f.field] === o.value,
      aboveParent: !!parent && !within(withField(p, f.field, o.value as never), parent),
    })),
  }));
}

// suggestAuto: the classifier is on but the default keeps new agents on
// manual review, where it never decides.
export const suggestAuto = (p: Policy, classifierOn: boolean) => classifierOn && p.access === 'rw' && p.review === 'manual';

// reviewCounts counts active agents (subagents too) by what the classifier
// sees: read-write on auto review (it decides for them), on manual review,
// and read-only (nothing runs).
export function reviewCounts(agents: Agent[]): { auto: number; manual: number; readOnly: number } {
  const out = { auto: 0, manual: 0, readOnly: 0 };
  for (const a of activeAgents(agents)) {
    if (a.sample || !isPolicy(a.policy)) continue;
    if (a.policy.access === 'ro') out.readOnly++;
    else if (a.policy.review === 'auto') out.auto++;
    else out.manual++;
  }
  return out;
}

// countsText puts reviewCounts in words for the classifier's settings.
export function countsText(c: { auto: number; manual: number; readOnly: number }): string {
  const n = (k: number, what: string) => `${k} ${what}`;
  const parts = [n(c.auto, 'on auto review'), n(c.manual, 'on manual review')];
  if (c.readOnly) parts.push(n(c.readOnly, 'read-only'));
  const total = c.auto + c.manual + c.readOnly;
  return total ? `Active agents: ${parts.join(', ')}.` : 'No active agents.';
}
