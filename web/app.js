const $ = (selector, scope = document) => scope.querySelector(selector);
const $$ = (selector, scope = document) => [...scope.querySelectorAll(selector)];
const supportedLocales = ['zh-CN', 'zh-TW', 'en'];
const ui = {
  'zh-CN': {
    skip:'跳到捐赠', openSource:'开源，靠你我继续。', language:'语言', currency:'币种', donateTitle:'给下一行代码\n一点支持。', amount:'捐赠金额', paymentMethod:'选择支付方式', loading:'正在加载…', leaveNote:'留个名字，或一句话', nameOptional:'称呼（选填）', emailOptional:'邮箱（选填）', messageOptional:'留言（选填）', namePlaceholder:'你想被怎样称呼', messagePlaceholder:'给项目留句话吧', publicConsent:'愿意公开称呼与留言，邮箱始终保密。', donate:'送一点燃料', donating:'正在准备支付…', agreement:'继续捐赠即表示你已阅读', and:'与', terms:'用户协议', privacy:'隐私政策', paymentNote:'金额由你决定。每一份支持都算数。', sceneHint:'光标 / 方向键，让字符流动。', pause:'暂停动态', resume:'继续动态', recentTitle:'微小的支持，真实的力量。', noDonations:'这里会记录愿意公开的支持。下一份，可能来自你。', contact:'联系', statsApi:'统计 API', checkPayment:'查看支付状态', checking:'正在查询…', backDonate:'返回捐赠', openCheckout:'前往支付页面', noMethods:'暂时还没有可用的支付方式，请稍后再来。', chooseMethod:'请先选择一种支付方式。', invalidAmount:'请输入大于 0 的金额，最多两位小数。', invalidJPY:'日元金额需要是大于 0 的整数。', amountTooHigh:'金额过大，请输入较小的金额。', requestFailed:'请求失败，请稍后重试。', loadFailed:'暂时无法加载捐赠配置。请刷新页面重试。', emailInvalid:'邮箱格式似乎有误，请检查后重试。', pendingTitle:'燃料正在路上。', pendingCopy:'请完成支付。只有支付平台确认，或管理员核实后，这笔支持才会记入。', customPendingCopy:'请通过下方二维码或链接完成捐赠。管理员核实后，这笔支持会记入。', paidTitle:'收到这份支持了。', paidCopy:'谢谢你，让开源继续向前。这笔捐赠已经确认。', failedTitle:'这笔支付未完成。', failedCopy:'支付平台未确认收款，你可以返回后重新尝试。', refundedTitle:'这笔捐赠已退款。', refundedCopy:'支付平台已确认退款。', cancelledTitle:'支付已取消。', cancelledCopy:'你可以返回捐赠，重新选择金额与支付方式。', statusWaiting:'等待支付确认，页面会自动更新。', customWaiting:'等待管理员确认。你可以保留这个页面链接，稍后查看。', statusUnavailable:'暂时无法获取支付状态。稍后重试，或保留此页面链接。', statusConfirmed:'已确认', anonymous:'匿名支持者', donations:'份已确认支持', close:'关闭', qrAlt:'捐赠支付二维码', pulse:'谢谢这份好奇。', fallbackTagline:'留一点燃料', fallbackDescription:'一杯咖啡，一点时间，下一行代码。\n让你喜欢的开源项目继续生长。', fallbackFooter:'由开源驱动。由你继续。', defaultTitle:'给开源，\n留一点燃料。', unsupportedWaffo:'此币种暂不支持 Waffo，请切换币种或支付方式。', autoCheckPaused:'自动查询已暂停；点击查看支付状态即可继续。', waffoRange:'Waffo 的美元支付金额需要在 $1 到 $10,000 之间。', paypalTWD:'PayPal 的台币支付金额需要是整数。'
  },
  'zh-TW': {
    skip:'跳到捐贈', openSource:'開源，靠你我繼續。', language:'語言', currency:'幣別', donateTitle:'給下一行程式碼\n一點支持。', amount:'捐贈金額', paymentMethod:'選擇支付方式', loading:'正在載入…', leaveNote:'留個名字，或一句話', nameOptional:'稱呼（選填）', emailOptional:'信箱（選填）', messageOptional:'留言（選填）', namePlaceholder:'你想被怎樣稱呼', messagePlaceholder:'給專案留句話吧', publicConsent:'願意公開稱呼與留言，信箱始終保密。', donate:'送一點燃料', donating:'正在準備支付…', agreement:'繼續捐贈即表示你已閱讀', and:'與', terms:'使用者協議', privacy:'隱私權政策', paymentNote:'金額由你決定。每一份支持都算數。', sceneHint:'移動游標，讓字元流動。', pause:'暫停動態', resume:'繼續動態', recentTitle:'微小的支持，真實的力量。', noDonations:'這裡會記錄願意公開的支持。下一份，可能來自你。', contact:'聯絡', statsApi:'統計 API', checkPayment:'查看支付狀態', checking:'正在查詢…', backDonate:'返回捐贈', openCheckout:'前往支付頁面', noMethods:'暫時還沒有可用的支付方式，請稍後再來。', chooseMethod:'請先選擇一種支付方式。', invalidAmount:'請輸入大於 0 的金額，最多兩位小數。', invalidJPY:'日圓金額需要是大於 0 的整數。', amountTooHigh:'金額過大，請輸入較小的金額。', requestFailed:'請求失敗，請稍後重試。', loadFailed:'暫時無法載入捐贈設定。請重新整理頁面再試。', emailInvalid:'信箱格式似乎有誤，請檢查後重試。', pendingTitle:'燃料正在路上。', pendingCopy:'請完成支付。只有支付平台確認，或管理員核實後，這筆支持才會記入。', customPendingCopy:'請透過下方 QR code 或連結完成捐贈。管理員核實後，這筆支持會記入。', paidTitle:'收到這份支持了。', paidCopy:'謝謝你，讓開源繼續向前。這筆捐贈已經確認。', failedTitle:'這筆支付未完成。', failedCopy:'支付平台未確認收款，你可以返回後重新嘗試。', refundedTitle:'這筆捐贈已退款。', refundedCopy:'支付平台已確認退款。', cancelledTitle:'支付已取消。', cancelledCopy:'你可以返回捐贈，重新選擇金額與支付方式。', statusWaiting:'等待支付確認，頁面會自動更新。', customWaiting:'等待管理員確認。你可以保留這個頁面連結，稍後查看。', statusUnavailable:'暫時無法取得支付狀態。稍後重試，或保留此頁面連結。', statusConfirmed:'已確認', anonymous:'匿名支持者', donations:'份已確認支持', close:'關閉', qrAlt:'捐贈支付 QR code', pulse:'謝謝這份好奇。', fallbackTagline:'留一點燃料', fallbackDescription:'一杯咖啡，一點時間，下一行程式碼。\n讓你喜歡的開源專案繼續生長。', fallbackFooter:'由開源驅動。由你繼續。', defaultTitle:'給開源，\n留一點燃料。', unsupportedWaffo:'此幣別暫不支援 Waffo，請切換幣別或支付方式。', autoCheckPaused:'自動查詢已暫停；點擊查看支付狀態即可繼續。'
  },
  en: {
    skip:'Skip to donation', openSource:'Open source runs on people.', language:'Language', currency:'Currency', donateTitle:'A little fuel.\nMore good code.', amount:'Your contribution', paymentMethod:'Choose how to pay', loading:'Loading…', leaveNote:'Leave a name, or a little note', nameOptional:'Name (optional)', emailOptional:'Email (optional)', messageOptional:'Note (optional)', namePlaceholder:'What should we call you?', messagePlaceholder:'A few words for the project', publicConsent:'Show my name and note publicly. My email stays private.', donate:'Send a little fuel', donating:'Preparing your payment…', agreement:'By continuing, you acknowledge the', and:'and', terms:'Terms', privacy:'Privacy policy', paymentNote:'Your amount. Your choice. Every bit helps.', sceneHint:'Move your cursor. Let the characters drift.', pause:'Pause motion', resume:'Resume motion', recentTitle:'Small gestures. Real momentum.', noDonations:'Supporters who choose to share will appear here. The next one could be you.', contact:'Contact', statsApi:'Stats API', checkPayment:'Check payment status', checking:'Checking…', backDonate:'Back to donation', openCheckout:'Open payment page', noMethods:'No payment methods are available yet. Please come back soon.', chooseMethod:'Choose a payment method first.', invalidAmount:'Enter an amount greater than 0, with up to two decimal places.', invalidJPY:'Enter a whole yen amount greater than 0.', amountTooHigh:'That amount is too large. Please enter a smaller amount.', requestFailed:'The request failed. Please try again shortly.', loadFailed:'Donation settings could not be loaded. Refresh this page to try again.', emailInvalid:'That email address looks incomplete. Please check it.', pendingTitle:'Fuel is on its way.', pendingCopy:'Complete your payment. Your support is recorded only after the payment provider confirms it or an administrator verifies it.', customPendingCopy:'Use the QR code or link below to donate. Your support will be recorded after an administrator verifies it.', paidTitle:'Your support arrived.', paidCopy:'Thank you for keeping open source moving. This donation is confirmed.', failedTitle:'This payment did not complete.', failedCopy:'The payment provider has not confirmed receipt. You can go back and try again.', refundedTitle:'This donation was refunded.', refundedCopy:'The payment provider has confirmed the refund.', cancelledTitle:'Payment cancelled.', cancelledCopy:'Go back to choose an amount and payment method again.', statusWaiting:'Waiting for payment confirmation. This page updates automatically.', customWaiting:'Waiting for an administrator to confirm. Keep this page link to check later.', statusUnavailable:'Payment status is temporarily unavailable. Try again shortly, or keep this page link.', statusConfirmed:'Confirmed', anonymous:'Anonymous supporter', donations:'confirmed contributions', close:'Close', qrAlt:'Donation payment QR code', pulse:'Thanks for your curiosity.', fallbackTagline:'Leave a little fuel', fallbackDescription:'A coffee. A little time. The next line of code.\nHelp the open source you love keep growing.', fallbackFooter:'Built with open source. Kept going by you.', defaultTitle:'Good code.\nA little fuel.', unsupportedWaffo:'Waffo does not support this currency. Choose another currency or payment method.', autoCheckPaused:'Automatic checks paused. Check payment status to continue.'
  }
};
const defaultPolicies = {
  'zh-CN': {terms:'本站为开源项目接受自愿捐赠。请确认金额、币种和支付方式后再继续。捐赠不构成商品购买、服务承诺或投资，不保证任何回报。\n\n支付由对应平台处理，适用其规则。退款、支付异常及其他疑问，请通过本站公布的联系方式联系管理员。使用本站即表示你同意合理、合法地使用本服务。', privacy:'我们仅收集完成、记录及通知捐赠所需的信息，包括金额、支付方式、时间，以及你自愿填写的称呼、邮箱和留言。支付账户及银行卡信息由支付平台处理。\n\n只有获得你的同意，称呼和留言才会公开；邮箱不公开。信息可能通过管理员配置的通知服务发送给管理员。我们不会出售你的个人信息。你可联系管理员请求更正或删除，依法须保留的记录除外。'},
  'zh-TW': {terms:'本站為開源專案接受自願捐贈。請確認金額、幣別和支付方式後再繼續。捐贈不構成商品購買、服務承諾或投資，不保證任何回報。\n\n支付由對應平台處理，適用其規則。退款、支付異常及其他疑問，請透過本站公布的聯絡方式聯絡管理員。使用本站即表示你同意合理、合法地使用本服務。', privacy:'我們僅收集完成、記錄及通知捐贈所需的資訊，包括金額、支付方式、時間，以及你自願填寫的稱呼、信箱和留言。支付帳戶及信用卡資訊由支付平台處理。\n\n只有獲得你的同意，稱呼和留言才會公開；信箱不公開。資訊可能透過管理員設定的通知服務發送給管理員。我們不會出售你的個人資訊。你可聯絡管理員請求更正或刪除，依法須保留的記錄除外。'},
  en: {terms:'This site accepts voluntary support for an open-source project. Check your amount, currency, and payment method before continuing. A donation is not a purchase, a service commitment, or an investment, and does not promise a return.\n\nPayments are processed by the selected payment provider under its rules. Contact the administrator using the details on this site for refund requests, payment issues, or other questions. Use this service reasonably and lawfully.', privacy:'We collect only the information needed to process, record, and notify the administrator about a donation: amount, method, time, and any name, email, or note you choose to provide. Payment providers handle card and payment account details.\n\nYour name and note appear publicly only with your consent. Your email is never public. Donation details may be delivered to the administrator through configured notification services. We do not sell your personal information. Contact the administrator to request corrections or deletion, except for records that must legally be retained.'}
};
Object.assign(ui['zh-TW'],{waffoRange:'Waffo 的美元支付金額需要在 $1 到 $10,000 之間。',paypalTWD:'PayPal 的台幣支付金額需要是整數。'});
Object.assign(ui.en,{waffoRange:'Waffo accepts USD payments from $1 to $10,000.',paypalTWD:'PayPal requires a whole-dollar amount for TWD.'});
Object.assign(ui['zh-CN'],{waffoAmountRange:'Waffo 支持的金额范围是 {min} 至 {max}。',sentenceEnd:'。'});
Object.assign(ui['zh-TW'],{waffoAmountRange:'Waffo 支援的金額範圍是 {min} 至 {max}。',sentenceEnd:'。'});
Object.assign(ui.en,{waffoAmountRange:'Waffo accepts amounts from {min} to {max}.',sentenceEnd:'.'});
const waffoRangesMinor={USD:[100,1000000],EUR:[100,940000],GBP:[100,815000],HKD:[800,7760000],JPY:[100,1600000],CNY:[100,100000]};
let site = null;
let locale = 'zh-CN';
let currency = 'CNY';
let submissionBusy = false;
let activeCheckout = null;
let pollTimer;
let pollCount = 0;
let statsSequence = 0;
let donationFingerprint = '', donationIdempotency = '';
const t = key => ui[locale][key] ?? ui.en[key] ?? key;
const exponent = value => value === 'JPY' ? 0 : 2;
const storeValue = (key, value) => { try { localStorage.setItem(key, value); } catch {} };
const readStored = key => { try { return localStorage.getItem(key); } catch { return null; } };

async function request(path, options = {}) {
  const response = await fetch(path, { credentials:'same-origin', ...options, headers:{Accept:'application/json', ...(options.body ? {'Content-Type':'application/json'} : {}), ...options.headers} });
  let result;
  try { result = await response.json(); } catch { throw new Error(t('requestFailed')); }
  if (!response.ok) throw new Error(result.error || t('requestFailed'));
  return result;
}
function money(amountMinor, unit) {
  try { return new Intl.NumberFormat(locale, {style:'currency',currency:unit, maximumFractionDigits:exponent(unit)}).format(amountMinor / 10 ** exponent(unit)); }
  catch { return `${unit} ${amountMinor / 10 ** exponent(unit)}`; }
}
function currencySymbol(unit) {
  try { return new Intl.NumberFormat(locale,{style:'currency',currency:unit,currencyDisplay:'narrowSymbol'}).formatToParts(0).find(part=>part.type==='currency')?.value || unit; }
  catch { return unit; }
}
function selectLocale() {
  const available = (site?.languages || supportedLocales).filter(item => supportedLocales.includes(item));
  const stored = readStored('token-language');
  if (available.includes(stored)) return stored;
  for (const language of navigator.languages || [navigator.language]) {
    if (available.includes(language)) return language;
    if (/^zh-(TW|HK|MO|Hant)/i.test(language) && available.includes('zh-TW')) return 'zh-TW';
    if (/^zh/i.test(language) && available.includes('zh-CN')) return 'zh-CN';
    if (/^en/i.test(language) && available.includes('en')) return 'en';
  }
  return available.includes(site?.default_language) ? site.default_language : available[0] || 'en';
}
function setMultiline(element, text) { element.textContent = text; element.style.whiteSpace = 'pre-line'; }
function localText(key, fallback) {
  return site?.translations?.[locale]?.[key] || site?.[key] || fallback;
}
function applyLanguage() {
  document.documentElement.lang = locale;
  $$('[data-i18n]').forEach(element => setMultiline(element,t(element.dataset.i18n)));
  $$('[data-placeholder]').forEach(element => element.placeholder = t(element.dataset.placeholder));
  $('#language').value = locale;
  $('#language').setAttribute('aria-label',t('language'));
  $('#presets').setAttribute('aria-label',t('amount'));
  $('#close-policy').setAttribute('aria-label',t('close'));
  if($('#checkout-qr')) $('#checkout-qr').alt = t('qrAlt');
  $('#scene').setAttribute('aria-label',locale==='en' ? 'Interactive ASCII orbital sculpture' : locale==='zh-TW' ? '可互動的 ASCII 軌道雕塑' : '可互动的 ASCII 轨道雕塑');
  $('.token-cloud').setAttribute('aria-label',locale==='en' ? 'Interactive token cloud' : locale==='zh-TW' ? '互動字元雲' : '互动字符云');
  $('#site-name').textContent = site?.name || '留一点燃料';
  $('#logo').setAttribute('aria-label',site?.name || '留一点燃料');
  const tagline = localText('tagline', t('fallbackTagline'));
  const defaultTaglines = ['留一点燃料','留一點燃料','Leave a little fuel','留一点燃料。','留一點燃料。','Leave a little fuel.','Keep the good things running.'];
  $('#invitation-title').replaceChildren();
  const title = defaultTaglines.includes(tagline) || !site ? t('defaultTitle') : tagline;
  const pieces = title.split('\n');
  pieces.forEach((part,index) => {
    if(index) $('#invitation-title').append(document.createElement('br'));
    if(index===pieces.length-1 && /[。.]/.test(part.slice(-1))) {
      $('#invitation-title').append(document.createTextNode(part.slice(0,-1)));
      const accent=document.createElement('span');accent.className='lime';accent.textContent=part.slice(-1);$('#invitation-title').append(accent);
    } else $('#invitation-title').append(document.createTextNode(part));
  });
  setMultiline($('#site-description'),localText('description',t('fallbackDescription')));
  $('#site-tagline').textContent=tagline;
  $('#site-footer-text').textContent=localText('footer',t('fallbackFooter'));
  document.title=site?.name || '留一点燃料';
  document.querySelector('meta[name=description]').content=localText('description',t('fallbackDescription')).replaceAll('\n',' ');
  updateMotionControl();
  if(submissionBusy) $('#donate-button span').textContent=t('donating');
  if(activeCheckout) renderCheckout();
  if($('#policy-dialog').open) showPolicy($('#policy-dialog').dataset.policy);
}
function setCurrency(value, keepAmount = true) {
  const available = site?.currencies?.length ? site.currencies : ['USD','CNY','TWD','EUR','GBP','HKD','JPY'];
  currency = available.includes(value) ? value : available.includes(site?.currency) ? site.currency : available[0];
  $('#currency').value=currency;
  $('#currency-symbol').textContent=currencySymbol(currency);
  if(!keepAmount || !$('#amount').value) $('#amount').value=String(site?.presets?.[1] || site?.presets?.[0] || 15);
  $('#amount-error').hidden=true;
  renderPresets();
  renderMethods();
  fetchStats();
}
function renderPresets() {
  const container=$('#presets');container.replaceChildren();
  const presets=(site?.presets?.length ? site.presets : [5,15,50,100]).slice(0,6);
  presets.forEach(amount => {
    const button=document.createElement('button');button.type='button';button.className='preset';button.textContent=String(amount);button.dataset.amount=String(amount);button.setAttribute('aria-label',`${t('amount')} ${currencySymbol(currency)}${amount}`);
    button.addEventListener('click',()=>{$('#amount').value=String(amount);$('#amount-error').hidden=true;markPreset();sceneEnergy=Math.min(2.2,.7+Math.log10(Number(amount)+1)/3);pulseScene();});container.append(button);
  });
  markPreset();
}
function markPreset() { $$('.preset').forEach(button=>{const selected=Number(button.dataset.amount)===Number($('#amount').value);button.classList.toggle('active',selected);button.setAttribute('aria-pressed',String(selected));}); }
function renderMethods() {
  const old=$('input[name=method_id]:checked')?.value;
  const container=$('#payment-methods');container.replaceChildren();
  const methods=site?.methods || [];
  if(!methods.length) {const note=document.createElement('p');note.className='empty-note';note.textContent=t('noMethods');container.append(note);$('#donate-button').disabled=true;return;}
  methods.forEach((method,index) => {
    const label=document.createElement('label');label.className='method';
    const radio=document.createElement('input');radio.type='radio';radio.name='method_id';radio.value=method.id;radio.required=true;
    radio.checked=old ? method.id===old : index===0;
    const unavailable=method.type==='waffo' && currency==='TWD';radio.disabled=unavailable;
    const body=document.createElement('span');body.className='method-body';const name=document.createElement('span');name.className='method-title';name.textContent=method.name;body.append(name);
    if(method.description || unavailable) {const description=document.createElement('span');description.className='method-description';description.textContent=unavailable ? t('unsupportedWaffo') : method.description;body.append(description);}
    const type=document.createElement('span');type.className='method-type';type.textContent=method.type==='custom' ? (method.qr_url ? 'QR' : 'LINK') : method.type==='waffo' ? 'PANCAKE' : method.type;
    if(unavailable) {label.style.opacity='.55';radio.checked=false;}
    label.append(radio,body,type);container.append(label);
  });
  if(!$('input[name=method_id]:checked')) {const first=$('input[name=method_id]:not(:disabled)');if(first)first.checked=true;}
  $('#donate-button').disabled=submissionBusy || !$('input[name=method_id]:not(:disabled)');
}
function parseAmount() {
  const value=$('#amount').value.trim();
  const precision=exponent(currency);
  const pattern=precision ? /^\d+(?:\.\d{1,2})?$/ : /^\d+$/;
  if(!pattern.test(value)) throw new Error(t(precision?'invalidAmount':'invalidJPY'));
  const [whole,fraction='']=value.split('.');
  const result=Number(whole)*10**precision + Number(fraction.padEnd(precision,'0'));
  if(!Number.isSafeInteger(result) || result>1000000*10**precision)throw new Error(t('amountTooHigh'));
  if(result<1)throw new Error(t(precision?'invalidAmount':'invalidJPY'));
  return result;
}
async function fetchStats() {
  if(!site) return;
  const seq=++statsSequence;
  try {
    const result=await request(`/api/stats?currency=${encodeURIComponent(currency)}`);
    if(seq!==statsSequence)return;
    const count=result.count ?? result.total_count ?? result.total_donations ?? site?.stats?.count;
    let total=result.total_minor;
    if(total===undefined) {
      const entries=result.currencies || result.totals || [];
      if(Array.isArray(entries)) total=entries.find(entry=>entry.currency===currency)?.total_minor;
      else total=entries[currency]?.total_minor ?? entries[currency];
    }
    if(count>0 && typeof total==='number') $('#support-total').textContent=`${money(total,currency)} / ${count} ${t('donations')}`;
    else $('#support-total').textContent='';
  } catch { $('#support-total').textContent=''; }
}
function renderRecent() {
  const container=$('#recent-support');container.replaceChildren();
  const recent=site?.recent || [];
  if(!recent.length){const empty=document.createElement('p');empty.className='empty-support';empty.textContent=t('noDonations');container.append(empty);return;}
  recent.slice(0,8).forEach(donation=>{
    const article=document.createElement('article');article.className='support-entry';const header=document.createElement('header');const name=document.createElement('strong');name.textContent=donation.name || t('anonymous');const amount=document.createElement('span');amount.className='support-money';amount.textContent=money(donation.amount_minor,donation.currency);header.append(name,amount);article.append(header);
    if(donation.message){const note=document.createElement('p');note.textContent=donation.message;article.append(note);}
    if(donation.paid_at){const date=new Date(donation.paid_at);if(!Number.isNaN(date.getTime())) {const time=document.createElement('time');time.dateTime=date.toISOString();time.textContent=new Intl.DateTimeFormat(locale,{month:'short',day:'numeric'}).format(date);article.append(time);}}
    container.append(article);
  });
}
function showPolicy(kind) {
  if(!['terms','privacy'].includes(kind))return;
  const dialog=$('#policy-dialog');dialog.dataset.policy=kind;$('#policy-title').textContent=t(kind);$('#policy-content').textContent=localText(kind,defaultPolicies[locale][kind]);if(!dialog.open)dialog.showModal();
}
function safeCheckoutURL(value) {if(typeof value!=='string' || !value.trim())return null;try {const url=new URL(value,location.origin);return ['https:','http:'].includes(url.protocol) ? url.href : null;}catch{return null;}}
function showCheckout(checkout) {
  activeCheckout=checkout;pollCount=0;$('#donation-form').hidden=true;$('#checkout-status').hidden=false;$('#checkout-status').focus();
  const url=new URL(location.href);url.searchParams.set('donation',checkout.id);url.searchParams.set('status_token',checkout.status_token);history.replaceState(null,'',url);
  renderCheckout();schedulePoll(1200);
}
function renderCheckout() {
  if(!activeCheckout)return;
  const state=activeCheckout.status;
  const finalState=['paid','confirmed','completed','succeeded','refunded','failed','cancelled','canceled'].includes(state);
  const key=['paid','confirmed','completed','succeeded'].includes(state)?'paid':state==='refunded'?'refunded':state==='failed'?'failed':['cancelled','canceled'].includes(state)?'cancelled':'pending';
  $('#checkout-title').textContent=t(`${key}Title`);$('#checkout-copy').textContent=t(key==='pending' && activeCheckout.custom ? 'customPendingCopy' : `${key}Copy`);
  $('#status-mark').textContent=key==='paid'?'[ + ]':key==='pending'?'[ · ]':'[ / ]';
  $('#checkout-amount').textContent=activeCheckout.amount_minor ? money(activeCheckout.amount_minor,activeCheckout.currency) : '';
  const qr=safeCheckoutURL(activeCheckout.qr_url);const qrContainer=$('#checkout-qr-container');qrContainer.hidden=!qr || finalState;
  if(qr){let image=$('#checkout-qr');if(!image){image=document.createElement('img');image.id='checkout-qr';image.alt=t('qrAlt');image.src=qr;qrContainer.append(image);}else{image.src=qr;image.alt=t('qrAlt');}}
  const link=safeCheckoutURL(activeCheckout.checkout_url);$('#checkout-link').hidden=!link || finalState;if(link)$('#checkout-link').href=link;
  $('#checkout-instructions').hidden=!activeCheckout.instructions || finalState;$('#checkout-instructions').textContent=activeCheckout.instructions || '';
  $('#status-refresh').hidden=finalState;
  if(!activeCheckout.pollError) $('#status-detail').textContent=key==='pending' ? t(activeCheckout.custom?'customWaiting':'statusWaiting') : key==='paid' ? `${t('statusConfirmed')}${activeCheckout.paid_at ? ` · ${new Intl.DateTimeFormat(locale,{dateStyle:'medium',timeStyle:'short'}).format(new Date(activeCheckout.paid_at))}`:''}` : '';
  if(finalState)clearTimeout(pollTimer);
}
function schedulePoll(delay=6000) {clearTimeout(pollTimer);if(!activeCheckout || document.hidden)return;pollTimer=setTimeout(()=>pollStatus(false),delay);}
async function pollStatus(manual=false) {
  if(!activeCheckout)return;
  if(!manual && pollCount>=150){$('#status-detail').textContent=t('autoCheckPaused');return;}
  const button=$('#status-refresh');button.disabled=true;button.textContent=t('checking');const id=activeCheckout.id;
  try {
    const status=await request(`/api/donations/${encodeURIComponent(id)}?token=${encodeURIComponent(activeCheckout.status_token)}`);
    if(activeCheckout?.id!==id)return;
    Object.assign(activeCheckout,status,{pollError:false});renderCheckout();pollCount++;
    if(['pending','created','processing',''].includes(status.status || ''))schedulePoll(activeCheckout.custom?12000:6000);
    if(['paid','confirmed','completed','succeeded'].includes(status.status)) {fetchStats();try {site=await request('/api/site');renderRecent();}catch{}}
  }catch{
    if(activeCheckout?.id!==id)return;
    activeCheckout.pollError=true;$('#status-detail').textContent=t('statusUnavailable');pollCount++;schedulePoll(15000);
  }finally {button.disabled=false;button.textContent=t('checkPayment');}
}
$('#donation-form').addEventListener('submit',async event=>{
  event.preventDefault();if(submissionBusy || !site)return;
  $('#form-error').hidden=true;$('#amount-error').hidden=true;
  let amountMinor;
  try {amountMinor=parseAmount();}catch(error){$('#amount-error').textContent=error.message;$('#amount-error').hidden=false;$('#amount').focus();return;}
  const selected=$('input[name=method_id]:checked');
  if(!selected || selected.disabled){$('#form-error').textContent=t('chooseMethod');$('#form-error').hidden=false;return;}
  const method=site.methods.find(item=>item.id===selected.value);
  const waffoRange=method?.type==='waffo' && waffoRangesMinor[currency];
  if(waffoRange && (amountMinor<waffoRange[0] || amountMinor>waffoRange[1])) {$('#amount-error').textContent=t('waffoAmountRange').replace('{min}',money(waffoRange[0],currency)).replace('{max}',money(waffoRange[1],currency));$('#amount-error').hidden=false;$('#amount').focus();return;}
  if(method?.type==='paypal' && currency==='TWD' && amountMinor%100!==0) {$('#amount-error').textContent=t('paypalTWD');$('#amount-error').hidden=false;$('#amount').focus();return;}
  if(site.collect_email && !$('#donor-email').checkValidity()){$('#form-error').textContent=t('emailInvalid');$('#form-error').hidden=false;$('.donor-details').open=true;$('#donor-email').focus();return;}
  submissionBusy=true;$('#donate-button').disabled=true;$('#donate-button span').textContent=t('donating');
  try {
    const body=JSON.stringify({amount_minor:amountMinor,currency,method_id:selected.value,name:site.collect_name ? $('#donor-name').value.trim() : '',email:site.collect_email ? $('#donor-email').value.trim() : '',message:site.collect_message ? $('#donor-message').value.trim() : '',public:$('#donor-public').checked,accepted_terms:true});
    if(body!==donationFingerprint || !donationIdempotency){donationFingerprint=body;donationIdempotency=crypto.randomUUID ? crypto.randomUUID() : Array.from(crypto.getRandomValues(new Uint8Array(24)),byte=>byte.toString(16).padStart(2,'0')).join('');}
    const donation=await request('/api/donations',{method:'POST',body,headers:{'Idempotency-Key':donationIdempotency}});
    if(!donation.id || !donation.status_token)throw new Error(t('requestFailed'));
    showCheckout({...donation,amount_minor:amountMinor,currency,custom:method?.type==='custom'});
    const checkout=safeCheckoutURL(donation.checkout_url);
    if(checkout && method?.type!=='custom')location.assign(checkout);
  }catch(error){$('#form-error').textContent=error.message || t('requestFailed');$('#form-error').hidden=false;}
  finally{submissionBusy=false;$('#donate-button').disabled=!$('input[name=method_id]:not(:disabled)');$('#donate-button span').textContent=t('donate');}
});
$('#amount').addEventListener('input',()=>{$('#amount-error').hidden=true;markPreset();sceneEnergy=Math.min(2.2,.7+Math.log10(Number($('#amount').value)||1)/3);});
$('#currency').addEventListener('change',()=>setCurrency($('#currency').value));
$('#language').addEventListener('change',()=>{locale=$('#language').value;storeValue('token-language',locale);applyLanguage();setCurrency(site?.language_currencies?.[locale] || {'zh-CN':'CNY','zh-TW':'TWD',en:'USD'}[locale]);renderRecent();});
$$('[data-policy]').forEach(button=>button.addEventListener('click',()=>showPolicy(button.dataset.policy)));
$('#close-policy').addEventListener('click',()=>$('#policy-dialog').close());
$('#policy-dialog').addEventListener('click',event=>{if(event.target===$('#policy-dialog')){const box=event.target.getBoundingClientRect();if(event.clientX<box.left || event.clientX>box.right || event.clientY<box.top || event.clientY>box.bottom)event.target.close();}});
$('#status-refresh').addEventListener('click',()=>{pollCount=0;pollStatus(true);});
$('#new-donation').addEventListener('click',()=>{clearTimeout(pollTimer);activeCheckout=null;donationFingerprint='';donationIdempotency='';$('#checkout-status').hidden=true;$('#donation-form').hidden=false;const url=new URL(location.href);url.searchParams.delete('donation');url.searchParams.delete('status_token');history.replaceState(null,'',url);$('#amount').focus();});
let logoClicks=[];
$('#logo').addEventListener('click',()=>{const now=Date.now();logoClicks=logoClicks.filter(time=>now-time<3000);logoClicks.push(now);if(logoClicks.length>=5)location.assign('/admin');});

// The scene renders real characters. It never owns or blocks the payment flow.
const motionPreference=matchMedia('(prefers-reduced-motion: reduce)');
let motionPaused=motionPreference.matches;
let sceneEnergy=1;
let pointerX=0,pointerY=0,rotation=0,lastFrame=0,pulseUntil=0,sceneRAF;
const scene=$('#scene');
scene.tabIndex=0;
scene.addEventListener('keydown',event=>{if(event.target!==scene || !['ArrowLeft','ArrowRight','ArrowUp','ArrowDown'].includes(event.key))return;event.preventDefault();rotation+=event.key==='ArrowLeft'?-.35:event.key==='ArrowRight'?.35:0;pointerY=Math.max(-.5,Math.min(.5,pointerY+(event.key==='ArrowUp'?-.12:event.key==='ArrowDown'?.12:0)));drawScene(rotation);});
function runScene(){cancelAnimationFrame(sceneRAF);if(!motionPaused && !document.hidden)sceneRAF=requestAnimationFrame(animateScene);}
function updateMotionControl(){const button=$('#motion-toggle');button.setAttribute('aria-pressed',String(motionPaused));$('span',button).textContent=t(motionPaused?'resume':'pause');$('svg',button).innerHTML=motionPaused?'<path d="m5 3 7 5-7 5z"/>':'<path d="M5 3v10M11 3v10"/>';}
function pulseScene(){pulseUntil=performance.now()+700;}
$('#motion-toggle').addEventListener('click',()=>{motionPaused=!motionPaused;updateMotionControl();runScene();});
motionPreference.addEventListener('change',event=>{motionPaused=event.matches;updateMotionControl();runScene();});
scene.addEventListener('pointermove',event=>{if(motionPaused)return;const bounds=scene.getBoundingClientRect();pointerX=((event.clientX-bounds.left)/bounds.width-.5)*.55;pointerY=((event.clientY-bounds.top)/bounds.height-.5)*.35;});
scene.addEventListener('pointerleave',()=>{pointerX=0;pointerY=0;});
$$('.token').forEach(button=>button.addEventListener('click',()=>{button.classList.remove('pulse');void button.offsetWidth;button.classList.add('pulse');pulseScene();$('#token-response').textContent=`${button.textContent} — ${t('pulse')}`;if(motionPaused)drawScene(rotation);setTimeout(()=>button.classList.remove('pulse'),700);}));
function drawScene(angle){
  const width=96,height=34;
  const cells=Array(width*height).fill(' '),depth=new Float32Array(width*height).fill(-999);
  const shades=' .,:;irsXA253hMHGS#9B&@';
  const spin=angle*.23+pointerX,tilt=.78+pointerY;
  const cosA=Math.cos(spin),sinA=Math.sin(spin),cosB=Math.cos(tilt),sinB=Math.sin(tilt);
  function point(x,y,z,light,ring=false){
    const a=x*cosA-z*sinA,c=x*sinA+z*cosA;
    const b=y*cosB-c*sinB,d=y*sinB+c*cosB;
    const scale=4.8/(4.8+d);
    const col=Math.round(width/2+a*15*scale),row=Math.round(height/2+b*6.5*scale);
    if(col<0||col>=width||row<0||row>=height)return;
    const index=col+row*width;
    if(-d>depth[index]){depth[index]=-d;cells[index]=ring ? (light>.55?'+':light>.3?':':'.') : shades[Math.max(1,Math.min(shades.length-1,Math.round(light*(shades.length-1))))];}
  }
  const pulse=performance.now()<pulseUntil ? 1.04 : 1;
  for(let u=0;u<Math.PI*2;u+=.047){
    for(let v=0;v<Math.PI*2;v+=.08){
      const cv=Math.cos(v),sv=Math.sin(v),cu=Math.cos(u),su=Math.sin(u);
      const radius=1.14+.52*cv;
      const x=radius*cu*pulse,y=.52*sv*pulse,z=radius*su*pulse;
      const light=Math.max(.08,.37+.4*(cv*cu*.3+sv*.85-cv*su*.6));
      point(x,y,z,light);
    }
  }
  for(let v=0;v<Math.PI*2;v+=.014){
    const radius=2.35;
    point(Math.cos(v)*radius,Math.sin(v)*radius*.72,Math.sin(v)*radius*.64,.4+.35*Math.cos(v),true);
  }
  $('#ascii').textContent=Array.from({length:height},(_,row)=>cells.slice(row*width,(row+1)*width).join('')).join('\n');
}
function animateScene(now){
  if(motionPaused || document.hidden){lastFrame=0;return;}
  if(now-lastFrame>65){rotation+=.035*sceneEnergy;drawScene(rotation);lastFrame=now;}
  sceneRAF=requestAnimationFrame(animateScene);
}
document.addEventListener('visibilitychange',()=>{if(document.hidden){clearTimeout(pollTimer);cancelAnimationFrame(sceneRAF);lastFrame=0;}else{runScene();if(activeCheckout)schedulePoll(800);}});
drawScene(.5);runScene();

async function start(){
  try {
    site=await request('/api/site');locale=selectLocale();
    const languages=(site.languages || supportedLocales).filter(language=>supportedLocales.includes(language));
    $$('#language option').forEach(option=>{option.hidden=!languages.includes(option.value);option.disabled=option.hidden;});
    const currencyOptions=site.currencies?.length ? site.currencies : ['USD','CNY','TWD','EUR','GBP','HKD','JPY'];
    $('#currency').replaceChildren(...currencyOptions.map(value=>{const option=document.createElement('option');option.value=value;option.textContent=value;return option;}));
    $('#name-field').hidden=!site.collect_name;$('#email-field').hidden=!site.collect_email;$('#message-field').hidden=!site.collect_message;
    $('#public-consent-label').hidden=!site.collect_name && !site.collect_message;
    $('.donor-details').hidden=!site.collect_name && !site.collect_email && !site.collect_message;
    if(site.contact_email){$('#contact-link').href=`mailto:${site.contact_email}`;$('#contact-link').hidden=false;}
    applyLanguage();setCurrency(site.language_currencies?.[locale] || {'zh-CN':'CNY','zh-TW':'TWD',en:'USD'}[locale],false);renderRecent();
    const params=new URLSearchParams(location.search);
    const id=params.get('donation'),token=params.get('status_token');
    if(id && token){showCheckout({id,status_token:token,status:'pending'});await pollStatus(true);}
  }catch(error){
    applyLanguage();$('#payment-methods').replaceChildren();const note=document.createElement('p');note.className='empty-note';note.textContent=t('loadFailed');$('#payment-methods').append(note);$('#donate-button').disabled=true;
  }
}
start();
