const $ = (selector, scope = document) => scope.querySelector(selector);
const $$ = (selector, scope = document) => [...scope.querySelectorAll(selector)];
const supportedLocales = ['zh-CN', 'zh-TW', 'en'];
const ui = {
  'zh-CN': {
    skip:'跳到捐赠', language:'语言', currency:'币种', amount:'金额', paymentMethod:'支付方式', loading:'正在加载…', leaveNote:'信息（选填）', nameOptional:'称呼', emailOptional:'邮箱', messageOptional:'留言', publicConsent:'公开称呼和留言', donate:'捐赠', donating:'正在准备支付…', terms:'用户协议', privacy:'隐私政策', contact:'联系', checkPayment:'查看状态', checking:'正在查询…', backDonate:'返回', openCheckout:'前往支付', noMethods:'无可用支付方式', chooseMethod:'请选择支付方式。', invalidAmount:'请输入大于 0 的金额，最多两位小数。', invalidJPY:'请输入大于 0 的日元整数金额。', amountTooHigh:'金额过大，请输入较小的金额。', requestFailed:'请求失败，请重试。', loadFailed:'加载失败，请刷新重试。', emailInvalid:'请填写有效的邮箱。', pendingTitle:'等待付款', pendingCopy:'请完成付款。', customPendingCopy:'请使用二维码或链接付款。', paidTitle:'已确认', paidCopy:'', failedTitle:'付款失败', failedCopy:'请返回重试。', refundedTitle:'已退款', refundedCopy:'', cancelledTitle:'已取消', cancelledCopy:'', statusWaiting:'等待支付确认。', customWaiting:'等待管理员确认。', statusUnavailable:'暂时无法查询，请稍后重试。', statusConfirmed:'已确认', anonymous:'匿名', close:'关闭', qrAlt:'支付二维码', unsupportedWaffo:'Waffo 不支持此币种，请切换币种或支付方式。', autoCheckPaused:'点击查看状态继续查询。', waffoAmountRange:'Waffo 金额范围：{min} 至 {max}。', paypalTWD:'PayPal 的台币金额需要是整数。'
  },
  'zh-TW': {
    skip:'跳到捐贈', language:'語言', currency:'幣別', amount:'金額', paymentMethod:'支付方式', loading:'正在載入…', leaveNote:'資訊（選填）', nameOptional:'稱呼', emailOptional:'信箱', messageOptional:'留言', publicConsent:'公開稱呼和留言', donate:'捐贈', donating:'正在準備支付…', terms:'使用者協議', privacy:'隱私權政策', contact:'聯絡', checkPayment:'查看狀態', checking:'正在查詢…', backDonate:'返回', openCheckout:'前往支付', noMethods:'無可用支付方式', chooseMethod:'請選擇支付方式。', invalidAmount:'請輸入大於 0 的金額，最多兩位小數。', invalidJPY:'請輸入大於 0 的日圓整數金額。', amountTooHigh:'金額過大，請輸入較小的金額。', requestFailed:'請求失敗，請重試。', loadFailed:'載入失敗，請重新整理。', emailInvalid:'請填寫有效的信箱。', pendingTitle:'等待付款', pendingCopy:'請完成付款。', customPendingCopy:'請使用 QR code 或連結付款。', paidTitle:'已確認', paidCopy:'', failedTitle:'付款失敗', failedCopy:'請返回重試。', refundedTitle:'已退款', refundedCopy:'', cancelledTitle:'已取消', cancelledCopy:'', statusWaiting:'等待支付確認。', customWaiting:'等待管理員確認。', statusUnavailable:'暫時無法查詢，請稍後重試。', statusConfirmed:'已確認', anonymous:'匿名', close:'關閉', qrAlt:'支付 QR code', unsupportedWaffo:'Waffo 不支援此幣別，請切換幣別或支付方式。', autoCheckPaused:'點擊查看狀態繼續查詢。', waffoAmountRange:'Waffo 金額範圍：{min} 至 {max}。', paypalTWD:'PayPal 的台幣金額需要是整數。'
  },
  en: {
    skip:'Skip to donation', language:'Language', currency:'Currency', amount:'Amount', paymentMethod:'Payment method', loading:'Loading…', leaveNote:'Details (optional)', nameOptional:'Name', emailOptional:'Email', messageOptional:'Note', publicConsent:'Show name and note publicly', donate:'Donate', donating:'Preparing payment…', terms:'Terms', privacy:'Privacy', contact:'Contact', checkPayment:'Check status', checking:'Checking…', backDonate:'Back', openCheckout:'Pay', noMethods:'No payment methods available', chooseMethod:'Choose a payment method.', invalidAmount:'Enter an amount greater than 0, with up to two decimal places.', invalidJPY:'Enter a whole yen amount greater than 0.', amountTooHigh:'The amount is too large. Enter a smaller amount.', requestFailed:'Request failed. Please retry.', loadFailed:'Could not load. Refresh to retry.', emailInvalid:'Enter a valid email address.', pendingTitle:'Pending payment', pendingCopy:'Complete your payment.', customPendingCopy:'Pay using the QR code or link.', paidTitle:'Donation confirmed', paidCopy:'', failedTitle:'Payment failed', failedCopy:'Go back to retry.', refundedTitle:'Refunded', refundedCopy:'', cancelledTitle:'Cancelled', cancelledCopy:'', statusWaiting:'Awaiting payment confirmation.', customWaiting:'Awaiting administrator confirmation.', statusUnavailable:'Status unavailable. Please retry shortly.', statusConfirmed:'Confirmed', anonymous:'Anonymous', close:'Close', qrAlt:'Payment QR code', unsupportedWaffo:'Waffo does not support this currency. Choose another currency or method.', autoCheckPaused:'Check status to resume.', waffoAmountRange:'Waffo amount range: {min} to {max}.', paypalTWD:'PayPal requires a whole-dollar amount for TWD.'
  }
};
const defaultPolicies = {
  'zh-CN': {terms:'本站为开源项目接受自愿捐赠。请确认金额、币种和支付方式后再继续。捐赠不构成商品购买、服务承诺或投资，不保证任何回报。\n\n支付由对应平台处理，适用其规则。退款、支付异常及其他疑问，请通过本站公布的联系方式联系管理员。使用本站即表示你同意合理、合法地使用本服务。', privacy:'我们仅收集完成、记录及通知捐赠所需的信息，包括金额、支付方式、时间，以及你自愿填写的称呼、邮箱和留言。支付账户及银行卡信息由支付平台处理。\n\n只有获得你的同意，称呼和留言才会公开；邮箱不公开。信息可能通过管理员配置的通知服务发送给管理员。我们不会出售你的个人信息。你可联系管理员请求更正或删除，依法须保留的记录除外。'},
  'zh-TW': {terms:'本站為開源專案接受自願捐贈。請確認金額、幣別和支付方式後再繼續。捐贈不構成商品購買、服務承諾或投資，不保證任何回報。\n\n支付由對應平台處理，適用其規則。退款、支付異常及其他疑問，請透過本站公布的聯絡方式聯絡管理員。使用本站即表示你同意合理、合法地使用本服務。', privacy:'我們僅收集完成、記錄及通知捐贈所需的資訊，包括金額、支付方式、時間，以及你自願填寫的稱呼、信箱和留言。支付帳戶及信用卡資訊由支付平台處理。\n\n只有獲得你的同意，稱呼和留言才會公開；信箱不公開。資訊可能透過管理員設定的通知服務發送給管理員。我們不會出售你的個人資訊。你可聯絡管理員請求更正或刪除，依法須保留的記錄除外。'},
  en: {terms:'This site accepts voluntary support for an open-source project. Check your amount, currency, and payment method before continuing. A donation is not a purchase, a service commitment, or an investment, and does not promise a return.\n\nPayments are processed by the selected payment provider under its rules. Contact the administrator using the details on this site for refund requests, payment issues, or other questions. Use this service reasonably and lawfully.', privacy:'We collect only the information needed to process, record, and notify the administrator about a donation: amount, method, time, and any name, email, or note you choose to provide. Payment providers handle card and payment account details.\n\nYour name and note appear publicly only with your consent. Your email is never public. Donation details may be delivered to the administrator through configured notification services. We do not sell your personal information. Contact the administrator to request corrections or deletion, except for records that must legally be retained.'}
};
const waffoRangesMinor={USD:[100,1000000],EUR:[100,940000],GBP:[100,815000],HKD:[800,7760000],JPY:[100,1600000],CNY:[100,100000]};
let site = null;
let locale = 'zh-CN';
let currency = 'CNY';
let submissionBusy = false;
let activeCheckout = null;
let pollTimer;
let pollCount = 0;
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
  $('#site-name').textContent = site?.name || 'Donate';
  $('#logo').setAttribute('aria-label',site?.name || 'Donate');
  for(const [key,selector] of [['tagline','#site-tagline'],['description','#site-description'],['footer','#site-footer-text']]) {
    const text = localText(key,'');
    const element = $(selector);
    setMultiline(element,text);
    element.hidden = !text.trim();
  }
  $('#custom-copy').hidden = $('#site-tagline').hidden && $('#site-description').hidden;
  document.title = 'Donate';
  document.querySelector('meta[name=description]').content = localText('description','').replaceAll('\n',' ');
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
}
function renderPresets() {
  const container=$('#presets');container.replaceChildren();
  const presets=(site?.presets?.length ? site.presets : [5,15,50,100]).slice(0,6);
  presets.forEach(amount => {
    const button=document.createElement('button');button.type='button';button.className='preset';button.textContent=String(amount);button.dataset.amount=String(amount);button.setAttribute('aria-label',`${t('amount')} ${currencySymbol(currency)}${amount}`);
    button.addEventListener('click',()=>{$('#amount').value=String(amount);$('#amount-error').hidden=true;markPreset();});container.append(button);
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
    if(unavailable) {label.style.opacity='.55';radio.checked=false;}
    label.append(radio,body);container.append(label);
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
function renderRecent() {
  const container=$('#recent-support');container.replaceChildren();
  const recent=Array.isArray(site?.recent) ? site.recent : [];
  $('#support-section').hidden=!recent.length;
  if(!recent.length)return;
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
  $('#checkout-title').textContent=t(`${key}Title`);
  const copy=t(key==='pending' && activeCheckout.custom ? 'customPendingCopy' : `${key}Copy`);
  $('#checkout-copy').textContent=copy;$('#checkout-copy').hidden=!copy;
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
    if(['paid','confirmed','completed','succeeded'].includes(status.status)) {try {site=await request('/api/site');renderRecent();}catch{}}
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
$('#amount').addEventListener('input',()=>{$('#amount-error').hidden=true;markPreset();});
$('#currency').addEventListener('change',()=>setCurrency($('#currency').value));
$('#language').addEventListener('change',()=>{locale=$('#language').value;storeValue('token-language',locale);applyLanguage();setCurrency(site?.language_currencies?.[locale] || {'zh-CN':'CNY','zh-TW':'TWD',en:'USD'}[locale]);renderRecent();});
$$('[data-policy]').forEach(button=>button.addEventListener('click',()=>showPolicy(button.dataset.policy)));
$('#close-policy').addEventListener('click',()=>$('#policy-dialog').close());
$('#policy-dialog').addEventListener('click',event=>{if(event.target===$('#policy-dialog')){const box=event.target.getBoundingClientRect();if(event.clientX<box.left || event.clientX>box.right || event.clientY<box.top || event.clientY>box.bottom)event.target.close();}});
$('#status-refresh').addEventListener('click',()=>{pollCount=0;pollStatus(true);});
$('#new-donation').addEventListener('click',()=>{clearTimeout(pollTimer);activeCheckout=null;donationFingerprint='';donationIdempotency='';$('#checkout-status').hidden=true;$('#donation-form').hidden=false;const url=new URL(location.href);url.searchParams.delete('donation');url.searchParams.delete('status_token');history.replaceState(null,'',url);$('#amount').focus();});
let logoClicks=[];
$('#logo').addEventListener('click',()=>{const now=Date.now();logoClicks=logoClicks.filter(time=>now-time<3000);logoClicks.push(now);if(logoClicks.length>=5)location.assign('/admin');});

document.addEventListener('visibilitychange',()=>{
  if(document.hidden)clearTimeout(pollTimer);
  else if(activeCheckout)schedulePoll(800);
});

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
