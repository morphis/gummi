import { expect, test } from '../fixtures/test';

// The page's markdown renderer (assets/markdown.js) on its own, in the
// served page: what it builds for a source, and that code keeps its spaces
// on screen. A bug report's repro is often an indented block whose double
// space is the bug itself; drawn as a paragraph it collapses to one.

const REPRO = [
  'Words miscounts whitespace. Run:',
  '',
  '    printf "a  b\\n c\\t d\\n" | go run ./cmd/textstat',
  '',
  'and it prints:',
  '',
  '    words 5',
  '    lines 3',
  '',
  'Expected `words  4` from `a  b`.',
].join('\n');

async function render(page: import('@playwright/test').Page, src: string) {
  return page.evaluate(async (s) => {
    const { markdown } = await import('/assets/markdown.js');
    const el = markdown(s);
    el.dataset.testid = 'md-probe';
    document.getElementById('md-probe')?.remove();
    el.id = 'md-probe';
    document.body.append(el);
    const kids = [...el.children].map((c) => ({ tag: c.tagName.toLowerCase(), text: c.textContent }));
    return kids;
  }, src);
}

test.beforeEach(({}, info) => {
  test.skip(info.project.name !== 'desktop', 'the renderer is the same on every viewport');
});

test('an indented block is code, with its spaces and lines as written', async ({ pairedPage: page }) => {
  const kids = await render(page, REPRO);
  expect(kids.map((k) => k.tag)).toEqual(['p', 'pre', 'p', 'pre', 'p']);
  expect(kids[1].text).toBe('printf "a  b\\n c\\t d\\n" | go run ./cmd/textstat');
  expect(kids[3].text).toBe('words 5\nlines 3');
  const probe = page.locator('#md-probe');
  await expect(probe.locator('pre').first()).toHaveCSS('white-space', 'pre');
  // a code span keeps its double space on screen too
  const span = probe.locator('p code').first();
  await expect(span).toHaveText('words  4');
  await expect(span).toHaveCSS('white-space', 'pre-wrap');
  // what is on screen is what was written: two lines, the double space
  expect(await probe.locator('pre').nth(1).innerText()).toBe('words 5\nlines 3');
  expect(await probe.locator('pre').first().innerText()).toContain('"a  b');
});

test('an indented block follows CommonMark’s edges', async ({ pairedPage: page }) => {
  // an indented line under a paragraph's text continues the paragraph
  let kids = await render(page, 'Some text\n    still the same paragraph');
  expect(kids.map((k) => k.tag)).toEqual(['p']);
  // a tab is four columns; blank lines inside the block are kept, the
  // blank tail is not; the block ends at the first line less indented
  kids = await render(page, '\tone\n\n\t  two\n\n\nafter');
  expect(kids.map((k) => k.tag)).toEqual(['pre', 'p']);
  expect(kids[0].text).toBe('one\n\n  two');
  // a list item's continuation is the item's, not code
  kids = await render(page, '- an item\n\n    more of the item\n- another');
  expect(kids.map((k) => k.tag)).toEqual(['ul']);
  // fenced code is unchanged, and an indented fence is code, not a fence
  kids = await render(page, '```\n  x  y\n```\n\n    ```\n    z');
  expect(kids.map((k) => k.tag)).toEqual(['pre', 'pre']);
  expect(kids[0].text).toBe('  x  y');
  expect(kids[1].text).toBe('```\nz');
});

test('a bullet run and a numbered run are two lists, with task items', async ({ pairedPage: page }) => {
  const kids = await render(page, '- one\n- two\n1. first\n2. second');
  expect(kids.map((k) => k.tag)).toEqual(['ul', 'ol']);
  const tasks = await page.evaluate(async () => {
    const { markdown } = await import('/assets/markdown.js');
    const el = markdown('- [ ] open\n- [x] done\n- plain');
    return [...el.querySelectorAll('li')].map((li) => {
      const box = li.querySelector('input[type="checkbox"]') as HTMLInputElement | null;
      return { text: li.textContent?.trim(), box: box ? box.checked : null, disabled: box?.disabled ?? null };
    });
  });
  expect(tasks).toEqual([{ text: 'open', box: false, disabled: true }, { text: 'done', box: true, disabled: true }, { text: 'plain', box: null, disabled: null }]);
});

test('strikethrough, long words and a refused link read right', async ({ pairedPage: page }) => {
  const got = await page.evaluate(async () => {
    const { markdown } = await import('/assets/markdown.js');
    const strike = markdown('was ~~old~~ now').querySelector('del')?.textContent;
    const js = markdown('[x](javascript:alert(1)) after');
    const box = document.createElement('div');
    box.style.width = '200px';
    box.append(markdown('a ' + 'w'.repeat(200) + ' https://example.com/' + 'a'.repeat(200)));
    document.body.append(box);
    const wide = box.scrollWidth <= 200;
    box.remove();
    return { strike, js: js.textContent, link: js.querySelectorAll('a').length, wide };
  });
  expect(got.strike).toBe('old');
  // the refused link is its text, with no stray ")" left behind
  expect(got.js).toBe('x after');
  expect(got.link).toBe(0);
  expect(got.wide).toBe(true);
});

test('a worktree image draws inline and nothing else is fetched as one', async ({ pairedPage: page }) => {
  const got = await page.evaluate(async () => {
    const { markdown } = await import('/assets/markdown.js');
    const files = { dir: '/w/FF-1', url: '/files/FF-1/sig/' };
    const imgs = (src: string) => [...markdown(src, { files }).querySelectorAll('img')].map((i) => i.getAttribute('src'));
    return {
      rel: imgs('![shot](out/a%20b.png)'),
      abs: imgs('![shot](/w/FF-1/out/a.png)'),
      bare: imgs('wrote /w/FF-1/out/b.jpg today'),
      outside: imgs('![x](/etc/a.png) ![y](https://example.com/a.png) ![z](../a.png)'),
      noFiles: [...markdown('![shot](out/a.png)').querySelectorAll('img')].length,
    };
  });
  expect(got.rel).toEqual(['/files/FF-1/sig/out/a%20b.png']);
  expect(got.abs).toEqual(['/files/FF-1/sig/out/a.png']);
  expect(got.bare).toEqual(['/files/FF-1/sig/out/b.jpg']);
  expect(got.outside).toEqual([]);
  expect(got.noFiles).toBe(0);
});

test('a worktree image shows a failure box and opens in a lightbox', async ({ pairedPage: page }) => {
  const got = await page.evaluate(async () => {
    const { markdown } = await import('/assets/markdown.js');
    const files = { dir: '/w/FF-1', url: '/files/FF-1/sig/' };
    const el = markdown('![shot](out/missing.png)', { files });
    document.body.append(el);
    const box = el.querySelector('.md-figure') as HTMLElement;
    await new Promise((r) => setTimeout(r, 500));
    const err = box.classList.contains('broken') && !!box.querySelector('.md-figure-err');
    const png = 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==';
    const ok = markdown(`![p](${png})`, { files });
    return { err, dataImage: ok.querySelectorAll('img').length };
  });
  expect(got.err).toBe(true);
  expect(got.dataImage).toBe(0);
});
