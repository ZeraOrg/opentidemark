// SPDX-License-Identifier: Apache-2.0
//
// Static CSP check of a production build (SPEC §15.7, §16): the bundle must run under
// `default-src 'self'; script-src 'self'; style-src 'self'; font-src 'self'` without exceptions.
// Usage: node scripts/check-csp.mjs <dist-dir>
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { extname, join, relative } from 'node:path';

const dist = process.argv[2] ?? 'dist';
const problems = [];

function walk(dir) {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    return statSync(path).isDirectory() ? walk(path) : [path];
  });
}

const files = walk(dist);
const rel = (f) => relative(dist, f);

const html = readFileSync(join(dist, 'index.html'), 'utf8');
for (const m of html.matchAll(/<script\b([^>]*)>([\s\S]*?)<\/script>/gi)) {
  const attrs = m[1] ?? '';
  if (!/\bsrc=/.test(attrs) || (m[2] ?? '').trim() !== '') problems.push('index.html: inline <script>');
}
if (/<style\b/i.test(html)) problems.push('index.html: inline <style>');
if (/\sstyle=/i.test(html)) problems.push('index.html: style attribute');
if (/\son[a-z]+=/i.test(html)) problems.push('index.html: inline event handler');
for (const m of html.matchAll(/(?:src|href)="([^"]+)"/gi)) {
  if (/^(https?:)?\/\//i.test(m[1] ?? '')) problems.push(`index.html: external resource ${m[1]}`);
}

let fonts = 0;
for (const file of files) {
  const ext = extname(file).toLowerCase();
  if (ext === '.woff2' || ext === '.woff') fonts++;
  if (ext === '.css') {
    const css = readFileSync(file, 'utf8');
    if (/url\(\s*["']?data:(font|application\/(font|x-font))/i.test(css))
      problems.push(`${rel(file)}: font as data: URI`);
    if (/url\(\s*["']?(https?:)?\/\//i.test(css)) problems.push(`${rel(file)}: external url() in CSS`);
    if (/@import\s+url\(\s*["']?https?:/i.test(css)) problems.push(`${rel(file)}: external @import`);
  }
  if (ext === '.js' || ext === '.html') {
    const text = readFileSync(file, 'utf8');
    if (/mockServiceWorker|setupWorker\(/.test(text))
      problems.push(`${rel(file)}: mock layer in production build`);
    if (/fonts\.googleapis|fonts\.gstatic/.test(text)) problems.push(`${rel(file)}: external font host`);
  }
}
if (fonts === 0) problems.push('no self-hosted font files in the build');

if (problems.length > 0) {
  console.error(`CSP check failed (${problems.length}):\n  ${problems.join('\n  ')}`);
  process.exit(1);
}
console.log(`CSP check passed: ${files.length} files, ${fonts} font files.`);
