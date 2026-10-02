// npm ci && npx playwright install chromium && npm run test:browser
// Only donor lifecycle regressions: DONATE_BROWSER_FOCUS=lifecycle npm run test:browser
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
const fixture = await mkdtemp(resolve(tmpdir(), 'donate-browser-'));
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
async function coffeeMotion(page) {
  const logo = page.locator('pre.coffee-logo').first();
  await logo.waitFor({state:'visible'});
  const initial = await logo.textContent();
  assert.ok(initial.includes('|    |)'), 'Coffee cup must contain recognizable ASCII art');
  await page.waitForFunction(initial => document.querySelector('pre.coffee-logo')?.textContent !== initial, initial, {timeout:3000});
  const animated = await logo.textContent();
  assert.deepEqual(animated.split('\n').slice(-4), initial.split('\n').slice(-4), 'Coffee cup should stay fixed while steam moves');
  await page.emulateMedia({reducedMotion:'reduce'});
  await page.waitForTimeout(100);
  const still = await logo.textContent();
  await page.waitForTimeout(700);
  assert.equal(await logo.textContent(), still, 'Reduced-motion preference must stop coffee animation');
  await page.emulateMedia({reducedMotion:'no-preference'});
  await page.waitForFunction(still => document.querySelector('pre.coffee-logo')?.textContent !== still, still, {timeout:3000});
}
async function api(context, path, options) {
  const response = await context.request.fetch(`${base}${path}`, options);
  return {status:response.status(), data:response.status() === 204 ? null : await response.json()};
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
async function openDonor(page) {
  await hidden(page,'#donor-dialog');
  await page.locator('#donor-account-entry').click();
  await visible(page,'#donor-dialog');
  assert.equal(await page.evaluate(() => !!document.activeElement?.closest('#donor-dialog')),true,'Account dialog must receive focus');
}
async function closeDonor(page) {
  await page.locator('#close-donor').click();
  await hidden(page,'#donor-dialog');
  assert.equal(await page.evaluate(() => document.activeElement?.id),'donor-account-entry','Closing account dialog must return focus');
}
async function donorState(context) {
  const state = await api(context,'/api/donor/session');
  assert.equal(state.status,200);
  return state.data;
}
async function confirmedReceipt(page) {
  if(!(await page.locator('#checkout-title').textContent()).includes('confirmed')) {
    await page.locator('#status-refresh').click({timeout:2000}).catch(async error => {
      // Automatic polling may complete while Playwright is locating the button.
      if(!(await page.locator('#checkout-title').textContent()).includes('confirmed')) throw error;
    });
  }
  await textIncludes(page,'#checkout-title','confirmed');
}
async function crossRoleAssertion(page, prefix) {
  return page.evaluate(async prefix => {
    const decode = value => {
      const text = value.replace(/-/g,'+').replace(/_/g,'/');
      return Uint8Array.from(atob(text.padEnd(Math.ceil(text.length/4)*4,'=')),character => character.charCodeAt(0));
    };
    const encode = value => {
      let text = '';for(const byte of new Uint8Array(value)) text += String.fromCharCode(byte);
      return btoa(text).replace(/\+/g,'-').replace(/\//g,'_').replace(/=+$/,'');
    };
    const begin = await fetch(`${prefix}/login/begin`,{method:'POST',headers:{'Content-Type':'application/json'},body:'{}'});
    if(!begin.ok)return {stage:'begin',status:begin.status};
    const {publicKey} = await begin.json();
    publicKey.challenge = decode(publicKey.challenge);
    // A hostile client can remove a browser credential filter. The server must
    // still reject a genuine signature made with the other role's resident key.
    publicKey.allowCredentials = [];
    publicKey.timeout = 3000;
    const credential = await navigator.credentials.get({publicKey});
    const response = {};
    for(const field of ['clientDataJSON','authenticatorData','signature','userHandle']) {
      if(credential.response[field])response[field] = encode(credential.response[field]);
    }
    const assertion = {id:credential.id,rawId:encode(credential.rawId),type:credential.type,response,clientExtensionResults:credential.getClientExtensionResults()};
    const finish = await fetch(`${prefix}/login/finish`,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(assertion)});
    return {stage:'finish',status:finish.status};
  }, prefix);
}
async function createCustomDonation(page, amount, {name='',email='',message='',publicName=false} = {}) {
  await visible(page,'#donation-form');
  await page.locator('#currency').selectOption('USD');
  await page.locator('#amount').fill(String(amount));
  if(!await page.locator('.donor-details').evaluate(element => element.open)) await page.locator('.donor-details summary').click();
  await page.locator('#donor-name').fill(name);
  await page.locator('#donor-email').fill(email);
  await page.locator('#donor-message').fill(message);
  await page.locator('#donor-public').setChecked(publicName);
  const [response] = await Promise.all([
    page.waitForResponse(response => response.url() === `${base}/api/donations` && response.request().method() === 'POST'),
    page.locator('#donate-button').click()
  ]);
  assert.equal(response.status(),200,await response.text());
  await visible(page,'#checkout-qr');
  return {donation:await response.json(),request:response.request()};
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

function deferred() {
  let resolve;
  const promise = new Promise(done => {resolve=done;});
  return {promise,resolve};
}
async function waitForSignal(signal, description) {
  let timer;
  try {
    await Promise.race([signal.promise,new Promise((_,reject) => {
      timer=setTimeout(() => reject(new Error(`Timed out waiting for ${description}`)),15_000);
    })]);
  } finally {clearTimeout(timer);}
}
async function donorLifecycleScenarios() {
  const context = await browser.newContext({locale:'en-US',viewport:{width:1440,height:1050}});
  const page = await context.newPage();
  watch(page);
  let device = await addAuthenticator(page);
  const startupSeen=deferred(), startupRelease=deferred();
  const oldHistorySeen=deferred(), oldHistoryRelease=deferred();
  const newHistorySeen=deferred(), newHistoryRelease=deferred();
  const finishSeen=deferred(), finishRelease=deferred();
  let initialSession=true, holdHistory=false, historyIndex=0, holdFinish=false;
  let sessionRequests=0, historyRequests=0, registerRequests=0;
  const checkouts=[];
  let originalState, firstCheckout;
  // The delay fixture preserves an actual server response that expires an
  // invalid cookie. A separate Node fetch avoids applying Set-Cookie to the
  // browser before the deliberately delayed browser response is delivered.
  const expiredCookie='expired-lifecycle-fixture';
  const expiredResponse=await fetch(`${base}/api/donor/session`,{headers:{Cookie:`donate_donor=${expiredCookie}`}});
  assert.equal(expiredResponse.status,200);
  const expiredBody=await expiredResponse.text();
  assert.equal(JSON.parse(expiredBody).authenticated,false);
  const expiredClear=expiredResponse.headers.get('set-cookie');
  assert.ok(expiredClear?.includes('donate_donor='),'Expired session must send a clearing cookie');
  await context.addCookies([{name:'donate_donor',value:expiredCookie,url:base,httpOnly:true,sameSite:'Lax'}]);
  const site=await (await fetch(`${base}/api/site`)).json();
  site.methods=[{id:'lifecycle-fixture',type:'custom',name:'Lifecycle fixture',description:'',qr_url:'/favicon.svg',checkout_url:''}];
  await context.route(`${base}/api/site`,route => route.fulfill({status:200,contentType:'application/json',body:JSON.stringify(site)}));
  await context.route(`${base}/api/donations`,async route => {
    assert.equal(route.request().method(),'POST');
    checkouts.push({body:route.request().postData(),headers:route.request().headers()});
    // Keep the same unresolved checkout intent without creating a payment.
    await route.fulfill({status:409,contentType:'application/json',body:JSON.stringify({error:'Paused checkout fixture'})});
  });
  page.on('request',request => {
    const path=new URL(request.url()).pathname;
    if(path==='/api/donor/session')sessionRequests++;
    if(path==='/api/donor/donations')historyRequests++;
    if(path==='/api/donor/register/begin')registerRequests++;
  });
  await context.route(`${base}/api/donor/session`,async route => {
    if(!initialSession)return route.continue();
    initialSession=false;startupSeen.resolve();
    await startupRelease.promise;
    await route.fulfill({status:200,headers:{'Content-Type':'application/json','Set-Cookie':expiredClear},body:expiredBody});
  });
  await context.route(/\/api\/donor\/donations\?/,async route => {
    if(!holdHistory)return route.continue();
    if(++historyIndex===1) {
      oldHistorySeen.resolve();await oldHistoryRelease.promise;
      await route.fulfill({status:401,contentType:'application/json',body:JSON.stringify({error:'Delayed old-session history fixture'})});
    } else {
      newHistorySeen.resolve();await newHistoryRelease.promise;
      await route.continue();
    }
  });
  await context.route(`${base}/api/donor/register/finish`,async route => {
    if(holdFinish) {finishSeen.resolve();await finishRelease.promise;}
    await route.continue();
  });
  const retryCheckout=async () => {
    await Promise.all([
      page.waitForResponse(response => response.url()===`${base}/api/donations` && response.status()===409),
      page.locator('#donate-button').click()
    ]);
    await textIncludes(page,'#form-error','Paused checkout fixture');
    await page.waitForFunction(() => !document.querySelector('#donate-button').disabled);
    return checkouts.at(-1);
  };
  try {
    await step('Delayed expired session finishes before genuine Passkey signup; concurrent session reads are shared',async () => {
      await page.goto(base);
      await waitForSignal(startupSeen,'the delayed startup session');
      await visible(page,'#presets button');
      await openDonor(page);
      await page.locator('#donor-register').click();
      await page.waitForTimeout(200);
      assert.equal(sessionRequests,1,'Opening the dialog and registration must share the startup session read');
      assert.equal(registerRequests,0,'Registration must wait until a pending cookie-clearing response settles');
      startupRelease.resolve();
      await visible(page,'#donor-logout');
      originalState=await donorState(context);
      assert.equal(originalState.authenticated,true);
      assert.equal(originalState.passkey_count,1);
      assert.equal((await device.cdp.send('WebAuthn.getCredentials',{authenticatorId:device.authenticatorId})).credentials.length,1);
      const cookie=(await context.cookies(base)).find(cookie => cookie.name==='donate_donor');
      assert.ok(cookie?.value && cookie.value!==expiredCookie,'The real signup cookie must survive the stale clearing response');
      await closeDonor(page);
      await page.locator('#currency').selectOption('USD');
      await page.locator('#amount').fill('15');
      firstCheckout=await retryCheckout();
      assert.ok(firstCheckout.headers['idempotency-key']);
      assert.equal(firstCheckout.headers['x-csrf-token'],originalState.csrf_token);
    });
    await step('Closing and reopening during real backup enrollment sends no session or history reads until finish settles',async () => {
      holdHistory=true;
      await openDonor(page);
      await waitForSignal(oldHistorySeen,'the old session history request');
      await device.cdp.send('WebAuthn.removeVirtualAuthenticator',{authenticatorId:device.authenticatorId});
      device=await addAuthenticator(page);
      holdFinish=true;
      await page.locator('#donor-add-passkey').click();
      await waitForSignal(finishSeen,'the real backup registration finish');
      const before={sessionRequests,historyRequests};
      assert.equal(await page.locator('#donate-button').isDisabled(),true);
      await closeDonor(page);
      await openDonor(page);
      await page.waitForTimeout(200);
      assert.deepEqual({sessionRequests,historyRequests},before,'An unfinished auth mutation must exclude background reads');
      const finished=page.waitForResponse(response => response.url()===`${base}/api/donor/register/finish`);
      finishRelease.resolve();
      assert.equal((await finished).status(),200,'The real backup registration must finish even after the dialog closes');
      await waitForSignal(newHistorySeen,'history reconciliation after backup registration');
      await page.waitForFunction(() => !document.querySelector('#donate-button').disabled);
      assert.equal(await page.locator('#donor-add-passkey').isDisabled(),false,'Slow history must not lock account actions after registration');
      const after=await donorState(context);
      assert.equal(after.user.id,originalState.user.id);
      assert.equal(after.passkey_count,2);
      assert.notEqual(after.csrf_token,originalState.csrf_token,'Real backup enrollment must rotate the session');
    });
    await step('A delayed old history 401 cannot sign out the rotated session or replace a pending checkout key',async () => {
      const oldResponse=page.waitForResponse(response => new URL(response.url()).pathname==='/api/donor/donations' && response.status()===401);
      oldHistoryRelease.resolve();
      await oldResponse;
      newHistoryRelease.resolve();
      await visible(page,'#donor-history-empty');
      await hidden(page,'#donor-error');
      const after=await donorState(context);
      assert.equal(after.authenticated,true);
      assert.equal(after.user.id,originalState.user.id);
      await closeDonor(page);
      const retry=await retryCheckout();
      assert.equal(retry.body,firstCheckout.body);
      assert.equal(retry.headers['idempotency-key'],firstCheckout.headers['idempotency-key'],'Same-owner backup registration must preserve the pending checkout key');
      assert.equal(retry.headers['x-csrf-token'],after.csrf_token);
    });
    await step('Expiry of a known donor session stops that checkout; only an explicit retry creates a guest intent',async () => {
      await context.addCookies([{name:'donate_donor',value:expiredCookie,url:base,httpOnly:true,sameSite:'Lax'}]);
      const before=checkouts.length;
      await page.locator('#donate-button').click();
      await textIncludes(page,'#form-error','expired');
      await page.waitForFunction(() => !document.querySelector('#donate-button').disabled);
      assert.equal(checkouts.length,before,'An expired known account must not silently submit the same action as a guest');
      assert.equal((await donorState(context)).authenticated,false);
      assert.equal(await page.locator('#donor-account-entry').textContent(),'Sign in');
      const guest=await retryCheckout();
      assert.equal(checkouts.length,before+1);
      assert.equal(guest.body,firstCheckout.body);
      assert.equal(guest.headers['x-csrf-token'] || '','');
      assert.notEqual(guest.headers['idempotency-key'],firstCheckout.headers['idempotency-key']);
    });
  } finally {
    for(const pending of [startupRelease,oldHistoryRelease,newHistoryRelease,finishRelease])pending.resolve();
    await context.close();
  }
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
  const focus=process.env.DONATE_BROWSER_FOCUS;
  assert.ok(!focus || focus==='lifecycle','DONATE_BROWSER_FOCUS must be unset or lifecycle');
  if(!focus) {
  const adminContext = await browser.newContext({locale:'en-US',viewport:{width:1440,height:1050}});
  const admin = await adminContext.newPage();
  watch(admin);
  const authenticator = await addAuthenticator(admin);

  await step('Minimal Donate page: no default copy, animated ASCII coffee, reduced motion, policies, locales, hidden administrator entrance', async () => {
    await admin.goto(base);
    await visible(admin, '#presets button');
    assert.equal(await admin.locator('#currency').inputValue(), 'USD');
    assert.equal(await admin.locator('#donate-button').isDisabled(), true);
    assert.equal(await admin.title(), 'Donate');
    assert.equal(await admin.locator('#site-name').textContent(), 'Donate');
    assert.equal(await admin.locator('#scene, #ascii, .token-cloud, #motion-toggle, #token-response, #support-total, a[href="/api/stats"]').count(), 0);
    for (const selector of ['#site-tagline','#site-description','#site-footer-text']) {
      const element = admin.locator(selector);
      if(await element.count()) {
        assert.equal((await element.textContent()).trim(), '', `${selector} has unwanted default copy`);
        await element.waitFor({state:'hidden'});
      }
    }
    const defaults = (await api(adminContext,'/api/site')).data;
    assert.equal(defaults.name,'Donate');
    for(const field of ['tagline','description','footer']) assert.equal(defaults[field] || '', '', `Default ${field} is not blank`);
    for(const copy of Object.values(defaults.translations || {})) {
      for(const field of ['tagline','description','footer']) assert.equal(copy[field] || '', '', `Default translated ${field} is not blank`);
    }
    assert.equal(defaults.recent.length,0);
    await hidden(admin,'#recent-support');
    await coffeeMotion(admin);
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
    await admin.screenshot({path:resolve(artifacts,'public-pristine-desktop.png'),fullPage:true});
    for(let count = 0; count < 4; count++) await admin.locator('#logo').click();
    assert.equal(new URL(admin.url()).pathname,'/');
    await admin.locator('#logo').click();
    await admin.waitForURL(`${base}/admin`);
    await admin.locator('#admin-language').selectOption('en');
    await visible(admin, '#password-form');
    assert.equal(await admin.title(),'Donate');
    assert.equal(await admin.locator('#brand-name').textContent(),'Donate');
    await coffeeMotion(admin);
  });

  await step('CLI bootstrap password only grants enrollment; real WebAuthn enrollment disables password login', async () => {
    await admin.locator('#bootstrap-password').fill(password);
    await admin.locator('#password-form button[type=submit]').click();
    await visible(admin, '#passkey-register');
    assert.equal((await api(adminContext,'/api/admin/settings')).status,403);
    await admin.locator('#passkey-register').click();
    await visible(admin, '#admin-view');
    await textIncludes(admin, '#ledger-list','No records');
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
    await textIncludes(admin,'#catalog-method','Waffo not configured');
    assert.equal(await admin.locator('#load-stores').isDisabled(),true);
    await admin.locator('[data-view=notifications]').click();
    await textIncludes(admin,'#notifications-list','No records');
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
    await confirmedReceipt(publicPage);
    await hidden(publicPage,'#checkout-qr');
    const stats = (await api(publicContext,'/api/stats?currency=USD')).data;
    assert.equal(stats.count,1);
    assert.equal(stats.total_minor,1500);
    const site = (await api(publicContext,'/api/site')).data;
    assert.equal(site.recent[0].name,'Public fixture supporter');
    assert.equal(site.recent[0].message,'ASCII keeps the project alive.');
    assert.ok(!JSON.stringify(site).includes('private-fixture@example.com'));
    await publicPage.reload();
    await textIncludes(publicPage,'#checkout-title','confirmed');
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

  await step('Donate desktop and narrow layouts have no horizontal overflow', async () => {
    await publicPage.goto(base);
    await visible(publicPage,'#presets button');
    await noOverflow(publicPage);
    await publicPage.screenshot({path:resolve(artifacts,'public-desktop.png'),fullPage:true});
    await publicPage.setViewportSize({width:390,height:844});
    await noOverflow(publicPage);
    await publicPage.screenshot({path:resolve(artifacts,'public-mobile.png'),fullPage:true});
    await publicPage.setViewportSize({width:320,height:740});
    await noOverflow(publicPage);
    await visible(publicPage,'pre.coffee-logo');
    await publicPage.screenshot({path:resolve(artifacts,'public-320.png'),fullPage:true});
    await admin.setViewportSize({width:390,height:844});
    for(const view of ['ledger','site','payments','catalog','notifications','api']) {
      await admin.locator(`[data-view=${view}]`).click();
      await noOverflow(admin);
    }
    await admin.locator('[data-view=ledger]').click();
    await admin.screenshot({path:resolve(artifacts,'admin-mobile.png'),fullPage:true});
    await admin.setViewportSize({width:320,height:740});
    for(const view of ['ledger','site','payments','catalog','notifications','api']) {
      await admin.locator(`[data-view=${view}]`).click();
      await noOverflow(admin);
    }
    await admin.locator('[data-view=ledger]').click();
    await visible(admin,'pre.coffee-logo');
    await admin.screenshot({path:resolve(artifacts,'admin-320.png'),fullPage:true});
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

  const donorContext = await browser.newContext({locale:'en-US',viewport:{width:1440,height:1050}});
  const donorPage = await donorContext.newPage();
  watch(donorPage);
  let donorDevice = await addAuthenticator(donorPage);
  let donorID, accountDonationID, guestAfterLogoutID;

  await step('Optional donor account: modal focus and Escape, genuine Passkey signup, independent administrator permissions', async () => {
    assert.equal((await donorState(adminContext)).authenticated,false,'An admin session must not grant a donor account');
    assert.equal((await api(adminContext,'/api/donor/donations')).status,401);
    await donorPage.goto(base);
    await visible(donorPage,'input[name=method_id]');
    await hidden(donorPage,'#donor-dialog');
    assert.equal((await donorState(donorContext)).authenticated,false);
    await openDonor(donorPage);
    await visible(donorPage,'#donor-register');
    await visible(donorPage,'#donor-login');
    assert.equal(await donorPage.locator('#donor-dialog input[type=password], #donor-dialog input[type=email]').count(),0,'Donor account signup only uses Passkeys');
    await noOverflow(donorPage);
    await donorPage.screenshot({path:resolve(artifacts,'donor-signin-desktop.png'),fullPage:true});
    await donorPage.keyboard.press('Escape');
    await hidden(donorPage,'#donor-dialog');
    assert.equal(await donorPage.evaluate(() => document.activeElement?.id),'donor-account-entry');
    await openDonor(donorPage);
    await donorPage.locator('#donor-register').click();
    await visible(donorPage,'#donor-logout');
    const session = await donorState(donorContext);
    assert.equal(session.authenticated,true);
    assert.equal(session.passkey_count,1);
    donorID = session.user.id;
    assert.ok(donorID);
    assert.equal(session.donor_id,donorID);
    assert.equal((await donorDevice.cdp.send('WebAuthn.getCredentials',{authenticatorId:donorDevice.authenticatorId})).credentials.length,1);
    assert.equal((await api(donorContext,'/api/auth/status')).data.authenticated,false);
    assert.equal((await api(donorContext,'/api/admin/settings')).status,401,'A donor must not become an administrator');
    assert.equal((await api(donorContext,'/api/private/stats')).status,401);
    assert.equal((await api(adminContext,'/api/auth/status')).data.authenticated,true,'Donor enrollment must preserve the separate admin session');
    assert.equal((await donorState(adminContext)).authenticated,false);
    assert.equal((await api(donorContext,'/api/donor/donations')).data.total,0);
    await textIncludes(donorPage,'#donor-history-empty','No donations');
    await closeDonor(donorPage);
  });

  await step('Donor backup Passkey, sign out and sign in preserve one stable account', async () => {
    await openDonor(donorPage);
    // Remove only the simulated device, leaving its enrolled key on the server.
    // A fresh device represents the donor's separate backup security key.
    await donorDevice.cdp.send('WebAuthn.removeVirtualAuthenticator',{authenticatorId:donorDevice.authenticatorId});
    donorDevice = await addAuthenticator(donorPage);
    await donorPage.locator('#donor-add-passkey').click();
    await donorPage.waitForFunction(() => !document.querySelector('#donor-add-passkey')?.disabled);
    const backedUp = await donorState(donorContext);
    assert.equal(backedUp.user.id,donorID);
    assert.equal(backedUp.passkey_count,2);
    assert.equal((await donorDevice.cdp.send('WebAuthn.getCredentials',{authenticatorId:donorDevice.authenticatorId})).credentials.length,1);
    await donorPage.locator('#donor-logout').click();
    await visible(donorPage,'#donor-login');
    assert.equal((await donorState(donorContext)).authenticated,false);
    assert.equal((await api(donorContext,'/api/donor/donations')).status,401);
    await donorPage.locator('#donor-login').click();
    await visible(donorPage,'#donor-logout');
    const signedIn = await donorState(donorContext);
    assert.equal(signedIn.user.id,donorID);
    assert.equal(signedIn.passkey_count,2);
    await closeDonor(donorPage);
  });

  await step('Real Passkey signatures cannot cross donor and administrator authentication', async () => {
    assert.deepEqual(await crossRoleAssertion(donorPage,'/api/auth'),{stage:'finish',status:401});
    assert.equal((await donorState(donorContext)).user.id,donorID);
    assert.equal((await api(donorContext,'/api/auth/status')).data.authenticated,false);
    assert.deepEqual(await crossRoleAssertion(admin,'/api/donor'),{stage:'finish',status:401});
    assert.equal((await api(adminContext,'/api/auth/status')).data.authenticated,true);
    assert.equal((await donorState(adminContext)).authenticated,false);
  });

  await step('Signed-in donation uses server-side ownership and donor CSRF; private account history updates after confirmation', async () => {
    const created = await createCustomDonation(donorPage,5,{name:'Account fixture supporter',email:'account-private@example.com',message:'Private account contribution'});
    accountDonationID = created.donation.id;
    assert.equal(created.request.headers()['x-csrf-token'],(await donorState(donorContext)).csrf_token);
    assert.ok(!Object.hasOwn(created.request.postDataJSON(),'donor_user_id'),'The browser must not supply donation ownership');
    const ledger = (await api(adminContext,'/api/admin/donations')).data.donations;
    assert.equal(ledger.find(item => item.id === accountDonationID).donor_user_id,donorID);
    assert.equal(ledger.find(item => item.id === donationID).donor_user_id || '','','The earlier guest donation must remain unlinked');
    const history = (await api(donorContext,'/api/donor/donations')).data;
    assert.equal(history.total,1);
    assert.equal(history.donations[0].id,accountDonationID);
    assert.equal(history.donations[0].status,'pending');
    assert.ok(!JSON.stringify(history).includes('private-fixture@example.com'));
    await openDonor(donorPage);
    await textIncludes(donorPage,'#donor-history-list','Pending');
    await closeDonor(donorPage);
    await admin.locator('[data-view=ledger]').click();
    await admin.locator('#ledger-refresh').click();
    const linkedDetails = admin.locator('.donation-row').filter({hasText:accountDonationID}).locator('.donation-details');
    await linkedDetails.locator('summary').click();
    assert.ok((await linkedDetails.textContent()).includes(`Account: ${donorID}`),'Admin record must show the stable donor account ID');
    const oldGuestDetails = admin.locator('.donation-row').filter({hasText:donationID}).locator('.donation-details');
    assert.ok(!(await oldGuestDetails.textContent()).includes('Account:'),'Guest records must not show an account binding');
    await admin.locator(`[data-show-confirm="${accountDonationID}"]`).click();
    const confirmation = admin.locator(`[data-confirm-id="${accountDonationID}"]`);
    const [confirmed] = await Promise.all([
      admin.waitForResponse(response => response.url().endsWith(`/api/admin/donations/${accountDonationID}/confirm`)),
      confirmation.locator('button[type=submit]').click()
    ]);
    assert.equal(confirmed.status(),200,await confirmed.text());
    await confirmedReceipt(donorPage);
    await openDonor(donorPage);
    await textIncludes(donorPage,'#donor-history-list','Confirmed');
    assert.equal((await api(donorContext,'/api/donor/donations')).data.donations[0].status,'confirmed');
    assert.equal((await api(donorContext,'/api/site')).data.recent.length,1,'Private account donations must not appear publicly');
    assert.ok(!JSON.stringify((await api(donorContext,'/api/site')).data).includes(donorID));
    await noOverflow(donorPage);
    await donorPage.screenshot({path:resolve(artifacts,'donor-history-desktop.png'),fullPage:true});
    for(const width of [390,320]) {
      await donorPage.setViewportSize({width,height:844});
      await noOverflow(donorPage);
      await donorPage.screenshot({path:resolve(artifacts,`donor-history-${width}.png`),fullPage:true});
    }
    await closeDonor(donorPage);
  });

  await step('Signing out restores guest donation; signing back in does not claim the guest record', async () => {
    await donorPage.locator('#new-donation').click();
    await openDonor(donorPage);
    await donorPage.locator('#donor-logout').click();
    await visible(donorPage,'#donor-register');
    await closeDonor(donorPage);
    const guest = await createCustomDonation(donorPage,5,{name:'Account fixture supporter',email:'account-private@example.com',message:'Private account contribution'});
    guestAfterLogoutID = guest.donation.id;
    assert.notEqual(guestAfterLogoutID,accountDonationID,'A new guest checkout must have its own record');
    assert.equal(guest.request.headers()['x-csrf-token'] || '','');
    const guestRecord = (await api(adminContext,'/api/admin/donations')).data.donations.find(item => item.id === guestAfterLogoutID);
    assert.equal(guestRecord.donor_user_id || '','');
    await openDonor(donorPage);
    await donorPage.locator('#donor-login').click();
    await visible(donorPage,'#donor-logout');
    assert.equal((await donorState(donorContext)).user.id,donorID);
    const history = (await api(donorContext,'/api/donor/donations')).data;
    assert.equal(history.total,1);
    assert.ok(!history.donations.some(item => item.id === guestAfterLogoutID));
    await closeDonor(donorPage);
  });

  await step('Separate donor account cannot read another donor history or acquire administrator access', async () => {
    const otherContext = await browser.newContext({locale:'en-US',viewport:{width:390,height:844}});
    const otherPage = await otherContext.newPage();
    watch(otherPage);
    const otherDevice = await addAuthenticator(otherPage);
    await otherPage.goto(base);
    await visible(otherPage,'#donor-account-entry');
    await openDonor(otherPage);
    await otherPage.locator('#donor-register').click();
    await visible(otherPage,'#donor-logout');
    const other = await donorState(otherContext);
    assert.notEqual(other.user.id,donorID);
    assert.equal((await otherDevice.cdp.send('WebAuthn.getCredentials',{authenticatorId:otherDevice.authenticatorId})).credentials.length,1);
    const history = await api(otherContext,`/api/donor/donations?donor_user_id=${encodeURIComponent(donorID)}`);
    assert.equal(history.status,200);
    assert.equal(history.data.total,0);
    assert.deepEqual(history.data.donations,[]);
    assert.equal((await api(otherContext,'/api/admin/settings')).status,401);
    await noOverflow(otherPage);
    await otherContext.close();
    lastPage = donorPage;
  });

  await step('Admin and donor cookies coexist without sharing CSRF or logout authority', async () => {
    const mixed = await browser.newContext();
    await mixed.addCookies([...(await adminContext.cookies(base)),...(await donorContext.cookies(base))]);
    const adminState = (await api(mixed,'/api/auth/status')).data;
    const ownerState = await donorState(mixed);
    assert.equal(adminState.authenticated,true);
    assert.equal(ownerState.user.id,donorID);
    assert.notEqual(adminState.csrf_token,ownerState.csrf_token);
    const wrongToken = await api(mixed,'/api/donor/logout',{method:'POST',headers:{Origin:base,'X-CSRF-Token':adminState.csrf_token},data:{}});
    assert.equal(wrongToken.status,401);
    assert.equal((await donorState(mixed)).authenticated,true);
    const donorLogout = await api(mixed,'/api/donor/logout',{method:'POST',headers:{Origin:base,'X-CSRF-Token':ownerState.csrf_token},data:{}});
    assert.equal(donorLogout.status,200);
    assert.equal((await donorState(mixed)).authenticated,false);
    assert.equal((await api(mixed,'/api/auth/status')).data.authenticated,true,'Donor logout must leave admin authentication intact');
    await donorPage.reload();
    await openDonor(donorPage);
    await visible(donorPage,'#donor-login');
    await donorPage.locator('#donor-login').click();
    await visible(donorPage,'#donor-logout');
    const reauthenticated = await donorState(donorContext);
    await mixed.addCookies(await donorContext.cookies(base));
    const adminLogout = await api(mixed,'/api/auth/logout',{method:'POST',headers:{Origin:base,'X-CSRF-Token':adminState.csrf_token},data:{}});
    assert.equal(adminLogout.status,204);
    assert.equal((await api(mixed,'/api/auth/status')).data.authenticated,false);
    assert.equal((await donorState(mixed)).user.id,reauthenticated.user.id,'Admin logout must leave the donor session intact');
    await closeDonor(donorPage);
    await mixed.close();
  });

  }
  await donorLifecycleScenarios();

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
