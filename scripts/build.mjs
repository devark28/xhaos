// Bundles the React frontend (frontend/src/ui/main.tsx) to frontend/dist/index.js
// and index.css. StyleX compiles each component's styles to atomic CSS and
// appends it to index.css (the plugin looks for that name), which starts as ui/global.css.
//   node scripts/build.mjs           one production build
//   import { watch } from this file  rebuild on change (used by scripts/dev.mjs)
import * as esbuild from 'esbuild';
import stylex from '@stylexjs/unplugin/esbuild';
import { fileURLToPath } from 'node:url';
import { resolve } from 'node:path';
import { cpSync, existsSync, rmSync } from 'node:fs';

const root = resolve(fileURLToPath(import.meta.url), '../..');
export const options = {
  entryPoints: [resolve(root, 'frontend/src/ui/main.tsx')],
  // Code splitting: Excalidraw (the Draw tab) loads as its own chunk on first use.
  outdir: resolve(root, 'frontend/dist'),
  entryNames: 'index',
  chunkNames: 'chunks/[name]-[hash]',
  splitting: true,
  conditions: ['production'], // Excalidraw exports its CSS only under a build condition
  loader: { '.woff2': 'file' }, // Excalidraw's UI font, referenced from its CSS
  assetNames: 'assets/[name]-[hash]',
  bundle: true,
  format: 'esm',
  jsx: 'automatic',
  target: 'es2022',
  sourcemap: true,
  minify: true,
  // /wails/runtime.js only exists when the Go server serves the page.
  external: ['/wails/runtime.js'],
  define: { 'process.env.NODE_ENV': '"production"' },
  logLevel: 'warning',
  metafile: true, // the StyleX plugin finds index.css through it
  plugins: [stylex({ useCSSLayers: true, dev: process.env.NODE_ENV === 'development' })],
};

export async function watch(onRebuild) {
  const ctx = await esbuild.context({
    ...options,
    plugins: [...options.plugins, { name: 'notify', setup(build) { build.onEnd(result => onRebuild?.(result)); } }],
  });
  copyAssets();
  await ctx.watch();
  return ctx;
}

// Excalidraw's fonts, served by us rather than its CDN (see Draw.tsx).
export function copyAssets() {
  const target = resolve(root, 'frontend/dist/excalidraw/fonts');
  if (!existsSync(target)) cpSync(resolve(root, 'node_modules/@excalidraw/excalidraw/dist/prod/fonts'), target, { recursive: true });
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  for (const dir of ['chunks', 'assets']) rmSync(resolve(root, 'frontend/dist', dir), { recursive: true, force: true }); // old hashed files
  await esbuild.build(options);
  copyAssets();
  console.log('built frontend/dist/index.js and index.css');
}
