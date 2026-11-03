const escape = (text: unknown): string => String(text).replace(/[&<>"']/g, char => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'} as Record<string, string>)[char]);
export function languageFor(path: string, content = ''): string {
  if (/\.rs\b/.test(path)) return 'rust';
  if (/\.[cm]?js\b/.test(path)) return 'javascript';
  if (/\.css\b/.test(path)) return 'css';
  if (/\.json\b/.test(path) || /^[\s]*[\[{]/.test(content)) return 'json';
  return 'text';
}
// Lightweight lexical coloring for the preview, with all source text HTML-escaped.
export function highlight(source: string, language = 'text'): string {
  if (language === 'text') return escape(source);
  const keywords = new Set((({
    javascript: 'export import from function return const let var if else try catch throw new async await class null undefined true false',
    rust: 'pub struct enum impl fn let mut use mod return if else match Some None Ok Err async await self Self true false u64 u32 bool String',
    json: 'true false null',
    bash: 'if then else fi for in do done export cd node npm cargo rg cat git',
    css: 'display flex grid relative absolute fixed auto none block'
  } as Record<string, string>)[language] || '').split(' '));
  const tokens = /("(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|`(?:\\.|[^`\\])*`|\/\/[^\n]*|#[^\n]*|\b\d+(?:\.\d+)?\b|\b[A-Za-z_$][\w$]*\b)/g;
  let result = '', end = 0;
  for (const match of String(source).matchAll(tokens)) {
    result += escape(source.slice(end, match.index!));
    const value = match[0];
    let kind = '';
    if (/^["'`]/.test(value)) kind = 'string';
    else if (value.startsWith('//') || (language === 'bash' && value.startsWith('#'))) kind = 'comment';
    else if (/^\d/.test(value)) kind = 'number';
    else if (keywords.has(value)) kind = 'keyword';
    else if (/^\s*\(/.test(source.slice(match.index! + value.length))) kind = 'function';
    result += kind ? `<span class="syntax-${kind}">${escape(value)}</span>` : escape(value);
    end = match.index! + value.length;
  }
  return result + escape(source.slice(end));
}
export function highlightDiff(source: string, language: string): string {
  return source.split('\n').map(line => {
    const kind = line.startsWith('+') ? 'addition' : line.startsWith('-') ? 'deletion' : '';
    return kind ? `<span class="syntax-${kind}">${escape(line[0])}${highlight(line.slice(1),language)}</span>` : highlight(line,language);
  }).join('\n');
}
