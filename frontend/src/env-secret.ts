// Helpers for the Secrets form's "Env file" kind: what the list shows of a
// .env's variables, and reading a dropped or chosen file. The server parses
// and validates the text (secret.ParseEnv); this only mirrors its size limit.

export const ENV_MAX_BYTES = 64 << 10;

export const variablesLabel = (n: number) => `${n} variable${n === 1 ? '' : 's'}`;

/** The key names for a short line: "A, B, C" or "A, B, C and 4 more". */
export function keysSummary(keys: string[], shown = 6): string {
  if (keys.length === 0) return 'no variables';
  if (keys.length <= shown) return keys.join(', ');
  return `${keys.slice(0, shown).join(', ')} and ${keys.length - shown} more`;
}

export interface TextFile { name: string; size: number; text(): Promise<string> }

/** The text of one dropped or chosen file, or why it cannot be used. */
export async function readEnvFile(files: ArrayLike<TextFile>): Promise<{ text: string; name: string } | { error: string }> {
  if (files.length === 0) return { error: 'No file was given.' };
  if (files.length > 1) return { error: 'Drop one file: a .env holds all the variables of a secret.' };
  const file = files[0];
  if (file.size > ENV_MAX_BYTES) return { error: `${file.name} is ${Math.ceil(file.size / 1024)} KB; a secret is at most ${ENV_MAX_BYTES >> 10} KB.` };
  let text: string;
  try { text = await file.text(); } catch { return { error: `Could not read ${file.name}.` }; }
  if (text.includes('\0')) return { error: `${file.name} is not a text file.` };
  return { text, name: file.name };
}
