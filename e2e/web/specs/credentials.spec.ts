import { execFileSync } from 'node:child_process';
import { mkdtempSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { expect, test } from '../fixtures/test';

// Settings takes a GitHub token and an SSH key for a host nobody has a
// shell open on. The page stores them and is only ever told what names
// them: the secret itself never comes back.

test('a token and a key are stored from settings and never shown again', async ({ pairedPage: page, api }) => {
  const dir = mkdtempSync(join(tmpdir(), 'gummi-e2e-key-'));
  execFileSync('ssh-keygen', ['-q', '-t', 'ed25519', '-N', '', '-C', 'e2e', '-f', join(dir, 'id')]);
  const key = readFileSync(join(dir, 'id'), 'utf8');
  const token = 'ghp_e2eTokenThatIsNotReal7788';

  await page.getByTestId('rail-more').click();
  await page.getByTestId('menu-settings').click();
  await expect(page.getByTestId('settings-token')).toBeVisible();
  await expect(page.getByTestId('settings-token-held')).toHaveCount(0);

  await page.getByTestId('settings-token').fill(token);
  await page.getByTestId('settings-token-save').click();
  await expect(page.getByTestId('settings-token-held')).toContainText('ending in 7788');
  await expect(page.getByTestId('settings-token')).toHaveValue('');

  // a key that does not parse is refused in the server's words
  await page.getByTestId('settings-sshkey').fill('-----BEGIN OPENSSH PRIVATE KEY-----\nnope\n-----END OPENSSH PRIVATE KEY-----');
  await page.getByTestId('settings-sshkey-save').click();
  await expect(page.getByTestId('view-error')).toContainText('not an SSH private key');
  await expect(page.getByTestId('settings-sshkey-held')).toHaveCount(0);

  await page.getByTestId('settings-sshkey').fill(key);
  await page.getByTestId('settings-sshkey-save').click();
  await expect(page.getByTestId('settings-sshkey-fp')).toContainText('SHA256:');
  await expect(page.getByTestId('settings-sshkey-pub')).toContainText('ssh-ed25519 ');
  await expect(page.getByTestId('settings-sshkey')).toHaveValue('');

  const got = await api('GET', '/api/settings');
  expect(got.json.credentials.tokenSet).toBe(true);
  expect(got.json.credentials.keySet).toBe(true);
  expect(JSON.stringify(got.json)).not.toContain(token);
  expect(JSON.stringify(got.json)).not.toContain('PRIVATE KEY');
  expect(await page.content()).not.toContain(token);

  await page.getByTestId('settings-token-forget').click();
  await expect(page.getByTestId('settings-token-held')).toHaveCount(0);
  await expect(page.getByTestId('settings-sshkey-fp')).toBeVisible();
});
