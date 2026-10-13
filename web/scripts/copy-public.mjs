// SPDX-License-Identifier: Apache-2.0
//
// Copies web/public into mock-public/ (the public dir of `--mode mock`, which additionally holds
// the generated MSW service worker). Cross-platform replacement for `cp -R`.
import { cpSync, mkdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
mkdirSync(join(root, 'mock-public'), { recursive: true });
cpSync(join(root, 'public'), join(root, 'mock-public'), { recursive: true });
