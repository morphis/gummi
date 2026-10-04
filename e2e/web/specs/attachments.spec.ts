import fs from 'node:fs';
import { expect, test } from '../fixtures/test';
import type { Page } from '@playwright/test';
import { pixelPNG } from '../fixtures/paths';
import { shot } from '../fixtures/shots';

// Image attachments, against a real `gummi web`: the new-card form attaches
// by reference (the spec shows the image once the card exists), and the
// composer's own upload + thread rendering (the only surfaces every e2e
// profile here can actually exercise, since every one of them runs the
// headless backend — out of scope for image delivery, so its composer and
// a session draft's attach control are never offered at all).

async function openForm(page: Page) {
  await page.getByTestId('rail-new').click();
  await expect(page.getByTestId('view-newcard')).toBeVisible();
}

const phone = (info: { project: { name: string } }) => info.project.name === 'phone';

test('a new card attaches an image and its spec shows it', async ({ pairedPage: page }, info) => {
  await openForm(page);
  await page.getByTestId('newcard-title').fill('Fix the broken header layout');
  await page.getByTestId('newcard-desc').fill('See the attached screenshot.');
  await page.getByTestId('newcard-file').setInputFiles(pixelPNG);
  await expect(page.getByTestId('newcard-chips')).toContainText('pixel.png');
  await page.getByTestId('newcard-create').click();
  await expect(page.getByTestId('view-newcard')).toHaveCount(0);
  await expect(page.getByTestId('card-title')).toHaveText('Fix the broken header layout');

  await page.getByTestId('tab-spec').click();
  const img = page.getByTestId('spec-doc').locator('img');
  await expect(img).toHaveAttribute('alt', 'pixel.png');
  await expect(img).toHaveAttribute('src', /\/api\/attachments\/[0-9a-f]{64}$/);
  await expect.poll(() => img.evaluate((el: HTMLImageElement) => el.naturalWidth)).toBeGreaterThan(0);
  await shot(page, info, 'attachments-spec');
});

test('a session draft hides its attach control on the headless backend', async ({ pairedPage: page }) => {
  await expect(page.getByTestId('conn')).toHaveAttribute('data-state', 'live');
  await page.getByTestId('rail-new-session').click();
  await expect(page.getByTestId('draft-hero')).toBeVisible();

  // the draft has no card yet, so its paperclip answers for the agent the
  // draft's pair names — every profile this harness configures runs the
  // headless backend, which never reports the image capability
  await expect(page.getByTestId('composer-attach')).toBeHidden();
});

test('the composer hides its attach control on the headless backend, and a thread thumbnail loads the real bytes behind it', async ({ pairedPage: page, server, api }, info) => {
  const made = await api('POST', '/api/cards', { kind: 'freeform', title: 'Poke at the attachments' });
  const id = String(made.json?.id);
  await page.goto(`${server.url}/#${id}`);
  await expect(page.getByTestId('card-id')).toHaveText(id);
  if (phone(info)) await page.getByTestId('tab-thread').click();

  // every profile this harness configures runs the headless backend, which
  // never reports the image capability, so the control stays hidden
  await expect(page.getByTestId('composer-attach')).toBeHidden();

  // the upload endpoint itself doesn't gate on the card's backend — upload
  // through the page's own helper, for real, against the real server
  const bytes = await fs.promises.readFile(pixelPNG);
  const ref = await page.evaluate(async (b64) => {
    const { uploadAttachment } = await import('/assets/api.js');
    const data = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
    const file = new File([data], 'bug-shot.png', { type: 'image/png' });
    return uploadAttachment(file);
  }, bytes.toString('base64'));
  expect(ref.name).toBe('bug-shot.png');

  // thread.js's own rendering of a you-turn's attachments, run for real in
  // the browser: the thumbnail it builds must link to and load the bytes
  // that upload just stored.
  const probe = await page.evaluate(async (r) => {
    const { itemEl } = await import('/assets/thread.js');
    const el = itemEl({ t: 'you', text: 'see the attached screenshot', attachments: [r] });
    el.id = 'thumb-probe';
    document.body.append(el);
    const img = el.querySelector('.thumb img');
    return { src: img?.getAttribute('src'), alt: img?.getAttribute('alt') };
  }, ref);
  expect(probe.src).toBe(`/api/attachments/${ref.id}`);
  expect(probe.alt).toBe('bug-shot.png');

  const probeImg = page.locator('#thumb-probe .thumb img');
  await expect(probeImg).toBeVisible();
  await expect.poll(() => probeImg.evaluate((el: HTMLImageElement) => el.naturalWidth)).toBeGreaterThan(0);
  await shot(page, info, 'attachments-thumb');
});
