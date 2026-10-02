// npm ci && npx playwright install chromium && npm run test:browser
// Uses an isolated temporary SQLite database. No live payment provider is contacted.
import assert from 'node:assert/strict';
import {execFileSync, spawn} from 'node:child_process';
import {existsSync} from 'node:fs';
import {mkdir, mkdtemp, rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {resolve} from 'node:path';
import {createServer} from 'node:net';
import {chromium} from 'playwright';

const root = resolve(import.meta.dirname, '..');
const fixture = await mkdtemp(resolve(tmpdir(), 'token-browser-'));
const dataDir = resolve(fixture, 'data');
const binary = resolve(fixture, 'donate');
const port = Number(process.env.DONATE_TEST_PORT || 8098);
const base = `http://localhost:${port}`;
const artifacts = resolve(root, 'output/playwright');
const env = {...process.env, DONATE_DATA_DIR:dataDir, DONATE_PUBLIC_URL:base, DONATE_ADDR:`127.0.0.1:${port}`};
let server, browser, lastPage;
let serverLog = '';
const scriptErrors = [];
const unexpectedResponses = [];
const consoleErrors = [];
const steps = [];

async function step(name, action) {
  await action();
  steps.push(name);
  console.log(`✓ ${name}`);
}

async function visible(page, selector) { await page.locator(selector).first().waitFor({state:'visible'}); }
async function hidden(page, selector) { await page.locator(selector).waitFor({state:'hidden'}); }
async function textIncludes(page, selector, text) {
  await page.waitForFunction(({selector,text}) => document.querySelector(selector)?.textContent.includes(text), {selector,text});
}
async function noOverflow(page) {
  const size = await page.evaluate(() => ({width:innerWidth, content:document.documentElement.scrollWidth}));
  assert.ok(size.content <= size.width + 1, `Horizontal overflow: ${size.content} > ${size.width}`);
}
async function api(context, path, options) {
  const response = await context.request.fetch(`${base}${path}`, options);
  return {status:response.status(), data:await response.json()};
}
async function save(page, form, expected = 200) {
  const [response] = await Promise.all([
    page.waitForResponse(response => response.url() === `${base}/api/admin/settings` && response.request().method() === 'PUT'),
    page.locator(`${form} button[type=submit]`).click()
  ]);
  assert.equal(response.status(), expected, await response.text());
  if (expected === 200) await textIncludes(page, '#notice', 'Settings saved');
}
async function addAuthenticator(page) {
  const cdp = await page.context().newCDPSession(page);
  await cdp.send('WebAuthn.enable');
  const {authenticatorId} = await cdp.send('WebAuthn.addVirtualAuthenticator', {options:{protocol:'ctap2',transport:'internal',hasResidentKey:true,hasUserVerification:true,isUserVerified:true,automaticPresenceSimulation:true}});
  return {cdp, authenticatorId};
}
function watch(page) {
  lastPage = page;
  page.setDefaultTimeout(15_000);
  page.on('pageerror', error => scriptErrors.push(`${page.url()}: ${error.message}`));
  page.on('console', message => {
    // Expected API validation errors are verified explicitly below.
    if(message.type() === 'error' && !message.text().startsWith('Failed to load resource:')) consoleErrors.push(message.text());
  });
  page.on('response', response => {
    if (response.status() >= 500 || (response.status() >= 400 && !response.url().includes('/api/'))) unexpectedResponses.push(`${response.status()} ${response.url()}`);
  });
}

try {
  await mkdir(artifacts, {recursive:true});
  // Refuse a used port before the first health request, so an existing service
  // can never accidentally become this script's test target.
  await new Promise((resolve, reject) => {
    const probe = createServer();
    probe.once('error', reject);
    probe.listen(port, '127.0.0.1', () => probe.close(resolve));
  });
  execFileSync('go', ['build','-o',binary,'./cmd/donate'], {cwd:root,stdio:'pipe'});
  const password = execFileSync(binary, ['admin','password'], {cwd:root,env,encoding:'utf8'}).trim();
  server = spawn(binary, ['serve'], {cwd:root,env,stdio:['ignore','pipe','pipe']});
  server.stdout.on('data', chunk => {serverLog += chunk;});
  server.stderr.on('data', chunk => {serverLog += chunk;});
  const deadline = Date.now() + 15_000;
  while (true) {
    assert.equal(server.exitCode, null, `Test server exited: ${serverLog}`);
    try { if((await fetch(`${base}/healthz`)).ok) break; } catch {}
    assert.ok(Date.now() < deadline, `Test server did not start: ${serverLog}`);
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  const executablePath = process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE || (existsSync('/usr/bin/chromium') ? '/usr/bin/chromium' : undefined);
  browser = await chromium.launch({headless:true,executablePath});
  const adminContext = await browser.newContext({locale:'en-US',viewport:{width:1440,height:1050}});
  const admin = await adminContext.newPage();
  watch(admin);
  const authenticator = await addAuthenticator(admin);

  await step('Public page: terms, privacy, token interaction, locale currency defaults, hidden administrator entrance', async () => {
    await admin.goto(base);
    await visible(admin, '#presets button');
    assert.equal(await admin.locator('#currency').inputValue(), 'USD');
    assert.equal(await admin.locator('#donate-button').isDisabled(), true);
    await admin.locator('[data-token=coffee]').click();
    assert.ok((await admin.locator('#token-response').textContent()).length);
    await admin.locator('[data-policy=terms]').first().click();
    await visible(admin, '#policy-dialog');
    await textIncludes(admin, '#policy-content', 'open-source');
    await admin.locator('#close-policy').click();
    await admin.locator('[data-policy=privacy]').first().click();
    await textIncludes(admin, '#policy-content', 'email');
    await admin.locator('#close-policy').click();
    for (const [locale,currency] of [['zh-CN','CNY'],['zh-TW','TWD'],['en','USD']]) {
      await admin.locator('#language').selectOption(locale);
      assert.equal(await admin.locator('#currency').inputValue(),currency);
      assert.equal(await admin.locator('html').getAttribute('lang'),locale);
    }
    await noOverflow(admin);
    await admin.screenshot({path:resolve(artifacts,'public-desktop.png'),fullPage:true});
    for(let count = 0; count < 4; count++) await admin.locator('#logo').click();
    assert.equal(new URL(admin.url()).pathname,'/');
    await admin.locator('#logo').click();
    await admin.waitForURL(`${base}/admin`);
    await admin.locator('#admin-language').selectOption('en');
    await visible(admin, '#password-form');
  });

  await step('CLI bootstrap password only grants enrollment; real WebAuthn enrollment disables password login', async () => {
    await admin.locator('#bootstrap-password').fill(password);
    await admin.locator('#password-form button[type=submit]').click();
    await visible(admin, '#passkey-register');
    assert.equal((await api(adminContext,'/api/admin/settings')).status,403);
    await admin.locator('#passkey-register').click();
    await visible(admin, '#admin-view');
    await textIncludes(admin, '#ledger-list','No contributions');
    const state = (await api(adminContext,'/api/auth/status')).data;
    assert.equal(state.initialized,true);
    assert.equal(state.authenticated,true);
    assert.equal(state.passkey_count,1);
    assert.equal((await authenticator.cdp.send('WebAuthn.getCredentials',{authenticatorId:authenticator.authenticatorId})).credentials.length,1);
    assert.equal((await api(adminContext,'/api/auth/password',{method:'POST',data:{password}})).status,403);
    assert.throws(() => execFileSync(binary,['admin','password'],{env,encoding:'utf8',stdio:'pipe'}));
    await admin.locator('#logout').click();
    await visible(admin,'#passkey-login');
    await hidden(admin,'#password-form');
    await admin.locator('#passkey-login').click();
    await visible(admin,'#admin-view');
  });

  let methodID, qrURL;
  await step('Administrator UI: custom payment creation, PNG upload, editable copy and currency configuration', async () => {
    await admin.locator('[data-view=payments]').click();
    await admin.locator('#new-method-type').selectOption('custom');
    await admin.locator('#add-method').click();
    await admin.locator('[name=method-0-name]').fill('Fixture QR support');
    await admin.locator('[name=method-0-description]').fill('Fixture transfer; confirmation by maintainer.');
    await admin.locator('[name=method-0-enabled]').check();
    const imageBuffer = await admin.screenshot({clip:{x:0,y:0,width:32,height:32}});
    const [upload] = await Promise.all([
      admin.waitForResponse(response => response.url() === `${base}/api/admin/upload`),
      admin.locator('[data-upload-index="0"]').setInputFiles({name:'fixture.png',mimeType:'image/png',buffer:imageBuffer})
    ]);
    assert.equal(upload.status(),201,await upload.text());
    await admin.waitForFunction(() => document.querySelector('[name=method-0-qr_url]').value.startsWith('/uploads/'));
    await save(admin,'#methods-form');
    let settings = (await api(adminContext,'/api/admin/settings')).data;
    assert.equal(settings.methods.length,1);
    methodID = settings.methods[0].id;
    qrURL = settings.methods[0].qr_url;
    assert.equal((await adminContext.request.get(`${base}${qrURL}`)).status(),200);
    await admin.locator('[data-view=site]').click();
    await admin.locator('[name=site-contact_email]').fill('maintainer@example.com');
    await admin.locator('[name=site-footer]').fill('An independent open-source project.');
    await admin.locator('[name=locale-currency-en]').selectOption('JPY');
    await save(admin,'#site-form');
    settings = (await api(adminContext,'/api/admin/settings')).data;
    assert.equal(settings.site.language_currencies.en,'JPY');
    const publicSite = (await api(adminContext,'/api/site')).data;
    assert.equal(publicSite.methods[0].qr_url,qrURL);
    assert.equal(publicSite.contact_email,'maintainer@example.com');
    assert.ok(!JSON.stringify(publicSite).includes(settings.stats_token));
    assert.ok(!Object.hasOwn(publicSite.methods[0],'config'));
  });

  await step('Notifications and Waffo catalog show honest empty states; incomplete webhook settings are rejected', async () => {
    await admin.locator('[data-view=catalog]').click();
    await textIncludes(admin,'#catalog-method','Add a Waffo');
    assert.equal(await admin.locator('#load-stores').isDisabled(),true);
    await admin.locator('[data-view=notifications]').click();
    await textIncludes(admin,'#notifications-list','No delivery history');
    await admin.locator('[name=webhook-enabled]').check();
    await save(admin,'#notifications-form',400);
    await visible(admin,'#notice.error');
    assert.equal((await api(adminContext,'/api/admin/settings')).data.webhook.enabled,false);
    await admin.locator('[name=webhook-enabled]').uncheck();
    await save(admin,'#notifications-form');
  });

  const publicContext = await browser.newContext({locale:'en-US',viewport:{width:1440,height:1050}});
  const publicPage = await publicContext.newPage();
  watch(publicPage);
  let donationID, statusToken;
  await step('Custom donation: JPY integer validation, optional donor information, pending QR checkout, no premature totals', async () => {
    await publicPage.goto(base);
    await visible(publicPage,'input[name=method_id]');
    assert.equal(await publicPage.locator('#currency').inputValue(),'JPY');
    await publicPage.locator('#amount').fill('15.50');
    await publicPage.locator('#donate-button').click();
    await textIncludes(publicPage,'#amount-error','whole');
    assert.equal((await api(publicContext,'/api/stats?currency=JPY')).data.count,0);
    await publicPage.locator('#currency').selectOption('USD');
    await publicPage.locator('.preset[data-amount="15"]').click();
    await publicPage.locator('.donor-details summary').click();
    await publicPage.locator('#donor-name').fill('Public fixture supporter');
    await publicPage.locator('#donor-email').fill('private-fixture@example.com');
    await publicPage.locator('#donor-message').fill('ASCII keeps the project alive.');
    await publicPage.locator('#donor-public').check();
    const [response] = await Promise.all([
      publicPage.waitForResponse(response => response.url() === `${base}/api/donations` && response.request().method() === 'POST'),
      publicPage.locator('#donate-button').click()
    ]);
    assert.equal(response.status(),200,await response.text());
    assert.equal(response.request().postDataJSON().accepted_terms,true);
    const donation = await response.json();
    donationID = donation.id;
    statusToken = donation.status_token;
    assert.equal(donation.status,'pending');
    await visible(publicPage,'#checkout-qr');
    assert.ok((await publicPage.locator('#checkout-qr').getAttribute('src')).endsWith(qrURL));
    assert.equal((await api(publicContext,'/api/stats?currency=USD')).data.total_minor,0);
    assert.equal((await api(publicContext,`/api/donations/${donationID}`)).status,404);
    const privateStatus = (await api(publicContext,`/api/donations/${donationID}?token=${statusToken}`)).data;
    assert.ok(!Object.hasOwn(privateStatus,'email'));
    await publicPage.screenshot({path:resolve(artifacts,'custom-checkout.png'),fullPage:true});
  });

  await step('Administrator confirms transfer; public receipt and consent-based supporter list update', async () => {
    await admin.locator('[data-view=ledger]').click();
    await admin.locator('#ledger-refresh').click();
    await visible(admin,`[data-show-confirm="${donationID}"]`);
    await admin.locator(`[data-show-confirm="${donationID}"]`).click();
    const form = admin.locator(`[data-confirm-id="${donationID}"]`);
    await form.locator(`input[name="confirm-reference-${donationID}"]`).fill('fixture-bank-reference');
    const [response] = await Promise.all([
      admin.waitForResponse(response => response.url().endsWith(`/api/admin/donations/${donationID}/confirm`)),
      form.locator('button[type=submit]').click()
    ]);
    assert.equal(response.status(),200,await response.text());
    await textIncludes(admin,'#notice','Payment confirmed');
    await publicPage.locator('#status-refresh').click();
    await textIncludes(publicPage,'#checkout-title','arrived');
    await hidden(publicPage,'#checkout-qr');
    const stats = (await api(publicContext,'/api/stats?currency=USD')).data;
    assert.equal(stats.count,1);
    assert.equal(stats.total_minor,1500);
    const site = (await api(publicContext,'/api/site')).data;
    assert.equal(site.recent[0].name,'Public fixture supporter');
    assert.equal(site.recent[0].message,'ASCII keeps the project alive.');
    assert.ok(!JSON.stringify(site).includes('private-fixture@example.com'));
    await publicPage.reload();
    await textIncludes(publicPage,'#checkout-title','arrived');
  });

  await step('Manual offline record: exact JPY units, private donor excluded from public list, statistics API access control', async () => {
    await admin.locator('.manual-entry summary').click();
    await admin.locator('[name=manual-currency]').selectOption('JPY');
    await admin.locator('[name=manual-amount]').fill('1200');
    await admin.locator('[name=manual-name]').fill('Private fixture supporter');
    await admin.locator('[name=manual-email]').fill('offline-private@example.com');
    await admin.locator('[name=manual-method]').selectOption(methodID);
    await admin.locator('[name=manual-reference]').fill('fixture-cash');
    const [response] = await Promise.all([
      admin.waitForResponse(response => response.url() === `${base}/api/admin/donations` && response.request().method() === 'POST'),
      admin.locator('#manual-form button[type=submit]').click()
    ]);
    assert.equal(response.status(),201,await response.text());
    const jpy = (await api(publicContext,'/api/stats?currency=JPY')).data;
    assert.equal(jpy.total_minor,1200);
    assert.equal(jpy.count,1);
    assert.equal((await api(publicContext,'/api/stats?currency=USD')).data.total_minor,1500);
    const site = (await api(publicContext,'/api/site')).data;
    assert.equal(site.recent.length,1);
    assert.ok(!JSON.stringify(site).includes('Private fixture supporter'));
    await admin.locator('[data-view=api]').click();
    const token = await admin.locator('#stats-token').inputValue();
    assert.equal((await api(publicContext,'/api/private/stats')).status,401);
    assert.equal((await api(publicContext,'/api/private/stats?currency=JPY',{headers:{Authorization:`Bearer ${token}`}})).data.total_minor,1200);
    await admin.locator('[data-view=ledger]').click();
    await admin.screenshot({path:resolve(artifacts,'admin-ledger.png'),fullPage:true});
  });

  await step('Desktop and narrow screens have no horizontal overflow; reduced-motion interaction can be paused', async () => {
    await publicPage.goto(base);
    await visible(publicPage,'#presets button');
    await noOverflow(publicPage);
    await publicPage.screenshot({path:resolve(artifacts,'public-desktop.png'),fullPage:true});
    await publicPage.setViewportSize({width:390,height:844});
    await noOverflow(publicPage);
    await publicPage.screenshot({path:resolve(artifacts,'public-mobile.png'),fullPage:true});
    await publicPage.setViewportSize({width:320,height:740});
    await noOverflow(publicPage);
    await publicPage.locator('#motion-toggle').click();
    assert.equal(await publicPage.locator('#motion-toggle').getAttribute('aria-pressed'),'true');
    await admin.setViewportSize({width:390,height:844});
    for(const view of ['ledger','site','payments','catalog','notifications','api']) {
      await admin.locator(`[data-view=${view}]`).click();
      await noOverflow(admin);
    }
    await admin.locator('[data-view=ledger]').click();
    await admin.screenshot({path:resolve(artifacts,'admin-mobile.png'),fullPage:true});
  });

  await step('CLI recovery revokes the existing session and all Passkeys; new enrollment restores administration', async () => {
    const oldCookies = await adminContext.cookies(base);
    const oldCookieHeader = oldCookies.map(cookie => `${cookie.name}=${cookie.value}`).join('; ');
    const replacement = execFileSync(binary,['admin','reset'],{env,encoding:'utf8',stdio:'pipe'}).trim();
    assert.notEqual(replacement,password);
    const revoked = await fetch(`${base}/api/admin/settings`,{headers:{Cookie:oldCookieHeader}});
    assert.equal(revoked.status,401);
    const resetState = (await api(adminContext,'/api/auth/status')).data;
    assert.equal(resetState.authenticated,false);
    assert.equal(resetState.initialized,false);
    assert.equal(resetState.passkey_count,0);
    await admin.reload();
    await visible(admin,'#password-form');
    await hidden(admin,'#passkey-login');
    await authenticator.cdp.send('WebAuthn.removeVirtualAuthenticator',{authenticatorId:authenticator.authenticatorId});
    const newAuthenticator = await addAuthenticator(admin);
    await admin.locator('#bootstrap-password').fill(replacement);
    await admin.locator('#password-form button[type=submit]').click();
    await visible(admin,'#passkey-register');
    await admin.locator('#passkey-register').click();
    await visible(admin,'#admin-view');
    assert.equal((await newAuthenticator.cdp.send('WebAuthn.getCredentials',{authenticatorId:newAuthenticator.authenticatorId})).credentials.length,1);
    await admin.locator('#logout').click();
    await visible(admin,'#passkey-login');
    await admin.locator('#passkey-login').click();
    await visible(admin,'#admin-view');
    assert.equal((await api(adminContext,'/api/admin/donations')).data.total,2);
  });

  assert.deepEqual(scriptErrors,[],'Uncaught browser JavaScript errors');
  assert.deepEqual(consoleErrors,[],'Unexpected console errors');
  assert.deepEqual(unexpectedResponses,[],'Broken assets or server errors');
  await rm(resolve(artifacts,'failure.png'),{force:true});
  console.log(`\n${steps.length} browser scenarios passed. Screenshots: ${artifacts}`);
} catch(error) {
  if(lastPage && !lastPage.isClosed()) await lastPage.screenshot({path:resolve(artifacts,'failure.png'),fullPage:true}).catch(() => {});
  console.error(error);
  if(scriptErrors.length) console.error('Browser errors:',scriptErrors);
  if(serverLog) console.error('Test server log:',serverLog);
  process.exitCode = 1;
} finally {
  await browser?.close();
  if(server && server.exitCode === null) {
    const closed = new Promise(resolve => server.once('close',resolve));
    server.kill('SIGTERM');
    await Promise.race([closed,new Promise(resolve => setTimeout(resolve,3000))]);
    if(server.exitCode === null) {server.kill('SIGKILL');await closed;}
  }
  await rm(fixture,{recursive:true,force:true});
}
