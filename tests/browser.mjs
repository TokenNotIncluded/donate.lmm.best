// npm ci && npx playwright install chromium && npm run test:browser
// Only donor lifecycle regressions: DONATE_BROWSER_FOCUS=lifecycle npm run test:browser
// Uses an isolated temporary SQLite database. No live payment provider is contacted.
import assert from 'node:assert/strict';
import {execFileSync, spawn} from 'node:child_process';
import {existsSync} from 'node:fs';
import {mkdir, mkdtemp, readFile, rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {resolve} from 'node:path';
import {createServer} from 'node:net';
import {createHmac} from 'node:crypto';
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
async function paintedIcon(page,selector) {
  await page.waitForFunction(selector => {
    const icon=document.querySelector(selector);
    if(!icon)return false;
    const box=icon.getBBox();
    return box.width>0 && box.height>0;
  },selector);
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
  if(!(await page.locator('#donation-thanks').isVisible())) {
    await page.locator('#status-refresh').click({timeout:2000}).catch(async error => {
      // Automatic polling may complete while Playwright is locating the button.
      if(!(await page.locator('#donation-thanks').isVisible())) throw error;
    });
  }
  await visible(page,'#donation-thanks');
  await hidden(page,'#checkout-status');
  assert.ok(!(new URL(page.url()).searchParams.has('status_token')),'A verified receipt URL must not expose its capability');
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
    // Credential filters are client-side hints. Exercise the server's role
    // policy with a genuine signature from the resident key on this device.
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
async function createCustomDonation(page, amount, {name='',email='',message='',publicName=false,publicThanks=false,currency='USD'} = {}) {
  await visible(page,'#donation-form');
  if(await page.locator('#currency').inputValue()!==currency)await page.locator('#currency').selectOption(currency);
  await page.locator('#amount').fill(String(amount));
  if(!await page.locator('.donor-details').evaluate(element => element.open)) await page.locator('.donor-details summary').click();
  await page.locator('#donor-name').fill(name);
  await page.locator('#donor-email').fill(email);
  await page.locator('#donor-message').fill(message);
  await page.locator('#donor-public').setChecked(publicName);
  await page.locator('#donor-public-thanks').setChecked(publicThanks);
  const [response] = await Promise.all([
    page.waitForResponse(response => response.url() === `${base}/api/donations` && response.request().method() === 'POST'),
    page.locator('#donate-button').click()
  ]);
  assert.equal(response.status(),200,await response.text());
  await visible(page,'#checkout-qr');
  return {donation:await response.json(),request:response.request()};
}
function updateFixtureDonation(id, changes) {
  const allowed=new Set(['method_id','method_type','method_name','provider_ref','expires_at','status']);
  assert.ok(Object.keys(changes).every(key => allowed.has(key)),'Only declared disposable lifecycle fixture columns may change');
  // Python's SQLite module edits only the database created by this script.
  // No provider API is called to manufacture a hosted checkout or a refund.
  execFileSync('python3',['-c',`import json, sqlite3, sys
changes = json.loads(sys.argv[3])
with sqlite3.connect(sys.argv[1], timeout=10) as db:
    cursor = db.execute('UPDATE donations SET ' + ','.join(key+'=?' for key in changes) + ' WHERE id=?', [*changes.values(), sys.argv[2]])
    assert cursor.rowcount == 1
`,resolve(dataDir,'donate.sqlite'),id,JSON.stringify(changes)],{stdio:'pipe'});
}
async function signedFixturePayment(context, donation, methodID, currency, amountMinor) {
  const body=JSON.stringify({id:`evt_browser_${donation.id}`,type:'checkout.session.completed',livemode:false,data:{object:{id:`cs_browser_${donation.id}`,client_reference_id:donation.id,mode:'payment',payment_status:'paid',currency:currency.toLowerCase(),amount_total:amountMinor,metadata:{donation_id:donation.id,method_id:methodID}}}});
  const timestamp=String(Math.floor(Date.now()/1000));
  const signature=createHmac('sha256','whsec_browser_fixture').update(`${timestamp}.${body}`).digest('hex');
  const path=`/api/webhooks/stripe?method_id=${methodID}`;
  for(let attempt=0;attempt<2;attempt++) {
    const response=await context.request.post(`${base}${path}`,{headers:{'Content-Type':'application/json','Stripe-Signature':`t=${timestamp},v1=${signature}`},data:body});
    assert.equal(response.status(),200,await response.text());
  }
}
async function receiptSharingSecurity(page, donation, currency, amountText) {
  await confirmedReceipt(page);
  await textIncludes(page,'#thanks-title','Thank you');
  await textIncludes(page,'#thanks-amount',amountText);
  assert.equal(await page.locator('#thanks-currency').textContent(),currency);
  assert.equal(await page.evaluate(id => sessionStorage.getItem(`donate-receipt:${id}`),donation.id),donation.status_token,'Receipt storage contains only the capability, never a cached paid status');
  const sharedURL=page.url();
  assert.ok(!sharedURL.includes(donation.status_token));
  const [fresh]=await Promise.all([
    page.waitForResponse(response => new URL(response.url()).pathname===`/api/donations/${donation.id}` && response.request().method()==='GET'),
    page.reload()
  ]);
  assert.equal(fresh.status(),200);
  await visible(page,'#donation-thanks');
  await textIncludes(page,'#thanks-amount',amountText);
  const context=await browser.newContext({locale:'en-US',viewport:{width:390,height:844}});
  const other=await context.newPage();
  watch(other);
  try {
    let receiptReads=0;
    other.on('request',request => {if(new URL(request.url()).pathname===`/api/donations/${donation.id}`)receiptReads++;});
    await other.goto(sharedURL);
    await visible(other,'#donation-form');
    await hidden(other,'#donation-thanks');
    assert.equal(receiptReads,0,'A token-free shared URL must not retrieve the private receipt on another device');
    await other.goto(`${base}/?donation=unknown-fixture&status=confirmed&amount_minor=999999&currency=USD`);
    await visible(other,'#donation-form');
    await hidden(other,'#donation-thanks');
    await other.evaluate(id => sessionStorage.setItem(`donate-receipt:${id}`,JSON.stringify({status:'confirmed',amount_minor:999999,currency:'USD'})),donation.id);
    const [rejected]=await Promise.all([
      other.waitForResponse(response => new URL(response.url()).pathname===`/api/donations/${donation.id}`),
      other.goto(sharedURL)
    ]);
    assert.equal(rejected.status(),404,'Fabricated paid data in browser storage is not a receipt capability');
    await hidden(other,'#donation-thanks');
    await context.route(`${base}/api/donations/unknown-status?*`,route => route.fulfill({status:200,contentType:'application/json',body:JSON.stringify({id:'unknown-status',status:'unrecognized',amount_minor:999999,currency:'USD',can_cancel:false})}));
    await other.goto(`${base}/?donation=unknown-status&status_token=fixture&status=confirmed`);
    await textIncludes(other,'#checkout-title','Pending');
    await hidden(other,'#donation-thanks');
    assert.ok(!new URL(other.url()).searchParams.has('status_token'));
  } finally {await context.close();}
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
async function checkoutVisualScenarios() {
  const context=await browser.newContext({locale:'en-US',viewport:{width:1440,height:1050}});
  const page=await context.newPage();
  watch(page);
  const site=await (await fetch(`${base}/api/site`)).json();
  const qrURL=site.methods.find(method => method.type==='custom')?.qr_url || '/favicon.svg';
  site.methods=[{id:'visual-card',type:'stripe',name:'Fixture card',description:''},{id:'visual-wallet',type:'paypal',name:'Fixture wallet',description:''},{id:'visual-qr',type:'custom',name:'Fixture QR',description:'',qr_url:qrURL,checkout_url:''}];
  site.language_currencies.en='USD';site.recent=[];
  for(const copy of [site,...Object.values(site.translations || {})])for(const key of ['tagline','description','footer'])copy[key]='';
  let state='pending',posts=0;
  page.on('request',request => {if(request.method()==='POST' && new URL(request.url()).pathname==='/api/donations')posts++;});
  await context.route(`${base}/api/site`,route => route.fulfill({status:200,contentType:'application/json',body:JSON.stringify(site)}));
  // Receipt presentation alone: no order is created or sent to a provider.
  await context.route(/\/api\/donations\/visual-fixture\?/,route => route.fulfill({status:200,contentType:'application/json',body:JSON.stringify({id:'visual-fixture',status:state,amount_minor:1500,currency:'USD',method_name:'Fixture QR',custom:true,can_cancel:state==='pending',expires_at:'',qr_url:state==='pending' ? qrURL : '',checkout_url:'',paid_at:state==='confirmed' ? '2026-10-03T00:00:00Z' : ''})}));
  try {
    await step('Checkout presentation: desktop and narrow pending, confirmed and failed fixtures render real sprite icons without creating payments',async () => {
      await page.goto(base);
      await visible(page,'input[name=method_id]');
      await paintedIcon(page,'#random-amount svg');
      for(const index of [0,1,2])await paintedIcon(page,`#payment-methods .method:nth-child(${index+1}) .method-icon`);
      for(const width of [1440,390,320]) {
        await page.setViewportSize({width,height:width===1440 ? 1050 : 844});
        await noOverflow(page);
        await page.screenshot({path:resolve(artifacts,`payment-method-icons-${width}.png`),fullPage:true});
      }
      for(const [status,title] of [['pending','Pending payment'],['confirmed','Donation confirmed'],['failed','Payment failed']]) {
        state=status;
        await page.goto(`${base}/?donation=visual-fixture&status_token=visual-token`);
        if(status==='confirmed') {
          await visible(page,'#donation-thanks');
          await textIncludes(page,'#thanks-amount','15.00');
          await textIncludes(page,'#thanks-currency','USD');
        } else {
          await textIncludes(page,'#checkout-title',title);
          await hidden(page,'#donation-thanks');
          await paintedIcon(page,'#checkout-icon');
        }
        if(status==='pending')await visible(page,'#checkout-qr');else await hidden(page,'#checkout-qr-container');
        for(const width of [1440,390,320]) {
          await page.setViewportSize({width,height:width===1440 ? 1050 : 844});
          await noOverflow(page);
          await page.screenshot({path:resolve(artifacts,`checkout-${status}-${width}.png`),fullPage:true});
        }
      }
      assert.equal(posts,0,'Visual state fixtures must never create donations');
    });
  } finally {await context.close();}
}

async function projectLifecycleScenarios(admin,adminContext,donorContext,methodID) {
  const guestContext=await browser.newContext({locale:'en-US',viewport:{width:1440,height:1050}});
  const guest=await guestContext.newPage();
  const owner=await donorContext.newPage();
  watch(guest);watch(owner);
  let project,cancelledGuest,cancelledOwner,expired;
  const projectState=async () => {
    const result=await api(guestContext,`/api/projects/${project.id}`);
    assert.equal(result.status,200);
    return result.data;
  };
  const confirmOffline=async donation => {
    const session=(await api(adminContext,'/api/auth/status')).data;
    const result=await api(adminContext,`/api/admin/donations/${donation.id}/confirm`,{method:'POST',headers:{Origin:base,'X-CSRF-Token':session.csrf_token},data:{reference:`project-fixture-${donation.id}`,paid_at:new Date().toISOString()}});
    assert.equal(result.status,200);
  };
  const cancelUI=async (page,donation) => {
    await visible(page,'#cancel-payment');
    const [response]=await Promise.all([
      page.waitForResponse(response => new URL(response.url()).pathname===`/api/donations/${donation.id}/cancel` && response.request().method()==='POST'),
      page.locator('#cancel-payment').click()
    ]);
    assert.equal(response.status(),200,await response.text());
    assert.equal(response.request().postDataJSON().status_token,donation.status_token);
    await textIncludes(page,'#checkout-title','Cancelled');
    await hidden(page,'#donation-thanks');
    await hidden(page,'#cancel-payment');
    await hidden(page,'#checkout-qr-container');
    const receipt=(await api(page.context(),`/api/donations/${donation.id}?token=${donation.status_token}`)).data;
    assert.equal(receipt.status,'cancelled');
    assert.equal(receipt.can_cancel,false);
    assert.ok(!receipt.checkout_url && !receipt.qr_url,'Terminal receipts must expose no payment instructions');
    return response.request();
  };
  try {
    await step('Project administration creates a real CNY goal; public language changes retain its currency and invalid checkout inputs create no records',async () => {
      await admin.locator('[data-view=projects]').click();
      await admin.locator('#project-new').click();
      await admin.locator('[name=project-id]').fill('browser-funding');
      await admin.locator('[name=project-name]').fill('Browser funding fixture');
      await admin.locator('[name=project-url]').fill('https://example.com/browser-funding');
      await admin.locator('[name=project-currency]').selectOption('CNY');
      await admin.locator('[name=project-target]').fill('1000');
      await admin.locator('[name=project-active]').check();
      const [created]=await Promise.all([
        admin.waitForResponse(response => new URL(response.url()).pathname==='/api/admin/projects' && response.request().method()==='POST'),
        admin.locator('#project-form button[type=submit]').click()
      ]);
      assert.equal(created.status(),201,await created.text());
      assert.ok(created.request().headers()['idempotency-key']);
      project=await created.json();
      assert.equal(project.id,'browser-funding');
      assert.equal(project.currency,'CNY');
      assert.equal(project.target_minor,100000);
      assert.equal(project.raised_minor,0);
      const existing=(await api(adminContext,'/api/admin/donations')).data.donations;
      assert.equal(existing.find(row => row.name==='Public fixture supporter').public_thanks,false,'Existing public-display consent must not grant the new public-thanks permission');
      assert.equal(existing.find(row => row.name==='Private fixture supporter').public_thanks,true,'Manual entry saves only the explicitly selected public-thanks consent');
      await guest.goto(`${base}/?project=${project.id}`);
      await visible(guest,'#project-overview');
      await textIncludes(guest,'#project-name',project.name);
      await textIncludes(guest,'#project-goal','1,000');
      assert.equal(await guest.locator('#currency').inputValue(),'CNY');
      await guest.locator('#language').selectOption('zh-CN');
      assert.equal(await guest.locator('#currency').inputValue(),'CNY');
      await guest.locator('#language').selectOption('en');
      assert.equal(await guest.locator('#currency').inputValue(),'CNY');
      assert.equal(await guest.locator('#currency').isEnabled(),true);
      await guest.locator('#currency').selectOption('USD');
      await guest.locator('#language').selectOption('zh-CN');
      assert.equal(await guest.locator('#currency').inputValue(),'USD');
      await guest.locator('#language').selectOption('en');
      assert.equal(await guest.locator('#currency').inputValue(),'USD');
      await guest.locator('#currency').selectOption('CNY');
      assert.equal(Number(await guest.locator('#project-progress-value').getAttribute('width')),0);
      const before=(await api(adminContext,'/api/admin/donations')).data.total;
      for(const invalid of [
        {project_id:project.id,currency:'XXX',amount_minor:1000},
        {project_id:'missing-fixture',currency:'CNY',amount_minor:1000},
        {project_id:project.id,currency:'CNY',amount_minor:1000.5},
        ...[null,'true',1].map(public_thanks => ({project_id:project.id,currency:'CNY',amount_minor:1000,public_thanks}))
      ]) {
        const response=await api(guestContext,'/api/donations',{method:'POST',headers:{Origin:base},data:{...invalid,method_id:methodID,accepted_terms:true}});
        assert.equal(response.status,400);
      }
      assert.equal((await api(adminContext,'/api/admin/donations')).data.total,before);
      assert.equal((await api(guestContext,'/api/projects/missing-fixture')).status,404);
    });
    await step('A CNY project accepts USD donations and shows them separately from its goal',async () => {
      const foreign=await createCustomDonation(guest,5,{currency:'USD'});
      assert.equal(foreign.donation.project_id,project.id);
      assert.equal(foreign.request.postDataJSON().currency,'USD');
      await confirmOffline(foreign.donation);
      const state=await projectState();
      assert.equal(state.raised_minor,0);
      assert.equal(state.count,0);
      assert.deepEqual(state.by_currency,[{currency:'USD',total_minor:500,count:1}]);
      await guest.goto(`${base}/?project=${project.id}`);
      await visible(guest,'#project-other-raised');
      await textIncludes(guest,'#project-other-raised','5.00');
      for(const width of [1440,390,320]) {
        await guest.setViewportSize({width,height:844});
        await noOverflow(guest);
      }
    });
    await step('Guest and donor cancel pending project donations; hosted expiry is server-confirmed and neither terminal state raises the goal',async () => {
      cancelledGuest=(await createCustomDonation(guest,8,{currency:'CNY'})).donation;
      assert.equal(cancelledGuest.project_id,project.id);
      assert.equal(cancelledGuest.expires_at,'','Offline QR payments have no hosted automatic deadline');
      const guestCancel=await cancelUI(guest,cancelledGuest);
      assert.equal(guestCancel.headers()['x-csrf-token'] || '','');
      assert.equal((await api(guestContext,`/api/donations/${cancelledGuest.id}/cancel`,{method:'POST',headers:{Origin:base},data:{status_token:cancelledGuest.status_token}})).status,200);
      await owner.goto(`${base}/?project=${project.id}`);
      await visible(owner,'#project-overview');
      assert.equal(await owner.locator('#donor-public-thanks').isChecked(),false,'Each new donation requires a fresh public-thanks opt-in');
      const owned=await createCustomDonation(owner,9,{currency:'CNY',name:'Project account fixture',email:'project-permission-private@example.com',publicThanks:true});
      cancelledOwner=owned.donation;
      assert.equal(owned.request.postDataJSON().project_id,project.id);
      assert.equal(owned.request.postDataJSON().public_thanks,true);
      const ownerCancel=await cancelUI(owner,cancelledOwner);
      const ownerSession=await donorState(donorContext);
      assert.equal(ownerCancel.headers()['x-csrf-token'],ownerSession.csrf_token);
      const row=(await api(adminContext,'/api/admin/donations')).data.donations.find(row => row.id===cancelledOwner.id);
      assert.equal(row.donor_user_id,ownerSession.user.id);
      assert.equal(row.project_id,project.id);
      assert.equal(row.public_thanks,true);
      await admin.locator('[data-view=ledger]').click();
      await admin.locator('#ledger-refresh').click();
      const permission=admin.locator('.donation-row').filter({hasText:cancelledOwner.id}).locator('.donation-details');
      await permission.locator('summary').click();
      assert.ok((await permission.textContent()).includes('Allow public thanks: Yes'));
      assert.equal((await projectState()).raised_minor,0);
      for(const path of [`/api/projects/${project.id}`,'/api/site','/api/stats?currency=CNY'])assert.ok(!JSON.stringify((await api(guestContext,path)).data).includes('project-permission-private@example.com'),'Public thanks consent must not expose donor email');
      await guest.locator('#new-donation').click();
      expired=(await createCustomDonation(guest,13,{currency:'CNY'})).donation;
      const settings=(await api(adminContext,'/api/admin/settings')).data;
      settings.methods.push({id:'lifecycle-stripe',type:'stripe',name:'Lifecycle signed fixture',enabled:false,config:{secret_key:'sk_test_browser_fixture',webhook_secret:'whsec_browser_fixture'}});
      const adminSession=(await api(adminContext,'/api/auth/status')).data;
      assert.equal((await api(adminContext,'/api/admin/settings',{method:'PUT',headers:{Origin:base,'X-CSRF-Token':adminSession.csrf_token},data:settings})).status,200);
      // Replace only this disposable row's provider/deadline. The disabled
      // Stripe method cannot create a checkout or contact Stripe's API.
      updateFixtureDonation(expired.id,{method_id:'lifecycle-stripe',method_type:'stripe',method_name:'Lifecycle signed fixture',provider_ref:`cs_browser_${expired.id}`,expires_at:new Date(Date.now()-60_000).toISOString()});
      await guest.locator('#status-refresh').click();
      await textIncludes(guest,'#checkout-title','Expired');
      await hidden(guest,'#cancel-payment');
      await hidden(guest,'#donation-thanks');
      const receipt=(await api(guestContext,`/api/donations/${expired.id}?token=${expired.status_token}`)).data;
      assert.equal(receipt.status,'expired');
      assert.equal(receipt.can_cancel,false);
      assert.ok(receipt.expires_at.endsWith('Z'));
      assert.ok(!receipt.checkout_url && !receipt.qr_url);
      assert.equal((await projectState()).raised_minor,0);
      assert.equal((await projectState()).count,0);
      for(const [page,state] of [[owner,'cancelled'],[guest,'expired']])for(const width of [1440,390,320]) {
        await page.setViewportSize({width,height:width===1440 ? 1050 : 844});
        await noOverflow(page);
        await page.screenshot({path:resolve(artifacts,`project-${state}-${width}.png`),fullPage:true});
      }
    });
    await step('A manual offline confirmation and genuine local signed late payment raise only their project; receipt sharing never exposes a success token',async () => {
      const genericUSD=(await api(guestContext,'/api/stats?currency=USD')).data.total_minor;
      await confirmOffline(cancelledOwner);
      await confirmedReceipt(owner);
      await textIncludes(owner,'#thanks-project',project.name);
      assert.equal((await projectState()).raised_minor,900);
      await signedFixturePayment(guestContext,expired,'lifecycle-stripe','CNY',1300);
      await confirmedReceipt(guest);
      const state=await projectState();
      assert.equal(state.raised_minor,2200);
      assert.equal(state.count,2);
      assert.ok(Math.abs(state.progress-2.2)<0.001);
      assert.equal((await api(guestContext,'/api/stats?currency=USD')).data.total_minor,genericUSD,'Project payment must not contaminate a different currency');
      assert.equal((await api(guestContext,'/api/stats?currency=CNY')).data.total_minor,2200);
      const history=(await api(donorContext,'/api/donor/donations')).data;
      const ownProject=history.donations.find(row => row.id===cancelledOwner.id);
      assert.equal(ownProject.project_id,project.id);
      assert.equal(ownProject.project_name,project.name);
      assert.equal(ownProject.status,'confirmed');
      assert.equal(ownProject.public_thanks,true);
      assert.ok(!JSON.stringify(history).includes('project-permission-private@example.com'));
      assert.ok(!history.donations.some(row => row.id===expired.id),'A guest project payment is not owned by the signed-in donor');
      await textIncludes(guest,'#thanks-project',project.name);
      await receiptSharingSecurity(guest,expired,'CNY','13.00');
      for(const width of [1440,390,320]) {
        await guest.setViewportSize({width,height:width===1440 ? 1050 : 844});
        await noOverflow(guest);
        await guest.screenshot({path:resolve(artifacts,`project-thanks-${width}.png`),fullPage:true});
      }
      await guest.locator('#thanks-back').click();
      assert.equal(new URL(guest.url()).searchParams.get('project'),project.id);
      assert.equal(new URL(guest.url()).searchParams.has('donation'),false);
      await visible(guest,'#project-overview');
      await textIncludes(guest,'#project-raised','22.00');
      assert.ok(Math.abs(Number(await guest.locator('#project-progress-value').getAttribute('width'))-2.2)<0.001);
      for(const width of [1440,390,320]) {
        await guest.setViewportSize({width,height:width===1440 ? 1050 : 844});
        await noOverflow(guest);
        await guest.screenshot({path:resolve(artifacts,`project-page-${width}.png`),fullPage:true});
      }
    });
    await step('Project goal SVG exports pin the project currency and period, link to its page, and exclude a controlled refunded fixture',async () => {
      await admin.locator('[data-view=badges]').click();
      await admin.locator('#badge-form [name=lang]').selectOption('en');
      await admin.locator('#badge-form [name=project]').selectOption(project.id);
      await admin.locator('#badge-form [name=title]').fill('');
      await admin.locator('#badge-form').evaluate(form => form.requestSubmit());
      assert.equal(await admin.locator('#badge-form [name=currency]').isDisabled(),true);
      assert.equal(await admin.locator('#badge-form [name=period]').isDisabled(),true);
      await admin.waitForFunction(() => {const img=document.querySelector('#badge-preview');return img.complete && img.naturalWidth>0 && img.naturalHeight>0;});
      const badgeURL=new URL(await admin.locator('#badge-preview').getAttribute('src'));
      assert.equal(badgeURL.searchParams.get('project'),project.id);
      assert.equal(badgeURL.searchParams.has('currency'),false);
      assert.equal(badgeURL.searchParams.has('period'),false);
      for(const key of badgeURL.searchParams.keys())assert.ok(['project','lang','layout','theme','width','title','amount_label','count_label','animation'].includes(key));
      const freshSVG=async () => {
        const response=await fetch(badgeURL,{cache:'no-store'});
        assert.equal(response.status,200);
        assert.ok(response.headers.get('content-type')?.startsWith('image/svg+xml'));
        const svg=await response.text();
        assert.ok(svg.includes(project.name));
        assert.ok(svg.includes('1,000.00'));
        return svg;
      };
      const fundedSVG=await freshSVG();
      assert.ok(fundedSVG.includes('22.00'));
      assert.ok(fundedSVG.includes('2.2%'));
      for(const query of ['currency=USD','period=7d'])assert.equal((await guestContext.request.get(`${base}/badge.svg?project=${project.id}&${query}`)).status(),400);
      assert.equal((await guestContext.request.get(`${base}/badge.svg?project=missing-fixture`)).status(),404);
      await adminContext.grantPermissions(['clipboard-read','clipboard-write'],{origin:base});
      await admin.locator('[data-badge-copy=readme]').click();
      assert.ok((await admin.locator('#badge-code').inputValue()).endsWith(`](${base}/?project=${project.id})`));
      await admin.locator('[data-badge-copy=html]').click();
      assert.ok((await admin.locator('#badge-code').inputValue()).includes(`href="${base}/?project=${project.id}"`));
      for(const width of [1440,390,320]) {
        await admin.setViewportSize({width,height:width===1440 ? 1050 : 844});
        await noOverflow(admin);
        await admin.screenshot({path:resolve(artifacts,`admin-project-badge-${width}.png`),fullPage:true});
      }
      // This is an aggregation/refund presentation fixture, not a fabricated
      // production refund or a request to Stripe's refund/session endpoints.
      updateFixtureDonation(expired.id,{status:'refunded'});
      const refunded=await projectState();
      assert.equal(refunded.raised_minor,900);
      assert.equal(refunded.count,1);
      assert.ok(Math.abs(refunded.progress-0.9)<0.001);
      assert.equal((await api(guestContext,'/api/stats?currency=CNY')).data.total_minor,900);
      const refundedSVG=await freshSVG();
      assert.ok(refundedSVG.includes('9.00'));
      assert.ok(refundedSVG.includes('0.9%'));
      await guest.goto(`${base}/?project=${project.id}&donation=${expired.id}`);
      await textIncludes(guest,'#checkout-title','Refunded');
      await hidden(guest,'#donation-thanks');
    });
    await step('Project editing retains the slug; archiving hides its public page and blocks new project checkout',async () => {
      await admin.locator('[data-view=projects]').click();
      await admin.locator(`[data-edit-project="${project.id}"]`).click();
      await admin.locator('[name=project-name]').fill('Browser funding edited');
      const [updated]=await Promise.all([
        admin.waitForResponse(response => new URL(response.url()).pathname===`/api/admin/projects/${project.id}` && response.request().method()==='PUT'),
        admin.locator('#project-form button[type=submit]').click()
      ]);
      assert.equal(updated.status(),200,await updated.text());
      assert.equal((await updated.json()).id,project.id);
      admin.once('dialog',dialog => dialog.accept());
      const [archived]=await Promise.all([
        admin.waitForResponse(response => new URL(response.url()).pathname===`/api/admin/projects/${project.id}` && response.request().method()==='PUT'),
        admin.locator(`[data-project-active="${project.id}"]`).click()
      ]);
      assert.equal(archived.status(),200,await archived.text());
      assert.equal((await api(guestContext,`/api/projects/${project.id}`)).status,404);
      const failed=await api(guestContext,'/api/donations',{method:'POST',headers:{Origin:base},data:{project_id:project.id,currency:'CNY',amount_minor:100,method_id:methodID,accepted_terms:true}});
      assert.equal(failed.status,400);
      await guest.goto(`${base}/?project=${project.id}`);
      await visible(guest,'#project-error');
      assert.equal(await guest.locator('#donate-button').isDisabled(),true);
      await hidden(guest,'#donation-thanks');
    });
  } finally {await guestContext.close();await owner.close();}
}

async function announcementScenario(admin,adminContext) {
  const context=await browser.newContext({locale:'en-US',viewport:{width:1440,height:1050}});
  const page=await context.newPage();watch(page);
  try {
    await step('An administrator announcement displays safe literal text and a link; an unsafe URL is rejected and empty configuration stays hidden',async () => {
      await admin.locator('[data-view=site]').click();
      await admin.locator('[name=site-announcement]').fill('Funding <fixture> & progress');
      await admin.locator('[name=site-announcement_url]').fill('https://example.com/funding');
      await save(admin,'#site-form');
      await page.goto(base);
      await visible(page,'#site-announcement');
      assert.equal(await page.locator('#announcement-text').textContent(),'Funding <fixture> & progress');
      assert.equal(await page.locator('#announcement-text *').count(),0,'Configured announcement text must not become HTML');
      await hidden(page,'#announcement-text');
      await visible(page,'#announcement-link');
      assert.equal(await page.locator('#announcement-link').textContent(),'Funding <fixture> & progress');
      assert.equal(await page.locator('#announcement-link').getAttribute('href'),'https://example.com/funding');
      for(const rel of ['noopener','noreferrer'])assert.ok((await page.locator('#announcement-link').getAttribute('rel')).includes(rel));
      for(const width of [1440,390,320]) {
        await page.setViewportSize({width,height:width===1440 ? 1050 : 844});
        await noOverflow(page);
        await page.screenshot({path:resolve(artifacts,`announcement-${width}.png`),fullPage:true});
      }
      const settings=(await api(adminContext,'/api/admin/settings')).data;
      settings.site.announcement_url='javascript:alert(1)';
      const session=(await api(adminContext,'/api/auth/status')).data;
      assert.equal((await api(adminContext,'/api/admin/settings',{method:'PUT',headers:{Origin:base,'X-CSRF-Token':session.csrf_token},data:settings})).status,400);
      assert.equal((await api(adminContext,'/api/admin/settings')).data.site.announcement_url,'https://example.com/funding');
      await admin.locator('[name=site-announcement]').fill('');
      await admin.locator('[name=site-announcement_url]').fill('');
      await save(admin,'#site-form');
      await page.reload();
      await visible(page,'#donation-form');
      await hidden(page,'#site-announcement');
    });
  } finally {await context.close();}
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
  let activeAdminDevice=authenticator;

  await step('Minimal Donate page: no default copy, animated ASCII coffee, reduced motion, policies, locales, hidden administrator entrance', async () => {
    let donationPosts=0;
    const countDonationPosts=request => {if(new URL(request.url()).pathname==='/api/donations' && request.method()==='POST')donationPosts++;};
    admin.on('request',countDonationPosts);
    await admin.goto(base);
    await visible(admin, '#presets button');
    assert.equal(await admin.locator('#currency').inputValue(), 'USD');
    assert.equal(await admin.locator('#donate-button').isDisabled(), true);
    assert.equal(await admin.locator('#donor-public-thanks').isChecked(),false);
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
      await admin.locator('#random-amount').click();
      const randomized=Number(await admin.locator('#amount').inputValue());
      assert.ok(Number.isInteger(randomized) && randomized>0 && randomized<=1_000_000,'Random amount must be a legal whole amount in every locale currency');
      assert.equal(await admin.locator('#donate-button').isDisabled(),true,'Randomizing an amount must not enable donation without a payment method');
    }
    assert.equal(donationPosts,0,'Random amount must only fill the input, never submit a donation');
    assert.equal(await admin.getByRole('button',{name:'Random amount',exact:true}).count(),1);
    await paintedIcon(admin,'#random-amount svg');
    assert.equal(await admin.locator('#donation-form').getByRole('button',{name:'Donate',exact:true}).count(),1);
    assert.deepEqual(await admin.locator('svg[data-icon]').evaluateAll(icons => icons.filter(icon => icon.getAttribute('aria-hidden')!=='true' || icon.getAttribute('focusable')!=='false').map(icon => icon.dataset.icon)),[],'Decorative icons must not add accessible names');
    admin.off('request',countDonationPosts);
    await admin.locator('#amount').fill('15');
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
    let donationPosts=0;
    const countDonationPosts=request => {if(new URL(request.url()).pathname==='/api/donations' && request.method()==='POST')donationPosts++;};
    publicPage.on('request',countDonationPosts);
    await publicPage.goto(base);
    await visible(publicPage,'input[name=method_id]');
    await paintedIcon(publicPage,'#payment-methods .method-icon');
    assert.equal(await publicPage.locator('#currency').inputValue(),'JPY');
    await publicPage.locator('#amount').fill('15.50');
    await publicPage.locator('#donate-button').click();
    await textIncludes(publicPage,'#amount-error','whole');
    assert.equal((await api(publicContext,'/api/stats?currency=JPY')).data.count,0);
    await publicPage.locator('#random-amount').click();
    const randomized=Number(await publicPage.locator('#amount').inputValue());
    assert.ok(Number.isInteger(randomized) && randomized>0 && randomized<=1_000_000,'Random amount must fit the JPY whole-unit input');
    await hidden(publicPage,'#amount-error');
    assert.equal(donationPosts,0,'A random amount must not submit the configured payment method');
    assert.equal(await publicPage.getByRole('radio',{name:'Fixture QR support Fixture transfer; confirmation by maintainer.',exact:true}).count(),1,'A payment icon must not change the radio accessible name');
    publicPage.off('request',countDonationPosts);
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
    await paintedIcon(publicPage,'#checkout-icon');
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
    await visible(publicPage,'#donation-thanks');
  });

  await step('Manual offline record: exact JPY units, private donor excluded from public list, statistics API access control', async () => {
    await admin.locator('.manual-entry summary').click();
    await admin.locator('[name=manual-currency]').selectOption('JPY');
    assert.equal(await admin.locator('[name=manual-public_thanks]').isChecked(),false);
    await admin.locator('[name=manual-public_thanks]').check();
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
    assert.equal(response.request().postDataJSON().public_thanks,true);
    assert.equal((await response.json()).public_thanks,true);
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

  await step('Public SVG badge builder: confirmed currency totals, styling, safe copy output and fresh downloadable settings', async () => {
    await admin.locator('[data-view=badges]').click();
    await visible(admin,'#badge-form');
    const options=await admin.locator('#badge-form [name=currency] option').evaluateAll(options => options.map(option => option.value));
    assert.deepEqual(options,(await api(adminContext,'/api/admin/settings')).data.site.currencies);
    await admin.locator('#badge-form [name=lang]').selectOption('en');
    await admin.locator('#badge-form [name=currency]').selectOption('USD');
    await admin.locator('#badge-form [name=period]').selectOption('all');
    await admin.locator('#badge-form [name=layout]').selectOption('receipt');
    await admin.locator('#badge-form [name=theme]').selectOption('dark');
    await admin.locator('#badge-form [name=animation]').selectOption('none');
    await admin.locator('#badge-form [name=title]').fill('Donate <fixture> & support');
    await admin.locator('#badge-form').evaluate(form => form.requestSubmit());
    const publicSVG=async () => {
      await admin.waitForFunction(() => {const image=document.querySelector('#badge-preview');return !image.hidden && image.complete && image.naturalWidth>0 && image.naturalHeight>0;});
      await hidden(admin,'#badge-status');
      const url=new URL(await admin.locator('#badge-preview').getAttribute('src'));
      assert.equal(url.origin,base);
      assert.equal(url.pathname,'/badge.svg');
      for(const key of url.searchParams.keys()) assert.ok(['lang','currency','period','layout','theme','width','title','amount_label','count_label','animation'].includes(key),'A public badge URL must not contain credentials');
      // No admin cookie or private API token: the exported badge is public.
      const response=await fetch(url);
      assert.equal(response.status,200);
      assert.ok(response.headers.get('content-type')?.startsWith('image/svg+xml'));
      const xml=await response.text();
      const parsed=await admin.evaluate(xml => {
        const document=new DOMParser().parseFromString(xml,'image/svg+xml');
        return {invalid:!!document.querySelector('parsererror'),name:document.documentElement.localName,texts:[...document.querySelectorAll('title,desc,text')].map(element => element.textContent),label:document.documentElement.getAttribute('aria-label') || ''};
      },xml);
      assert.equal(parsed.invalid,false,'Downloaded badge must be valid XML');
      assert.equal(parsed.name,'svg');
      assert.ok(!xml.includes('private-fixture@example.com') && !xml.includes('offline-private@example.com'));
      return {url:url.href,xml,texts:parsed.texts,content:`${parsed.label}\n${parsed.texts.join('\n')}`};
    };
    const usd=await publicSVG();
    assert.ok(usd.texts.includes('USD 15.00'),'USD badge must display exactly its confirmed USD 15.00');
    assert.ok(!/1,?200/.test(usd.content),'USD badge must not combine the JPY donation');
    assert.ok(usd.content.includes('Donate <fixture> & support'),'Custom badge title must survive safe XML escaping');
    await admin.screenshot({path:resolve(artifacts,'admin-badge-desktop.png'),fullPage:true});
    await admin.locator('#badge-form [name=currency]').selectOption('JPY');
    await admin.locator('#badge-form [name=layout]').selectOption('compact');
    await admin.locator('#badge-form [name=theme]').selectOption('transparent');
    await admin.locator('#badge-form').evaluate(form => form.requestSubmit());
    const jpy=await publicSVG();
    assert.ok(jpy.texts.includes('JPY 1,200'),'JPY badge must preserve its exact whole-unit confirmed total');
    assert.ok(!/15[.,]00/.test(jpy.content),'JPY badge must not combine the USD donation');
    assert.equal(new URL(jpy.url).searchParams.get('layout'),'compact');
    assert.equal(new URL(jpy.url).searchParams.get('theme'),'transparent');
    await adminContext.grantPermissions(['clipboard-read','clipboard-write'],{origin:base});
    for(const format of ['readme','html','url']) {
      await admin.locator(`[data-badge-copy=${format}]`).click();
      const code=await admin.locator('#badge-code').inputValue();
      await admin.waitForFunction(async code => (await navigator.clipboard.readText())===code,code);
      if(format==='readme') assert.equal(code,`[![Donate](${jpy.url})](${base}/)`);
      if(format==='url') assert.equal(code,jpy.url);
      if(format==='html') {
        const target=await admin.evaluate(code => {const document=new DOMParser().parseFromString(code,'text/html');return {home:document.querySelector('a').getAttribute('href'),image:document.querySelector('img').getAttribute('src')};},code);
        assert.deepEqual(target,{home:`${base}/`,image:jpy.url});
      }
    }
    // Clicking download immediately after an edit must flush the debounce.
    const [download]=await Promise.all([admin.waitForEvent('download'),admin.evaluate(() => {
      for(const [name,value] of [['width','360'],['title','Fresh download']]) {
        const input=document.querySelector(`#badge-form [name=${name}]`);input.value=value;input.dispatchEvent(new Event('input',{bubbles:true}));
      }
      document.querySelector('#badge-download').click();
    })]);
    assert.equal(download.suggestedFilename(),'donate-JPY-all.svg');
    assert.equal(await download.failure(),null);
    const path=resolve(artifacts,'donate-JPY-all.svg');
    await download.saveAs(path);
    const downloaded=await readFile(path,'utf8');
    const current=await admin.evaluate(xml => {const document=new DOMParser().parseFromString(xml,'image/svg+xml');return {invalid:!!document.querySelector('parsererror'),width:document.documentElement.getAttribute('width'),text:document.documentElement.textContent};},downloaded);
    assert.equal(current.invalid,false);
    assert.equal(current.width,'360');
    assert.ok(current.text.includes('Fresh download'),'Download must include the newest title');
    let invalidDownloads=0;
    const countInvalid=() => invalidDownloads++;
    admin.on('download',countInvalid);
    await admin.evaluate(() => {
      const input=document.querySelector('#badge-form [name=width]');input.value='239';input.dispatchEvent(new Event('input',{bubbles:true}));
      document.querySelector('#badge-download').click();
    });
    await visible(admin,'#badge-status');
    assert.equal(await admin.locator('#badge-download').getAttribute('href'),null);
    await admin.waitForTimeout(200);
    assert.equal(invalidDownloads,0,'Invalid immediate download must not use the previous valid URL');
    admin.off('download',countInvalid);
    await admin.locator('#badge-form [name=width]').fill('360');
    await admin.locator('#badge-form').evaluate(form => form.requestSubmit());
    await publicSVG();
    let invalidExports=0;
    const countInvalidExport=request => {if(new URL(request.url()).pathname==='/badge.svg')invalidExports++;};
    admin.on('request',countInvalidExport);
    await admin.locator('#badge-form [name=title]').fill('Donate\u200B');
    await admin.locator('#badge-form').evaluate(form => form.requestSubmit());
    await visible(admin,'#badge-status');
    assert.equal(await admin.locator('#badge-preview').isHidden(),true);
    for(const format of ['readme','html','url'])assert.equal(await admin.locator(`[data-badge-copy=${format}]`).isDisabled(),true);
    assert.equal(await admin.locator('#badge-download').getAttribute('href'),null);
    await admin.waitForTimeout(200);
    assert.equal(invalidExports,0,'Invisible format characters must not produce a badge export request');
    admin.off('request',countInvalidExport);
    await admin.locator('#badge-form [name=title]').fill('Fresh download');
    await admin.locator('#badge-form').evaluate(form => form.requestSubmit());
    await publicSVG();
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
    for(const view of ['ledger','site','payments','catalog','notifications','api','badges']) {
      await admin.locator(`[data-view=${view}]`).click();
      await noOverflow(admin);
      if(view==='badges')await admin.screenshot({path:resolve(artifacts,'admin-badge-mobile.png'),fullPage:true});
    }
    await admin.locator('[data-view=ledger]').click();
    await admin.screenshot({path:resolve(artifacts,'admin-mobile.png'),fullPage:true});
    await admin.setViewportSize({width:320,height:740});
    for(const view of ['ledger','site','payments','catalog','notifications','api','badges']) {
      await admin.locator(`[data-view=${view}]`).click();
      await noOverflow(admin);
      if(view==='badges')await admin.screenshot({path:resolve(artifacts,'admin-badge-320.png'),fullPage:true});
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
    activeAdminDevice=newAuthenticator;
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
  let donorID, accountDonationID, guestAfterLogoutID, administratorDonorID;

  await step('Optional donor account: modal focus and Escape, genuine Passkey signup, independent administrator permissions', async () => {
    assert.equal((await donorState(adminContext)).authenticated,false,'An admin cookie alone must not sign a donor in');
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

  await step('Donor Passkeys cannot authenticate administrators; administrator keys can authenticate only a separate public session', async () => {
    assert.deepEqual(await crossRoleAssertion(donorPage,'/api/auth'),{stage:'finish',status:401});
    assert.equal((await donorState(donorContext)).user.id,donorID);
    assert.equal((await api(donorContext,'/api/auth/status')).data.authenticated,false);
    const adminCookie=(await adminContext.cookies(base)).find(cookie => cookie.name==='donate_session').value;
    assert.deepEqual(await crossRoleAssertion(admin,'/api/donor'),{stage:'finish',status:200});
    assert.equal((await api(adminContext,'/api/auth/status')).data.authenticated,true);
    const publicSession=await donorState(adminContext);
    assert.equal(publicSession.authenticated,true);
    administratorDonorID=publicSession.user.id;
    assert.notEqual(administratorDonorID,donorID);
    assert.equal(publicSession.donor_id,administratorDonorID);
    assert.equal(publicSession.passkey_count,1);
    assert.equal((await adminContext.cookies(base)).find(cookie => cookie.name==='donate_session').value,adminCookie,'Public sign-in must preserve the existing administrator cookie');
    const logout=await api(adminContext,'/api/donor/logout',{method:'POST',headers:{Origin:base,'X-CSRF-Token':publicSession.csrf_token},data:{}});
    assert.equal(logout.status,200);
    assert.equal((await donorState(adminContext)).authenticated,false);
    assert.equal((await api(adminContext,'/api/auth/status')).data.authenticated,true);
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
    await donorPage.locator('#thanks-back').click();
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

  const verifySeparateCookies=async () => {
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
  };

  const administratorPublicContext=await browser.newContext({locale:'en-US',viewport:{width:1440,height:1050}});
  const administratorPublicPage=await administratorPublicContext.newPage();
  watch(administratorPublicPage);
  let administratorPublicDevice=await addAuthenticator(administratorPublicPage);
  let administratorDonationID;
  await step('An administrator Passkey signs into the homepage without an admin cookie and retains one ordinary donor identity',async () => {
    // Export/import only our disposable virtual fixture key. No browser profile
    // or real security key is read; this models using the same key on the homepage.
    const fixtureKeys=(await activeAdminDevice.cdp.send('WebAuthn.getCredentials',{authenticatorId:activeAdminDevice.authenticatorId})).credentials;
    assert.equal(fixtureKeys.length,1);
    await activeAdminDevice.cdp.send('WebAuthn.removeVirtualAuthenticator',{authenticatorId:activeAdminDevice.authenticatorId});
    await administratorPublicDevice.cdp.send('WebAuthn.addCredential',{authenticatorId:administratorPublicDevice.authenticatorId,credential:fixtureKeys[0]});
    assert.deepEqual(await administratorPublicContext.cookies(base),[]);
    await administratorPublicPage.goto(base);
    await visible(administratorPublicPage,'input[name=method_id]');
    await openDonor(administratorPublicPage);
    await administratorPublicPage.locator('#donor-login').click();
    await visible(administratorPublicPage,'#donor-logout');
    const session=await donorState(administratorPublicContext);
    assert.equal(session.authenticated,true);
    assert.equal(session.user.id,administratorDonorID);
    assert.equal(session.passkey_count,1);
    assert.equal((await api(administratorPublicContext,'/api/auth/status')).data.authenticated,false);
    assert.equal((await api(administratorPublicContext,'/api/admin/settings')).status,401);
    assert.equal((await api(administratorPublicContext,'/api/private/stats')).status,401);
    assert.ok(!(await administratorPublicContext.cookies(base)).some(cookie => cookie.name==='donate_session'),'Public sign-in must issue no administrator cookie');
    assert.equal((await api(administratorPublicContext,'/api/donor/donations')).data.total,0);
    await administratorPublicPage.locator('#donor-logout').click();
    await visible(administratorPublicPage,'#donor-login');
    assert.equal((await donorState(administratorPublicContext)).authenticated,false);
    await administratorPublicPage.locator('#donor-login').click();
    await visible(administratorPublicPage,'#donor-logout');
    assert.equal((await donorState(administratorPublicContext)).user.id,administratorDonorID,'Reusing the administrator key must not create another public identity');
    await noOverflow(administratorPublicPage);
    await administratorPublicPage.screenshot({path:resolve(artifacts,'administrator-public-account-desktop.png'),fullPage:true});
    await closeDonor(administratorPublicPage);
  });
  await step('The administrator public identity owns only its donation and history; its ordinary donor session cannot manage records',async () => {
    const created=await createCustomDonation(administratorPublicPage,7,{name:'Administrator public fixture',email:'administrator-public-private@example.com'});
    administratorDonationID=created.donation.id;
    assert.ok(!Object.hasOwn(created.request.postDataJSON(),'donor_user_id'));
    assert.equal(created.request.headers()['x-csrf-token'],(await donorState(administratorPublicContext)).csrf_token);
    assert.equal((await api(administratorPublicContext,`/api/admin/donations/${administratorDonationID}/confirm`,{method:'POST',headers:{Origin:base,'X-CSRF-Token':(await donorState(administratorPublicContext)).csrf_token},data:{reference:'unauthorized-fixture'}})).status,401);
    const ledger=(await api(adminContext,'/api/admin/donations')).data.donations;
    assert.equal(ledger.find(record => record.id===administratorDonationID).donor_user_id,administratorDonorID);
    assert.equal(ledger.find(record => record.id===accountDonationID).donor_user_id,donorID);
    assert.equal(ledger.find(record => record.id===guestAfterLogoutID).donor_user_id || '','');
    let history=(await api(administratorPublicContext,'/api/donor/donations')).data;
    assert.equal(history.total,1);
    assert.equal(history.donations[0].id,administratorDonationID);
    assert.equal(history.donations[0].status,'pending');
    assert.equal((await api(donorContext,'/api/donor/donations')).data.total,1,'The regular donor must not receive the administrator donation');
    const adminState=(await api(adminContext,'/api/auth/status')).data;
    const confirmed=await api(adminContext,`/api/admin/donations/${administratorDonationID}/confirm`,{method:'POST',headers:{Origin:base,'X-CSRF-Token':adminState.csrf_token},data:{reference:'administrator-public-confirmed-fixture',paid_at:new Date().toISOString()}});
    assert.equal(confirmed.status,200);
    await confirmedReceipt(administratorPublicPage);
    await openDonor(administratorPublicPage);
    await textIncludes(administratorPublicPage,'#donor-history-list','Confirmed');
    history=(await api(administratorPublicContext,'/api/donor/donations')).data;
    assert.equal(history.total,1);
    assert.equal(history.donations[0].id,administratorDonationID);
    assert.equal(history.donations[0].status,'confirmed');
    assert.ok(!JSON.stringify((await api(administratorPublicContext,'/api/site')).data).includes('administrator-public-private@example.com'));
    for(const width of [1440,390,320]) {
      await administratorPublicPage.setViewportSize({width,height:width===1440 ? 1050 : 844});
      await noOverflow(administratorPublicPage);
      await administratorPublicPage.screenshot({path:resolve(artifacts,`administrator-public-history-${width}.png`),fullPage:true});
    }
    await closeDonor(administratorPublicPage);
  });
  await step('A public backup key for the administrator-linked donor preserves the identity and history without gaining admin rights',async () => {
    await openDonor(administratorPublicPage);
    await administratorPublicDevice.cdp.send('WebAuthn.removeVirtualAuthenticator',{authenticatorId:administratorPublicDevice.authenticatorId});
    administratorPublicDevice=await addAuthenticator(administratorPublicPage);
    await administratorPublicPage.locator('#donor-add-passkey').click();
    await administratorPublicPage.waitForFunction(() => !document.querySelector('#donor-add-passkey').disabled);
    const backedUp=await donorState(administratorPublicContext);
    assert.equal(backedUp.user.id,administratorDonorID);
    assert.equal(backedUp.passkey_count,2);
    await administratorPublicPage.locator('#donor-logout').click();
    await visible(administratorPublicPage,'#donor-login');
    await administratorPublicPage.locator('#donor-login').click();
    await visible(administratorPublicPage,'#donor-logout');
    assert.equal((await donorState(administratorPublicContext)).user.id,administratorDonorID);
    assert.equal((await api(administratorPublicContext,'/api/donor/donations')).data.donations[0].id,administratorDonationID);
    assert.deepEqual(await crossRoleAssertion(administratorPublicPage,'/api/auth'),{stage:'finish',status:401},'A native donor backup key must not inherit administrator-key authority');
    assert.equal((await api(administratorPublicContext,'/api/auth/status')).data.authenticated,false);
    assert.equal((await api(administratorPublicContext,'/api/admin/settings')).status,401);
    assert.equal((await donorState(administratorPublicContext)).user.id,administratorDonorID);
    await closeDonor(administratorPublicPage);
    await administratorPublicContext.close();
  });
  await announcementScenario(admin,adminContext);
  await projectLifecycleScenarios(admin,adminContext,donorContext,methodID);
  // This check revokes the copied administrator session on the server, so run
  // it after the administrator confirms the new public donation.
  await verifySeparateCookies();

  }
  await donorLifecycleScenarios();
  if(!focus)await checkoutVisualScenarios();

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
