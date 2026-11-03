// Model choices for new agents. Anthropic IDs are real API model IDs; the
// others are prototype labels until the backend lists configured providers.
import type { ModelChoice } from './types.ts';

export const modelChoices: ModelChoice[] = [
  { id: 'anthropic/claude-opus-5-5', provider: 'anthropic', model: 'claude-opus-5-5', label: 'Claude-opus-5.5' },
  { id: 'anthropic/claude-sonnet-5-5', provider: 'anthropic', model: 'claude-sonnet-5-5', label: 'Claude-sonnet-5.5' },
  { id: 'anthropic/claude-haiku-5-5', provider: 'anthropic', model: 'claude-haiku-5-5', label: 'Claude-haiku-5.5' },
  { id: 'openai/gpt-6.1-sol', provider: 'openai', model: 'gpt-6.1-sol', label: 'GPT-6.1-sol' },
  { id: 'deepseek/deepseek-chat', provider: 'deepseek', model: 'deepseek-chat', label: 'DeepSeek-chat' },
  { id: 'local/small-quantized', provider: 'local', model: 'small-quantized', label: 'Local-small-quantized' },
];

const STORAGE_KEY = 'xhaos-model';

export const findModel = (id: string | null | undefined): ModelChoice => modelChoices.find(choice => choice.id === id) || modelChoices[0];

export function loadModelChoice(storage: Storage | undefined = globalThis.localStorage): string {
  try { return findModel(storage?.getItem(STORAGE_KEY)).id; } catch { return modelChoices[0].id; }
}

export function saveModelChoice(id: string, storage: Storage | undefined = globalThis.localStorage) {
  try { storage?.setItem(STORAGE_KEY, findModel(id).id); } catch { /* per-browser convenience only */ }
}

export const modelLabel = (provider?: string, model?: string): string =>
  modelChoices.find(choice => choice.provider === provider && choice.model === model)?.label || model || provider || '';
