// Bundles the React frontend (frontend/src/ui/main.tsx) to frontend/dist/app.js
// and app.css. StyleX compiles each component's styles to atomic CSS and
// appends it to app.css, which starts as ui/global.css.
//   node scripts/build.mjs           one production build
//   import { watch } from this file  rebuild on change (used by scripts/dev.mjs)
import * as esbuild from 'esbuild';
import stylex from '@stylexjs/unplugin/esbuild';
import { fileURLToPath } from 'node:url';
import { resolve } from 'node:path';

const root = resolve(fileURLToPath(import.meta.url), '../..');
export const options = {
  entryPoints: [resolve(root, 'frontend/src/ui/main.tsx')],
  outfile: resolve(root, 'frontend/dist/app.js'),
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
  metafile: true, // the StyleX plugin finds app.css through it
  plugins: [stylex({ useCSSLayers: true, dev: process.env.NODE_ENV === 'development' })],
};

export async function watch(onRebuild) {
  const ctx = await esbuild.context({
    ...options,
    plugins: [...options.plugins, { name: 'notify', setup(build) { build.onEnd(result => onRebuild?.(result)); } }],
  });
  await ctx.watch();
  return ctx;
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  await esbuild.build(options);
  console.log('built frontend/dist/app.js and app.css');
}
