// Pure helpers for the past-sessions UI (Settings → Past sessions): the line that says what
// the workspace's past sessions hold, and the one-line label of a result. No
// browser APIs here, so the tests run under node.

// historySummary is the line under the History heading.
export function historySummary(chunks: number, scrubbed: number): string {
  if (chunks <= 0) return 'No past sessions imported yet. Import them with -history-import (see the guide).';
  const passages = `${chunks.toLocaleString('en-US')} passage${chunks === 1 ? '' : 's'} from past sessions`;
  if (scrubbed <= 0) return passages;
  return `${passages} · ${scrubbed.toLocaleString('en-US')} with a secret removed`;
}

// resultLabel is the heading of a search result: project, date, session and
// who wrote the passage.
export function resultLabel(projectName: string, at: string, session: string, role: string): string {
  const day = at.length >= 10 ? at.slice(0, 10) : at;
  return [projectName || 'unknown project', day, session, role].filter(Boolean).join(' · ');
}
