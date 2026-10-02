import fs from 'node:fs';
import { execFileSync } from 'node:child_process';
import { gummiBin, pixelPNG, repoRoot } from './paths';

// A 1x1 white PNG, base64-encoded so the fixture is a text literal rather
// than a tracked binary file. Written to disk fresh on every run.
const pixelPNGBase64 =
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAACklEQVR4nGMAAQAABQABDQottAAAAABJRU5ErkJggg==';

// Build bin/gummi once for the whole run. `go build` is incremental, so a
// second run with no Go changes costs a couple of seconds. GUMMI_E2E_BIN
// (a prebuilt binary) skips the build entirely.
export default function globalSetup(): void {
  fs.writeFileSync(pixelPNG, Buffer.from(pixelPNGBase64, 'base64'));

  if (process.env.GUMMI_E2E_BIN) return;
  const started = Date.now();
  execFileSync('go', ['build', '-o', gummiBin, './cmd/gummi'], {
    cwd: repoRoot,
    stdio: 'inherit',
  });
  console.log(`[e2e] built ${gummiBin} in ${((Date.now() - started) / 1000).toFixed(1)}s`);
}
