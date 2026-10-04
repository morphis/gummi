import { expect, test } from '../fixtures/test';
import type { Page } from '@playwright/test';
import { shot } from '../fixtures/shots';

// Picking library skills: a card or a session can be given only some of
// the library's skills (and its agent is told to use them), and a line
// typed as "/skill <name>" asks the agent to use one, completed from the
// library as it is typed.

const phone = (info: { project: { name: string } }) => info.project.name === 'phone';

async function seedSkills(api: any) {
  for (const [name, description] of [['Review rules', 'How we review'], ['Deploy', 'How we ship']]) {
    const res = await api('POST', '/api/plugins', { kind: 'skill', name, content: `---\nname: ${name}\ndescription: ${description}\n---\n\n# ${name}\n` });
    expect(res.status).toBeLessThan(300);
  }
}

async function ready(page: Page, isPhone: boolean) {
  await expect(page.getByTestId('conn')).toHaveAttribute('data-state', 'live');
  if (isPhone && (await page.getByTestId('card-back').isVisible())) await page.getByTestId('card-back').click();
}

test('a card keeps the skills picked for it', async ({ pairedPage: page, api }, info) => {
  await seedSkills(api);
  await ready(page, phone(info));
  await page.getByTestId('rail-new').click();
  await expect(page.getByTestId('newcard-title')).toBeVisible();
  await expect(page.getByTestId('newcard-skills')).toContainText('Review rules');
  await page.getByTestId('newcard-title').fill('Tighten the review checklist');
  await page.getByTestId('newcard-skill-skill-review-rules').check();
  await shot(page, info, 'newcard-skills');
  await page.getByTestId('newcard-create').click();
  await expect(page.getByTestId('card-title')).toHaveText('Tighten the review checklist');
  await expect(page.getByTestId('card-skills')).toContainText('skill-review-rules');
  const id = (await page.getByTestId('card-id').textContent())!;
  expect((await api('GET', `/api/cards/${id}`)).json.skills).toEqual(['skill-review-rules']);
});

test('a goal offers no skills', async ({ pairedPage: page, api }, info) => {
  await seedSkills(api);
  await ready(page, phone(info));
  await page.getByTestId('rail-new').click();
  await expect(page.getByTestId('newcard-skills')).toBeVisible();
  await page.getByTestId('newcard-kind-goal').click();
  await expect(page.getByTestId('newcard-skills')).toHaveCount(0);
});

test('a session picks its skills and completes /skill from the library', async ({ pairedPage: page, api }, info) => {
  test.setTimeout(90_000);
  await seedSkills(api);
  await ready(page, phone(info));
  await page.getByTestId('rail-new-session').click();
  await expect(page.getByTestId('draft-hero')).toBeVisible();

  await expect(page.getByTestId('draft-skills')).toContainText('skills all');
  await page.getByTestId('draft-skills').click();
  await expect(page.getByTestId('draft-skills-pop')).toBeVisible();
  await page.getByTestId('draft-skill-skill-review-rules').check();
  await expect(page.getByTestId('draft-skills')).toContainText('Review rules');
  await shot(page, info, 'session-skills');
  await page.keyboard.press('Escape');
  await expect(page.getByTestId('draft-skills-pop')).toHaveCount(0);

  const input = page.getByTestId('composer-input');
  await input.fill('/skill de');
  const menu = page.getByTestId('composer-skills');
  await expect(menu).toBeVisible();
  await expect(page.getByTestId('composer-skill-skill-deploy')).toBeVisible();
  await expect(page.getByTestId('composer-skill-skill-review-rules')).toHaveCount(0);
  await shot(page, info, 'composer-skill-complete');
  await input.press('Tab');
  await expect(input).toHaveValue('/skill skill-deploy ');
  await expect(menu).toBeHidden();
  await input.pressSequentially('roll out v2');
  await expect(page.getByTestId('composer-says')).toContainText('starts the session');
  await input.press('Enter');

  await expect(page.getByTestId('card-id')).toHaveText(/^FF-/, { timeout: 20_000 });
  const id = (await page.getByTestId('card-id').textContent())!;
  await expect(page.getByTestId('card-title')).toHaveText('roll out v2');
  const card = (await api('GET', `/api/cards/${id}`)).json;
  expect(card.skills).toEqual(['skill-review-rules', 'skill-deploy']);
  await expect(page.getByTestId('thread')).toContainText('Use the "Deploy" skill', { timeout: 30_000 });
});
