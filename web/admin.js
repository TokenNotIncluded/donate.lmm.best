const $ = (selector, root = document) => root.querySelector(selector);
const $$ = (selector, root = document) => [...root.querySelectorAll(selector)];
const escapeHTML = value => String(value ?? '').replace(/[&<>"']/g, char => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[char]));
const languages = ['zh-CN', 'zh-TW', 'en'];
const currencies = ['USD', 'CNY', 'TWD', 'EUR', 'GBP', 'HKD', 'JPY'];
const words = {
  publicSite:['打开网站','Open site','開啟網站'], logout:['退出','Sign out','登出'], checkingSession:['正在检查登录状态…','Checking your session…','正在檢查登入狀態…'], bootstrapPassword:['初始密码','Bootstrap password','初始密碼'], continueSetup:['继续设置','Continue setup','繼續設定'], passkeyLogin:['使用 Passkey 登录','Sign in with Passkey','使用 Passkey 登入'], bindPasskey:['绑定 Passkey','Create a Passkey','綁定 Passkey'], recovery:['恢复登录','Recovery','恢復登入'], resetLogin:['重置登录','Reset sign-in','重設登入'], ledger:['捐款记录','Contributions','捐款紀錄'], siteSettings:['网站与文字','Site & copy','網站與文字'], paymentMethods:['支付方式','Payment methods','支付方式'], waffoProducts:['Waffo 产品','Waffo products','Waffo 商品'], notifications:['通知','Notifications','通知'], statsSecurity:['统计与安全','Statistics & security','統計與安全'], exportCSV:['导出 CSV','Export CSV','匯出 CSV'], manualEntry:['手动录入','Manual entry','手動錄入'], filterStatus:['状态','Status','狀態'], all:['全部','All','全部'], paid:['已确认','Confirmed','已確認'], pending:['待确认','Pending','待確認'], refunded:['已退款','Refunded','已退款'], failed:['失败','Failed','失敗'], refresh:['刷新','Refresh','重新整理'], previous:['上一页','Previous','上一頁'], next:['下一页','Next','下一頁'], addMethod:['添加支付方式','Add a payment method','新增支付方式'], customMethod:['自定义二维码 / 链接','Custom QR / link','自訂 QR Code / 連結'], add:['添加','Add','新增'], waffoAccount:['Waffo 支付方式','Waffo payment method','Waffo 支付方式'], store:['店铺','Store','店鋪'], selectStore:['选择店铺','Choose a store','選擇店鋪'], loadStores:['连接并读取店铺','Connect & load stores','連線並讀取店鋪'], refreshProducts:['刷新产品','Refresh products','重新整理商品'], useStore:['设为支付店铺','Use this store','設為支付店鋪'], deliveryHistory:['发送记录','Delivery history','傳送紀錄'], currency:['币种','Currency','幣種'], refreshStats:['读取统计','Load statistics','讀取統計'], statsAPI:['统计 API','Statistics API','統計 API'], publicStats:['公开接口','Public endpoint','公開介面'], privateStats:['私有接口 · Bearer Token','Private endpoint · Bearer token','私有介面 · Bearer Token'], statsToken:['统计 Token','Statistics token','統計 Token'], showHide:['显示 / 隐藏','Show / hide','顯示 / 隱藏'], copyToken:['复制 Token','Copy token','複製 Token'], rotateToken:['更新 Token','Rotate token','更新 Token'], loginSecurity:['登录安全','Sign-in security','登入安全'], addPasskey:['添加备用 Passkey','Add a backup Passkey','新增備用 Passkey'], firstSetup:['首次设置','First-time setup','首次設定'], enrollTitle:['绑定 Passkey','Create a Passkey','綁定 Passkey'], loginTitle:['登录','Sign in','登入'], webauthnUnavailable:['此浏览器无法使用 Passkey。请使用支持 Passkey 的浏览器，并通过 HTTPS 或 localhost 打开。','This browser cannot use Passkeys. Open with a compatible browser over HTTPS or localhost.','此瀏覽器無法使用 Passkey。請使用支援 Passkey 的瀏覽器，並透過 HTTPS 或 localhost 開啟。'], cancelled:['Passkey 操作取消或未完成。可以再试一次。','The Passkey request was canceled or did not finish. Try again.','Passkey 操作已取消或未完成。可以再試一次。'], networkError:['连接失败。检查网络或服务器后重试。','Connection failed. Check the network or server and retry.','連線失敗。檢查網路或伺服器後重試。'], saving:['正在保存…','Saving…','正在儲存…'], saved:['配置已保存。','Settings saved.','設定已儲存。'], saveAll:['保存','Save','儲存'], loading:['正在读取…','Loading…','正在讀取…'], retry:['重试','Retry','重試'], name:['名称','Name','名稱'], description:['说明','Description','說明'], tagline:['标语','Tagline','標語'], footer:['页脚文字','Footer copy','頁尾文字'], contactEmail:['联系邮箱（可选）','Contact email (optional)','聯絡電子郵件（選填）'], baseCopy:['默认文字','Default copy','預設文字'], languageCurrency:['语言与币种','Language & currency','語言與幣種'], enabledLanguages:['可用语言','Available languages','可用語言'], defaultLanguage:['默认语言','Default language','預設語言'], enabledCurrencies:['可用币种','Available currencies','可用幣種'], defaultCurrency:['统计默认币种','Default statistics currency','統計預設幣種'], localeCurrency:['语言默认币种','Default currency for language','語言預設幣種'], presets:['预设金额','Preset amounts','預設金額'], donorInfo:['捐款人信息','Donor information','捐款人資訊'], collectName:['允许填写名字','Allow donor name','允許填寫名字'], collectEmail:['允许填写邮箱','Allow donor email','允許填寫電子郵件'], collectMessage:['允许留言','Allow donor message','允許留言'], terms:['用户协议','Terms','使用者協議'], privacy:['隐私政策','Privacy policy','隱私政策'], translations:['语言文字','Translated copy','各語言文字'], enabled:['启用','Enabled','啟用'], methodName:['显示名称','Display name','顯示名稱'], methodId:['固定标识','Stable ID','固定識別碼'], credentials:['凭证','Credentials','憑證'], environment:['环境','Environment','環境'], test:['测试','Test','測試'], production:['生产','Production','正式'], merchantId:['商户 ID','Merchant ID','商戶 ID'], privateKey:['商户私钥','Merchant private key','商戶私密金鑰'], storeId:['店铺 ID','Store ID','店鋪 ID'], productId:['产品 ID','Product ID','商品 ID'], taxCategory:['商品税务类别','Product tax category','商品稅務類別'], chooseCategory:['选择真实商品类别','Choose the actual product category','選擇實際商品類別'], qrURL:['二维码地址','QR image URL','QR Code 網址'], paymentURL:['付款链接','Payment link','付款連結'], uploadQR:['上传二维码','Upload QR image','上傳 QR Code'], uploadInvalid:['请上传不超过 2 MB 的 PNG、JPEG 或 WebP。','Use a PNG, JPEG, or WebP file up to 2 MB.','請上傳不超過 2 MB 的 PNG、JPEG 或 WebP。'], removeMethod:['移除此方式','Remove method','移除此方式'], removeConfirm:['移除这个支付方式？保存配置后生效。','Remove this payment method? Changes take effect after saving.','移除此支付方式？儲存設定後生效。'], noMethods:['未配置','Not configured','未設定'], amount:['金额','Amount','金額'], donorName:['捐款人名字（可选）','Donor name (optional)','捐款人名字（選填）'], donorEmail:['捐款人邮箱（可选）','Donor email (optional)','捐款人電子郵件（選填）'], message:['留言（可选）','Message (optional)','留言（選填）'], method:['支付方式','Payment method','支付方式'], offline:['线下 / 其他','Offline / other','線下 / 其他'], paidAt:['实际到账时间','Time received','實際到帳時間'], reference:['凭证 / 交易编号（可选）','Reference / transaction ID (optional)','憑證 / 交易編號（選填）'], consentPublic:['捐款人同意公开名字和留言','Donor agreed to publish their name and message','捐款人同意公開名字及留言'], recordDonation:['录入已确认捐款','Record confirmed contribution','錄入已確認捐款'], manualRecorded:['捐款已录入。','Contribution recorded.','捐款已錄入。'], amountInvalid:['请输入大于 0 的有效金额；JPY 不支持小数，其余币种最多两位小数。','Enter an amount greater than zero. JPY accepts whole units; other currencies accept up to two decimals.','請輸入大於 0 的有效金額；JPY 不支援小數，其他幣種最多兩位小數。'], noDonations:['暂无记录','No records','暫無紀錄'], anonymous:['匿名支持者','Anonymous supporter','匿名支持者'], details:['查看详情','View details','查看詳情'], confirmPayment:['确认收到','Confirm received','確認收到'], confirmFinal:['款项已核实，确认','Payment verified · confirm','款項已核實，確認'], confirmed:['款项已确认。','Payment confirmed.','款項已確認。'], cancel:['取消','Cancel','取消'], records:['条记录','records','筆紀錄'], utc:['当地时间','Local time','當地時間'], webhookURL:['Webhook 地址','Webhook URL','Webhook 網址'], webhookSecret:['签名密钥','Signing secret','簽章金鑰'], enableWebhook:['启用 Webhook','Enable webhook','啟用 Webhook'], emailNotification:['邮件通知','Email notification','電子郵件通知'], enableSMTP:['启用邮件通知','Enable email notification','啟用電子郵件通知'], smtpHost:['SMTP 服务器','SMTP host','SMTP 伺服器'], smtpPort:['端口','Port','連接埠'], smtpUser:['用户名','Username','使用者名稱'], smtpPassword:['密码','Password','密碼'], smtpFrom:['发件地址','From address','寄件地址'], smtpTo:['接收地址','Recipient address','收件地址'], noNotifications:['暂无记录','No records','暫無紀錄'], attempts:['次尝试','attempts','次嘗試'], sent:['已发送','Sent','已傳送'], queued:['等待发送','Queued','等待傳送'], retryScheduled:['已安排重试。','Retry scheduled.','已安排重試。'], tokenCopied:['Token 已复制。','Token copied.','Token 已複製。'], tokenRotated:['新 Token 已保存，旧 Token 已失效。','New token saved. The previous token is invalid.','新 Token 已儲存，舊 Token 已失效。'], rotateConfirm:['更新统计 Token？现有 API 客户端需要更换凭证。','Rotate the statistics token? Existing API clients will need the new credential.','更新統計 Token？現有 API 用戶端需要更換憑證。'], passkeyAdded:['Passkey 已绑定。','Passkey enrolled.','Passkey 已綁定。'], passkeys:['个已绑定 Passkey','enrolled Passkeys','個已綁定 Passkey'], noWaffo:['未配置 Waffo','Waffo not configured','未設定 Waffo'], noStores:['无可用店铺','No available stores','無可用店鋪'], storesLoaded:['选择店铺','Choose a store','選擇店鋪'], noProducts:['暂无产品','No products','暫無商品'], createProduct:['创建产品','Create product','建立商品'], editProduct:['编辑产品','Edit product','編輯商品'], productName:['产品名称','Product name','商品名稱'], startingPrice:['默认起始金额','Default starting price','預設起始金額'], publishFirst:['首次发布到生产','First publish to production','首次發佈至正式環境'], selectCreated:['创建后用作当前支付产品','Use the created product for this payment method','建立後用作目前的支付商品'], productCreated:['产品已创建。','Product created.','商品已建立。'], productUpdated:['产品已更新。','Product updated.','商品已更新。'], productDeactivated:['产品已停用。','Product deactivated.','商品已停用。'], edit:['编辑','Edit','編輯'], deactivate:['停用产品','Deactivate product','停用商品'], useProduct:['用于支付','Use for payments','用於支付'], deactivateConfirm:['停用这个 Waffo 产品？已存在的付款记录仍会保留。','Deactivate this Waffo product? Existing payment records will remain.','停用此 Waffo 商品？已有的付款紀錄仍會保留。'], active:['启用','Active','啟用'], inactive:['停用','Inactive','停用'], draft:['草稿','Draft','草稿'], productStatus:['产品状态','Product status','商品狀態'], selectCurrency:['至少保留一个币种和一种语言，并为语言选择启用的默认币种。','Keep at least one currency and language, and select enabled defaults.','請至少保留一個幣種及一種語言，並選擇已啟用的預設幣種。'], presetInvalid:['预设金额需要是大于 0 的数字，最多 8 个。','Enter between one and eight positive preset amounts.','預設金額須為大於 0 的數字，最多 8 個。'], configUnsaved:['有配置尚未保存。','Settings have unsaved changes.','有設定尚未儲存。'], selectWaffoStore:['先读取并选择店铺。','Load and choose a store first.','請先讀取並選擇店鋪。'], sessionExpired:['会话已过期，请重新登录。','Your session expired. Sign in again.','工作階段已過期，請重新登入。'], selectTaxCategory:['必须选择实际商品税务类别。','Choose the actual product tax category.','請選擇實際商品稅務類別。'], clipboardError:['无法自动复制。显示 Token 后手动复制。','Could not copy automatically. Reveal the token and copy it manually.','無法自動複製。請顯示 Token 後手動複製。'], reactivation:['保存修改','Save changes','儲存修改']
};
const storageRead = (storage, key) => {try {return window[storage].getItem(key);} catch {return null;}};
const storageWrite = (storage, key, value) => {try {window[storage].setItem(key,value);} catch { /* The interface also works with browser storage disabled. */ }};
const savedLocale = storageRead('localStorage','token-admin-language');
words.createAnother = ['创建另一个产品','Create another product','建立另一個商品'];
words.storeChanged = ['店铺已选择','Store selected','店鋪已選擇'];
let locale = languages.includes(savedLocale) ? savedLocale : (navigator.language.startsWith('zh-TW') || navigator.language.startsWith('zh-HK') ? 'zh-TW' : navigator.language.startsWith('zh') ? 'zh-CN' : 'en');
const t = key => words[key]?.[locale === 'en' ? 1 : locale === 'zh-TW' ? 2 : 0] ?? words[key]?.[0] ?? key;
const langNames = {'zh-CN':'简体中文','zh-TW':'繁體中文','en':'English'};
const taxNames = {digital_goods:'Digital goods',saas:'SaaS',software:'Software',ebook:'E-book',online_course:'Online course',consulting:'Consulting',professional_service:'Professional service'};
const waffoRanges = {USD:[100,1000000],EUR:[100,940000],GBP:[100,815000],HKD:[800,7760000],JPY:[100,1600000],CNY:[100,100000]};
let auth = {};
let settings = null;
let currentView = 'ledger';
let dirty = false;
let ledgerOffset = 0;
let ledgerTotal = 0;
let ledgerSequence = 0;
const pageSize = 25;
let catalogProducts = [];
let editingProduct = null;
let createdProductShown = false;
let catalogEpoch = 0;
let catalogIdentityFingerprint = '';
let catalogMutating = false;
let uploadsInFlight = 0;
let noticeTimer;

function translateStatic() {
  document.documentElement.lang = locale;
  $('#admin-language').value = locale;
  $$('[data-i18n]').forEach(element => { element.textContent = t(element.dataset.i18n); });
  updateBrand();
  updateHeaderContext();
}
function updateBrand() {
  const name = 'Donate';
  $('#brand-name').textContent = name;
  $('.brand').setAttribute('aria-label', name);
  document.title = name;
}
function updateHeaderContext() {
  const label = t({ledger:'ledger',site:'siteSettings',payments:'paymentMethods',catalog:'waffoProducts',notifications:'notifications',api:'statsSecurity'}[currentView] || 'ledger');
  return label;
}
function notify(message, error = false) {
  clearTimeout(noticeTimer);
  const element = $('#notice');
  element.textContent = message;
  element.classList.toggle('error', error);
  element.hidden = false;
  if (!error) noticeTimer = setTimeout(() => { element.hidden = true; }, 7000);
}
async function api(path, {method = 'GET', body, headers = {}} = {}) {
  const requestHeaders = {...headers};
  if (method !== 'GET' && method !== 'HEAD' && auth.csrf_token) requestHeaders['X-CSRF-Token'] = auth.csrf_token;
  if (body !== undefined && !(body instanceof FormData)) { requestHeaders['Content-Type'] = 'application/json'; body = JSON.stringify(body); }
  let response;
  try { response = await fetch(path, {method, body, headers:requestHeaders, credentials:'same-origin', cache:'no-store'}); }
  catch { throw new Error(t('networkError')); }
  const data = response.status === 204 ? null : await response.json().catch(() => null);
  if (!response.ok) {
    if (response.status === 401 && path.startsWith('/api/admin/')) {
      await checkAuth();
      throw new Error(t('sessionExpired'));
    }
    throw new Error(data?.error || `${response.status} ${response.statusText}`);
  }
  return data;
}
async function busy(button, action, errorElement = null) {
  if (button?.disabled) return;
  if (button) { button.disabled = true; button.setAttribute('aria-busy', 'true'); }
  if (errorElement) errorElement.hidden = true;
  try { return await action(); }
  catch (error) {
    const message = error.name === 'NotAllowedError' || error.name === 'AbortError' ? t('cancelled') : error.message || String(error);
    if (errorElement) { errorElement.textContent = message; errorElement.hidden = false; } else notify(message, true);
    return null;
  } finally { if (button) { button.disabled = false; button.removeAttribute('aria-busy'); } }
}
async function busyCatalog(button, action) {
  if(catalogMutating) return;
  catalogMutating = true;
  const controls = $$('#catalog-method, #catalog-store, #load-stores, #load-products, #use-store, #add-method, #methods-form button, #site-form button, #notifications-form button');
  const states = controls.map(control => [control,control.disabled]);
  controls.forEach(control => {control.disabled = true;});
  // The originating button may be among the locked controls; busy owns its state.
  if(button) button.disabled = false;
  try {return await busy(button,action);} finally {catalogMutating = false;states.forEach(([control,disabled]) => {control.disabled = disabled;});$$('#methods-form button, #site-form button, #notifications-form button').forEach(control => {control.disabled = !!uploadsInFlight;});}
}
function statusText(status) { return t({paid:'paid',confirmed:'paid',pending:'pending',refunded:'refunded',failed:'failed',sent:'sent',delivered:'sent',processing:'queued',queued:'queued',retry:'queued',active:'active',inactive:'inactive',draft:'draft'}[status] || status); }
const option = (value, label, selected = false) => `<option value="${escapeHTML(value)}"${selected ? ' selected' : ''}>${escapeHTML(label)}</option>`;
const currencyOptions = (selected, values = settings?.site?.currencies || currencies) => values.map(value => option(value, value, value === selected)).join('');
function field(label, name, value = '', {type = 'text', full = false, required = false, readonly = false, maxlength = '', placeholder = '', rows = 4} = {}) {
  const id = `field-${name.replace(/[^a-z0-9-]/gi, '-')}`;
  return `<div class="field${full ? ' span-all' : ''}"><label for="${escapeHTML(id)}">${escapeHTML(label)}</label>${type === 'textarea' ? `<textarea id="${id}" name="${escapeHTML(name)}" rows="${rows}"${required ? ' required' : ''}${maxlength ? ` maxlength="${maxlength}"` : ''}>${escapeHTML(value)}</textarea>` : `<input id="${id}" name="${escapeHTML(name)}" type="${type}" value="${escapeHTML(value)}"${required ? ' required' : ''}${readonly ? ' readonly' : ''}${maxlength ? ` maxlength="${maxlength}"` : ''}${placeholder ? ` placeholder="${escapeHTML(placeholder)}"` : ''}${type === 'password' ? ' autocomplete="new-password"' : ''}>`}</div>`;
}
function selectField(label, name, options, {full = false, required = false} = {}) {
  const id = `field-${name.replace(/[^a-z0-9-]/gi, '-')}`;
  return `<div class="field${full ? ' span-all' : ''}"><label for="${id}">${escapeHTML(label)}</label><select id="${id}" name="${escapeHTML(name)}"${required ? ' required' : ''}>${options}</select></div>`;
}
const check = (label, name, checked = false) => `<label class="check-field"><input type="checkbox" name="${escapeHTML(name)}"${checked ? ' checked' : ''}><span>${escapeHTML(label)}</span></label>`;
const saveFooter = () => `<div class="form-actions"><button type="submit" class="primary">${escapeHTML(t('saveAll'))}</button></div>`;
function markDirty() { dirty = true; }
function setFormDirtyHandlers() { ['site-form','methods-form','notifications-form'].forEach(id => $(`#${id}`).addEventListener('input', markDirty)); }
function amountMinor(value, currency) {
  const exponent = currency === 'JPY' ? 0 : 2;
  const raw = String(value).trim();
  const expression = exponent ? /^\d+(?:\.\d{1,2})?$/ : /^\d+$/;
  if (!expression.test(raw)) throw new Error(t('amountInvalid'));
  const [whole, fraction = ''] = raw.split('.');
  const minor = Number(whole) * 10 ** exponent + Number(fraction.padEnd(exponent, '0'));
  if (!Number.isSafeInteger(minor) || minor <= 0) throw new Error(t('amountInvalid'));
  return minor;
}
function majorAmount(value, currency) { return (Number(value || 0) / (currency === 'JPY' ? 1 : 100)).toFixed(currency === 'JPY' ? 0 : 2); }
function money(value, currency) { try { return new Intl.NumberFormat(locale, {style:'currency',currency}).format(Number(value) / (currency === 'JPY' ? 1 : 100)); } catch { return `${majorAmount(value, currency)} ${currency}`; } }
function dateTime(value) { if (!value) return '—'; const date = new Date(value); return Number.isNaN(date.getTime()) ? value : date.toLocaleString(locale); }
function localDate(value = new Date()) { const date = new Date(value); return new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0,16); }
function isoDate(value) { const date = new Date(value); if (Number.isNaN(date.getTime())) throw new Error(t('paidAt')); return date.toISOString(); }
function encodeBuffer(buffer) {
  const bytes = new Uint8Array(buffer); let binary = '';
  bytes.forEach(byte => { binary += String.fromCharCode(byte); });
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}
function decodeBuffer(value) { const normalized = value.replace(/-/g,'+').replace(/_/g,'/'); const binary = atob(normalized.padEnd(Math.ceil(normalized.length / 4) * 4,'=')); return Uint8Array.from(binary, char => char.charCodeAt(0)).buffer; }
function credentialOptions(data, registration) {
  const publicKey = {...data.publicKey,challenge:decodeBuffer(data.publicKey.challenge)};
  if (registration) { publicKey.user = {...publicKey.user,id:decodeBuffer(publicKey.user.id)}; publicKey.excludeCredentials = (publicKey.excludeCredentials || []).map(value => ({...value,id:decodeBuffer(value.id)})); }
  else publicKey.allowCredentials = (publicKey.allowCredentials || []).map(value => ({...value,id:decodeBuffer(value.id)}));
  return {publicKey};
}
function serializeCredential(credential) {
  const response = {clientDataJSON:encodeBuffer(credential.response.clientDataJSON)};
  for (const key of ['attestationObject','authenticatorData','signature','userHandle']) if (credential.response[key]) response[key] = encodeBuffer(credential.response[key]);
  if (credential.response.getTransports) response.transports = credential.response.getTransports();
  return {id:credential.id,rawId:encodeBuffer(credential.rawId),type:credential.type,authenticatorAttachment:credential.authenticatorAttachment,response,clientExtensionResults:credential.getClientExtensionResults()};
}
async function checkAuth() {
  auth = await api('/api/auth/status');
  $('#auth-loading').hidden = true;
  $('#auth-ready').hidden = false;
  $('#auth-view').hidden = !!auth.authenticated;
  $('#admin-view').hidden = !auth.authenticated;
  $('#logout').hidden = !auth.authenticated && !auth.needs_passkey;
  updateHeaderContext();
  renderAuth();
  return auth;
}
function renderAuth() {
  const enroll = auth.needs_passkey;
  $('#auth-step-title').textContent = t(enroll ? 'enrollTitle' : auth.initialized ? 'loginTitle' : 'firstSetup');
  $('#password-form').hidden = !!auth.initialized || !!enroll;
  $('#passkey-login').hidden = !auth.initialized || !!enroll;
  $('#passkey-register').hidden = !enroll;
  $('#passkey-count').textContent = `${auth.passkey_count || 0} ${t('passkeys')}`;
}
async function enrollPasskey() {
  if (!window.PublicKeyCredential || !window.isSecureContext) throw new Error(t('webauthnUnavailable'));
  const options = await api('/api/auth/register/begin', {method:'POST',body:{}});
  const credential = await navigator.credentials.create(credentialOptions(options, true));
  auth = await api('/api/auth/register/finish', {method:'POST',body:serializeCredential(credential)});
  await checkAuth();
  notify(t('passkeyAdded'));
  if (!settings) await loadAdmin();
}
async function loadAdmin() {
  settings = await api('/api/admin/settings');
  settings.site ||= {};
  settings.methods ||= [];
  settings.webhook ||= {};
  settings.smtp ||= {};
  renderSettings();
  renderManual();
  renderCatalogMethods();
  renderProductForm();
  renderSecurity();
  translateStatic();
  await loadLedger();
}
function renderSettings() { renderSite(); renderMethods(); renderNotificationsForm();if(catalogMutating) $$('#methods-form button, #site-form button, #notifications-form button').forEach(button => {button.disabled = true;}); }
function renderSite() {
  const site = settings.site;
  const activeLanguages = site.languages || languages;
  const activeCurrencies = site.currencies || currencies;
  const languageCurrencies = site.language_currencies || {'zh-CN':'CNY','zh-TW':'TWD','en':'USD'};
  $('#site-form').innerHTML = `<div class="config-section"><h2>${escapeHTML(t('baseCopy'))}</h2><div class="form-grid">${field(t('name'),'site-name',site.name,{required:true,maxlength:80})}${field(t('tagline'),'site-tagline',site.tagline,{maxlength:200})}${field(t('description'),'site-description',site.description,{type:'textarea',full:true,maxlength:3000})}${field(t('footer'),'site-footer',site.footer,{maxlength:500})}${field(t('contactEmail'),'site-contact_email',site.contact_email,{type:'email'})}</div></div><div class="config-section"><h2>${escapeHTML(t('languageCurrency'))}</h2><div class="form-grid"><div class="field span-all"><span>${escapeHTML(t('enabledLanguages'))}</span><div class="check-group">${languages.map(value => check(langNames[value],`language-${value}`,activeLanguages.includes(value))).join('')}</div></div>${selectField(t('defaultLanguage'),'site-default_language',languages.map(value => option(value,langNames[value],value === (site.default_language || 'zh-CN'))).join(''))}${selectField(t('defaultCurrency'),'site-currency',currencyOptions(site.currency || 'USD',currencies))}<div class="field span-all"><span>${escapeHTML(t('enabledCurrencies'))}</span><div class="check-group">${currencies.map(value => check(value,`currency-${value}`,activeCurrencies.includes(value))).join('')}</div></div>${languages.map(value => selectField(`${t('localeCurrency')} · ${langNames[value]}`,`locale-currency-${value}`,currencyOptions(languageCurrencies[value] || 'USD',currencies))).join('')}${field(t('presets'),'site-presets',(site.presets || [5,15,50,100]).join(', '),{placeholder:'5, 15, 50, 100',required:true})}</div></div><div class="config-section"><h2>${escapeHTML(t('donorInfo'))}</h2><div class="check-group">${check(t('collectName'),'site-collect_name',site.collect_name)}${check(t('collectEmail'),'site-collect_email',site.collect_email)}${check(t('collectMessage'),'site-collect_message',site.collect_message)}</div></div><div class="config-section"><h2>${escapeHTML(t('terms'))} / ${escapeHTML(t('privacy'))}</h2><div class="form-grid">${field(t('terms'),'site-terms',site.terms,{type:'textarea',rows:6})}${field(t('privacy'),'site-privacy',site.privacy,{type:'textarea',rows:6})}</div></div><div class="config-section"><h2>${escapeHTML(t('translations'))}</h2>${languages.map(value => {const translation = site.translations?.[value] || {}; return `<details class="translation"><summary>${escapeHTML(langNames[value])}</summary><div class="form-grid">${['tagline','description','footer','terms','privacy'].map(key => field(t(key),`translation-${value}-${key}`,translation[key],{type:key === 'tagline' || key === 'footer' ? 'text' : 'textarea',full:key === 'description'})).join('')}</div></details>`;}).join('')}</div>${saveFooter()}`;
}
const configs = {
  waffo:[['merchant_id','merchantId','text'],['private_key','privateKey','textarea'],['environment','environment','environment'],['store_id','storeId','text'],['product_id','productId','text'],['tax_category','taxCategory','tax']],
  stripe:[['secret_key','Secret key','password'],['webhook_secret','Webhook signing secret','password']],
  paypal:[['client_id','Client ID','text'],['client_secret','Client secret','password'],['webhook_id','Webhook ID','text'],['environment','environment','paypalEnv']],custom:[]
};
function taxOptions(selected) { return option('',t('chooseCategory'),!selected) + Object.entries(taxNames).map(([key,label]) => option(key,label,key === selected)).join(''); }
function methodMarkup(method, index) {
  const prefix = `method-${index}`;
  const config = method.config || {};
  const title = method.name || ({waffo:'Waffo Pancake',stripe:'Stripe',paypal:'PayPal',custom:t('customMethod')}[method.type]);
  return `<details class="method-block" data-method-index="${index}" open><summary><span class="method-title">${escapeHTML(title)}<span class="method-tag">${escapeHTML(method.type)} / ${escapeHTML(method.id)}</span></span></summary><div class="form-grid">${field(t('methodName'),`${prefix}-name`,method.name,{required:true,maxlength:100})}<div class="field"><span>${escapeHTML(t('enabled'))}</span>${check(t('enabled'),`${prefix}-enabled`,method.enabled)}</div>${field(t('description'),`${prefix}-description`,method.description,{type:'textarea',full:true,maxlength:1000})}${(configs[method.type] || []).map(([key,label,type]) => {const name = `${prefix}-config-${key}`;if(type === 'environment') return selectField(t(label),name,option('test',t('test'),config[key] !== 'prod') + option('prod',t('production'),config[key] === 'prod'));if(type === 'paypalEnv') return selectField(t(label),name,option('sandbox',t('test'),config[key] !== 'live') + option('live',t('production'),config[key] === 'live'));if(type === 'tax') return selectField(t(label),name,taxOptions(config[key]));return field(words[label] ? t(label) : label,name,config[key],{type,full:type === 'textarea'});}).join('')}${method.type === 'custom' ? `${field(t('qrURL'),`${prefix}-qr_url`,method.qr_url,{readonly:true})}${field(t('paymentURL'),`${prefix}-checkout_url`,method.checkout_url,{type:'url'})}<div class="field span-all"><label for="upload-${index}">${escapeHTML(t('uploadQR'))}</label><input id="upload-${index}" type="file" accept="image/png,image/jpeg,image/webp" data-upload-index="${index}">${method.qr_url ? `<img class="qr-preview" src="${escapeHTML(method.qr_url)}" alt="${escapeHTML(t('qrURL'))}">` : ''}</div>` : ''}</div><button type="button" class="danger method-remove" data-remove-method="${index}">${escapeHTML(t('removeMethod'))}</button></details>`;
}
function renderMethods() { $('#methods-form').innerHTML = settings.methods.length ? settings.methods.map(methodMarkup).join('') + saveFooter() : `<p class="empty-state">${escapeHTML(t('noMethods'))}</p>${saveFooter()}`; }
function renderNotificationsForm() {
  const webhook = settings.webhook, smtp = settings.smtp;
  $('#notifications-form').innerHTML = `<div class="config-section"><h2>Webhook</h2><div class="form-grid"><div class="span-all">${check(t('enableWebhook'),'webhook-enabled',webhook.enabled)}</div>${field(t('webhookURL'),'webhook-url',webhook.url,{type:'url',full:true})}${field(t('webhookSecret'),'webhook-secret',webhook.secret,{type:'password',full:true})}</div></div><div class="config-section"><h2>${escapeHTML(t('emailNotification'))}</h2><div class="form-grid"><div class="span-all">${check(t('enableSMTP'),'smtp-enabled',smtp.enabled)}</div>${field(t('smtpHost'),'smtp-host',smtp.host)}${field(t('smtpPort'),'smtp-port',smtp.port || 587,{type:'number'})}${field(t('smtpUser'),'smtp-username',smtp.username)}${field(t('smtpPassword'),'smtp-password',smtp.password,{type:'password'})}${field(t('smtpFrom'),'smtp-from',smtp.from,{type:'email'})}${field(t('smtpTo'),'smtp-to',smtp.to,{type:'email'})}</div></div>${saveFooter()}`;
}
function collectMethods() {
  const data = new FormData($('#methods-form'));
  return settings.methods.map((method,index) => {const prefix = `method-${index}`;const config = {...method.config};for(const [key] of configs[method.type] || []) config[key] = String(data.get(`${prefix}-config-${key}`) || '').trim();return {...method,name:String(data.get(`${prefix}-name`) || '').trim(),description:String(data.get(`${prefix}-description`) || '').trim(),enabled:data.has(`${prefix}-enabled`),qr_url:method.type === 'custom' ? String(data.get(`${prefix}-qr_url`) || '').trim() : method.qr_url || '',checkout_url:method.type === 'custom' ? String(data.get(`${prefix}-checkout_url`) || '').trim() : method.checkout_url || '',config};});
}
function collectSettings() {
  for(const id of ['site-form','methods-form','notifications-form']) if (!$(`#${id}`).reportValidity()) throw new Error(t('configUnsaved'));
  const siteData = new FormData($('#site-form'));
  const selectedLanguages = languages.filter(value => siteData.has(`language-${value}`));
  const selectedCurrencies = currencies.filter(value => siteData.has(`currency-${value}`));
  const defaultLanguage = siteData.get('site-default_language'), defaultCurrency = siteData.get('site-currency');
  const languageCurrencies = Object.fromEntries(languages.map(value => [value,siteData.get(`locale-currency-${value}`)]));
  if (!selectedLanguages.length || !selectedCurrencies.length || !selectedLanguages.includes(defaultLanguage) || !selectedCurrencies.includes(defaultCurrency) || selectedLanguages.some(value => !selectedCurrencies.includes(languageCurrencies[value]))) throw new Error(t('selectCurrency'));
  const presets = String(siteData.get('site-presets')).split(',').map(value => Number(value.trim()));
  if (!presets.length || presets.length > 8 || presets.some(value => !Number.isSafeInteger(value) || value <= 0 || value > 1000000)) throw new Error(t('presetInvalid'));
  const site = {...settings.site,languages:selectedLanguages,currencies:selectedCurrencies,default_language:defaultLanguage,currency:defaultCurrency,language_currencies:languageCurrencies,presets,translations:{...settings.site.translations}};
  for(const key of ['name','tagline','description','footer','contact_email','terms','privacy']) site[key] = String(siteData.get(`site-${key}`) || '').trim();
  for(const key of ['collect_name','collect_email','collect_message']) site[key] = siteData.has(`site-${key}`);
  for(const language of languages) {site.translations[language] = {...site.translations[language]};for(const key of ['tagline','description','footer','terms','privacy']) site.translations[language][key] = String(siteData.get(`translation-${language}-${key}`) || '').trim();}
  const notifications = new FormData($('#notifications-form'));
  const webhook = {...settings.webhook,enabled:notifications.has('webhook-enabled'),url:String(notifications.get('webhook-url') || '').trim(),secret:String(notifications.get('webhook-secret') || '').trim()};
  const smtp = {...settings.smtp,enabled:notifications.has('smtp-enabled'),port:Number(notifications.get('smtp-port'))};
  for(const key of ['host','username','password','from','to']) smtp[key] = String(notifications.get(`smtp-${key}`) || '').trim();
  return {...settings,site,methods:collectMethods(),webhook,smtp};
}
async function saveSettings() {
  const next = collectSettings();
  await api('/api/admin/settings',{method:'PUT',body:next});
  const identityChanged = syncCatalogIdentity(next.methods.find(method => method.id === $('#catalog-method').value));
  settings = await api('/api/admin/settings');
  dirty = false;
  renderSettings();renderManual();renderCatalogMethods();renderSecurity();
  if(identityChanged) renderProductForm();
  updateBrand();
  notify(t('saved'));
}
function renderManual() {
  const currency = settings.site.currency || 'USD';
  $('#manual-form').innerHTML = `${field(t('amount'),'manual-amount','',{required:true,placeholder:'15.00'})}${selectField(t('currency'),'manual-currency',currencyOptions(currency))}${selectField(t('method'),'manual-method',option('',t('offline')) + settings.methods.map(value => option(value.id,value.name)).join(''))}${field(t('paidAt'),'manual-paid_at',localDate(),{type:'datetime-local',required:true})}${field(t('donorName'),'manual-name','',{maxlength:100})}${field(t('donorEmail'),'manual-email','',{type:'email'})}${field(t('reference'),'manual-reference','',{full:true,maxlength:200})}${field(t('message'),'manual-message','',{type:'textarea',full:true,maxlength:2000})}<div class="span-all">${check(t('consentPublic'),'manual-public')}</div><div class="actions span-all"><button type="submit" class="primary">${escapeHTML(t('recordDonation'))}</button></div>`;
  $('[name="manual-amount"]').inputMode = 'decimal';
}
async function loadLedger() {
  const sequence = ++ledgerSequence;
  $('#ledger-list').innerHTML = `<p class="empty-state">${escapeHTML(t('loading'))}</p>`;
  let data;
  try {data = await api(`/api/admin/donations?status=${encodeURIComponent($('#ledger-status').value)}&limit=${pageSize}&offset=${ledgerOffset}`);}
  catch(error) {if(sequence === ledgerSequence) $('#ledger-list').innerHTML = `<p class="empty-state inline-error">${escapeHTML(error.message)}</p>`;throw error;}
  if(sequence !== ledgerSequence) return;
  ledgerTotal = data.total || 0;
  $('#ledger-count').textContent = `${ledgerTotal} ${t('records')}`;
  $('#ledger-prev').disabled = ledgerOffset === 0;
  $('#ledger-next').disabled = ledgerOffset + pageSize >= ledgerTotal;
  $('#ledger-page').textContent = `${Math.floor(ledgerOffset / pageSize) + 1} / ${Math.max(1,Math.ceil(ledgerTotal / pageSize))}`;
  $('#ledger-list').innerHTML = data.donations?.length ? data.donations.map(donation => {
    const canConfirm = donation.status === 'pending' && (donation.method_type === 'custom' || donation.custom || settings.methods.find(method => method.id === donation.method_id)?.type === 'custom');
    return `<article class="donation-row"><div><div class="donation-main"><span class="donation-amount">${escapeHTML(money(donation.amount_minor,donation.currency))}</span><span class="donation-name">${escapeHTML(donation.name || t('anonymous'))}</span></div><div class="donation-meta"><span>${escapeHTML(donation.method_name || donation.method_id)}</span><time>${escapeHTML(dateTime(donation.paid_at || donation.created_at))}</time><span>${escapeHTML(donation.source)}</span></div>${donation.message ? `<p class="donation-message">${escapeHTML(donation.message)}</p>` : ''}<details class="donation-details"><summary>${escapeHTML(t('details'))}</summary><p>ID: ${escapeHTML(donation.id)}</p>${donation.email ? `<p>Email: ${escapeHTML(donation.email)}</p>` : ''}<p>${escapeHTML(t('consentPublic'))}: ${donation.public ? escapeHTML(t('enabled')) : '—'}</p></details></div><div class="donation-side"><span class="status ${escapeHTML(donation.status)}">${escapeHTML(statusText(donation.status))}</span>${canConfirm ? `<button type="button" class="quiet" data-show-confirm="${escapeHTML(donation.id)}">${escapeHTML(t('confirmPayment'))}</button>` : ''}</div>${canConfirm ? `<form class="confirm-form" data-confirm-id="${escapeHTML(donation.id)}" hidden>${field(t('reference'),`confirm-reference-${donation.id}`,'',{maxlength:200})}${field(t('paidAt'),`confirm-paid-${donation.id}`,localDate(),{type:'datetime-local',required:true})}<button type="submit" class="primary">${escapeHTML(t('confirmFinal'))}</button></form>` : ''}</article>`;
  }).join('') : `<p class="empty-state">${escapeHTML(t('noDonations'))}</p>`;
}
async function loadNotifications() {
  $('#notifications-list').innerHTML = `<p class="empty-state">${escapeHTML(t('loading'))}</p>`;
  let data;
  try {data = await api('/api/admin/notifications');} catch(error) {$('#notifications-list').innerHTML = `<p class="empty-state inline-error">${escapeHTML(error.message)}</p>`;throw error;}
  $('#notifications-list').innerHTML = data.notifications?.length ? data.notifications.map(item => `<article class="notification-row"><div><span class="status ${escapeHTML(item.status)}">${escapeHTML(statusText(item.status))} · ${escapeHTML(item.kind)}</span><p class="muted">${escapeHTML(dateTime(item.created_at))} / ${item.attempts || 0} ${escapeHTML(t('attempts'))}</p>${item.last_error ? `<p class="inline-error">${escapeHTML(item.last_error)}</p>` : ''}<code>${escapeHTML(item.event_id)}</code></div>${item.status === 'failed' ? `<button type="button" class="quiet" data-retry-notification="${escapeHTML(item.id)}">${escapeHTML(t('retry'))}</button>` : ''}</article>`).join('') : `<p class="empty-state">${escapeHTML(t('noNotifications'))}</p>`;
}
function renderSecurity() { const selected = $('#stats-currency').value || settings.site.currency;$('#stats-currency').innerHTML = currencyOptions(selected);$('#stats-token').value = settings.stats_token || '';$('#passkey-count').textContent = `${auth.passkey_count || 0} ${t('passkeys')}`; }
async function loadStats() { $('#stats-result').textContent = t('loading');try {const data = await api(`/api/stats?currency=${encodeURIComponent($('#stats-currency').value)}`);$('#stats-result').textContent = JSON.stringify(data,null,2);} catch(error) {$('#stats-result').textContent = error.message;throw error;} }
function renderCatalogMethods() {
  const selected = $('#catalog-method').value;
  const methods = settings.methods.filter(method => method.type === 'waffo');
  $('#catalog-method').innerHTML = methods.length ? methods.map(method => option(method.id,method.name,method.id === selected)).join('') : option('',t('noWaffo'));
  $('#load-stores').disabled = !methods.length;
  syncCatalogIdentity(methods.find(method => method.id === $('#catalog-method').value));
}
function syncCatalogIdentity(method) {
  // Store/product selections belong to this account and must survive ordinary saves.
  const config = method?.config || {};
  const fingerprint = method ? JSON.stringify([method.id,config.merchant_id || '',config.private_key || '',config.environment || 'test']) : '';
  if(fingerprint === catalogIdentityFingerprint) return false;
  catalogIdentityFingerprint = fingerprint;
  catalogEpoch++;
  $('#catalog-store').innerHTML = option('',t('selectStore'));
  $('#catalog-store').disabled = true;
  $('#load-products').disabled = true;
  $('#use-store').disabled = true;
  catalogProducts = [];
  editingProduct = null;
  createdProductShown = false;
  $('#products-list').innerHTML = '';
  $('#catalog-status').textContent = '';
  renderProductForm();
  return true;
}
function currentCatalogMethod() { const method = settings.methods.find(value => value.id === $('#catalog-method').value);if(!method) throw new Error(t('noWaffo'));return method; }
async function loadStores() {
  const method = currentCatalogMethod();
  const epoch = catalogEpoch;
  $('#catalog-status').textContent = t('loading');
  let data;
  try {data = await api(`/api/admin/waffo/stores?method_id=${encodeURIComponent(method.id)}`);} catch(error) {if(epoch === catalogEpoch) $('#catalog-status').textContent = error.message;throw error;}
  if(epoch !== catalogEpoch || $('#catalog-method').value !== method.id) return;
  const stores = data.stores || [];
  $('#catalog-store').innerHTML = option('',t('selectStore')) + stores.map(store => option(store.id,`${store.name || store.slug || store.id}${store.status ? ` · ${statusText(store.status)}` : ''}`,store.id === method.config?.store_id)).join('');
  $('#catalog-store').disabled = !stores.length;
  $('#catalog-status').textContent = t(stores.length ? 'storesLoaded' : 'noStores');
  $('#load-products').disabled = !$('#catalog-store').value;
  $('#use-store').disabled = !$('#catalog-store').value;
  editingProduct = null;renderProductForm();
  if($('#catalog-store').value) await loadProducts();
}
async function loadProducts() {
  const method = currentCatalogMethod(), store = $('#catalog-store').value;
  const epoch = catalogEpoch;
  if(!store) throw new Error(t('selectWaffoStore'));
  $('#products-list').innerHTML = `<p class="empty-state">${escapeHTML(t('loading'))}</p>`;
  let data;
  try {data = await api(`/api/admin/waffo/products?method_id=${encodeURIComponent(method.id)}&store_id=${encodeURIComponent(store)}`);} catch(error) {if(epoch === catalogEpoch) $('#products-list').innerHTML = `<p class="empty-state inline-error">${escapeHTML(error.message)}</p>`;throw error;}
  if(epoch !== catalogEpoch || $('#catalog-method').value !== method.id || $('#catalog-store').value !== store) return;
  catalogProducts = data.products || [];
  $('#catalog-status').textContent = '';
  renderProducts();
}
function renderProductForm() {
  const product = editingProduct;
  const priceCurrency = product ? Object.keys(product.prices || {})[0] : (settings.site.currency === 'TWD' ? 'USD' : settings.site.currency || 'USD');
  const price = product?.prices?.[priceCurrency];
  const selectedMethod = settings.methods.find(method => method.id === $('#catalog-method').value);
  const available = currencies.filter(currency => currency !== 'TWD');
  const range = waffoRanges[priceCurrency] || waffoRanges.USD;
  const defaultMinor = Math.max(range[0],Math.min(range[1],(settings.site.presets?.[0] || 5) * (priceCurrency === 'JPY' ? 1 : 100)));
  const startingAmount = price ? majorAmount(price.amount_minor,priceCurrency) : majorAmount(defaultMinor,priceCurrency);
  $('#product-form').innerHTML = `<div class="product-editor"><h2>${escapeHTML(t(product ? 'editProduct' : 'createProduct'))}</h2><div class="form-grid">${field(t('productName'),'product-name',product?.name || settings.site.name || 'Donate',{required:true,maxlength:64})}${selectField(t('currency'),'product-currency',currencyOptions(priceCurrency || 'USD',available))}${field(t('description'),'product-description',product?.description || settings.site.description || '',{type:'textarea',full:true,maxlength:3000})}${field(t('startingPrice'),'product-amount',startingAmount,{required:true})}${selectField(t('taxCategory'),'product-tax_category',taxOptions(price?.tax_category || selectedMethod?.config?.tax_category),{required:true})}${product ? selectField(t('productStatus'),'product-status',option('active',t('active'),product.status !== 'inactive') + option('inactive',t('inactive'),product.status === 'inactive')) : `<div class="span-all">${check(t('selectCreated'),'product-select',true)}</div>`}<div class="span-all">${check(t('publishFirst'),'product-publish')}</div><div class="actions span-all"><button type="submit" class="primary">${escapeHTML(t(product ? 'reactivation' : 'createProduct'))}</button>${product ? `<button type="button" id="cancel-product" class="quiet">${escapeHTML(t(createdProductShown ? 'createAnother' : 'cancel'))}</button>` : ''}</div></div></div>`;
  $('[name="product-amount"]').inputMode = 'decimal';
}
function renderProducts() {
  $('#products-list').innerHTML = catalogProducts.length ? catalogProducts.map(product => `<article class="product-row"><div><h3>${escapeHTML(product.name)}</h3><p>${escapeHTML(statusText(product.status))}${product.has_prod_version ? ` · ${escapeHTML(t('production'))}` : ''}${Object.entries(product.prices || {}).map(([currency,price]) => ` / ${escapeHTML(money(price.amount_minor,currency))}`).join('')}</p>${product.description ? `<p>${escapeHTML(product.description)}</p>` : ''}<code>${escapeHTML(product.id)}</code></div><div class="actions"><button type="button" class="quiet" data-edit-product="${escapeHTML(product.id)}">${escapeHTML(t('edit'))}</button><button type="button" class="quiet" data-use-product="${escapeHTML(product.id)}">${escapeHTML(t('useProduct'))}</button>${product.status !== 'inactive' ? `<button type="button" class="danger" data-deactivate-product="${escapeHTML(product.id)}">${escapeHTML(t('deactivate'))}</button>` : ''}</div></article>`).join('') : `<p class="empty-state">${escapeHTML(t('noProducts'))}</p>`;
}
let operationCache;
try {operationCache = JSON.parse(storageRead('sessionStorage','token-admin-operations') || '{}');} catch {operationCache = {};}
function operationKey(method, path, body) {
  const config = path.startsWith('/api/admin/waffo/products') ? settings.methods.find(value => value.id === body.method_id)?.config : null;
  const signature = JSON.stringify(config ? [method,path,body,config.merchant_id || '',config.environment || 'test'] : [method,path,body]);
  const pending = operationCache;
  const key = pending[signature] || crypto.randomUUID();
  pending[signature] = key;
  const entries = Object.entries(pending).slice(-30);
  operationCache = Object.fromEntries(entries);
  storageWrite('sessionStorage','token-admin-operations',JSON.stringify(operationCache));
  return {key,signature};
}
async function idempotentMutation(path,method,body) {
  const operation = operationKey(method,path,body);
  const result = await api(path,{method,body,headers:{'Idempotency-Key':operation.key}});
  delete operationCache[operation.signature];storageWrite('sessionStorage','token-admin-operations',JSON.stringify(operationCache));
  return result;
}
async function selectProduct(productId, storeId, methodId) {
  const index = settings.methods.findIndex(value => value.id === methodId);
  if(index < 0) throw new Error(t('noWaffo'));
  $(`[name="method-${index}-config-product_id"]`).value = productId;
  $(`[name="method-${index}-config-store_id"]`).value = storeId;
  if(editingProduct?.prices) {const price = Object.values(editingProduct.prices)[0];if(price?.tax_category) $(`[name="method-${index}-config-tax_category"]`).value = price.tax_category;}
  await saveSettings();
}
async function showView(view) {
  currentView = view;
  $('#view-announcement').textContent = updateHeaderContext();
  $$('#admin-nav button').forEach(button => {if(button.dataset.view === view) button.setAttribute('aria-current','page');else button.removeAttribute('aria-current');});
  $$('.view').forEach(section => { section.hidden = section.id !== `view-${view}`; });
  if(view === 'notifications') await loadNotifications();
  if(view === 'api') await loadStats();
}

$('#password-form').addEventListener('submit',event => {
  event.preventDefault();
  busy($('button',event.currentTarget),async () => {auth = await api('/api/auth/password',{method:'POST',body:{password:$('#bootstrap-password').value}});$('#bootstrap-password').value = '';await checkAuth();},$('#auth-error'));
});
$('#passkey-login').addEventListener('click',event => busy(event.currentTarget,async () => {
  if(!window.PublicKeyCredential || !window.isSecureContext) throw new Error(t('webauthnUnavailable'));
  const options = await api('/api/auth/login/begin',{method:'POST',body:{}});
  const credential = await navigator.credentials.get(credentialOptions(options,false));
  auth = await api('/api/auth/login/finish',{method:'POST',body:serializeCredential(credential)});
  await checkAuth();await loadAdmin();
},$('#auth-error')));
$('#passkey-register').addEventListener('click',event => busy(event.currentTarget,enrollPasskey,$('#auth-error')));
$('#add-passkey').addEventListener('click',event => busy(event.currentTarget,enrollPasskey));
$('#logout').addEventListener('click',event => busy(event.currentTarget,async () => {await api('/api/auth/logout',{method:'POST',body:{}});settings = null;dirty = false;await checkAuth();}));
$('#admin-nav').addEventListener('click',event => {const button = event.target.closest('[data-view]');if(button) busy(button,() => showView(button.dataset.view));});
$('#admin-language').addEventListener('change',event => {
  if(settings) {try {settings = collectSettings();} catch(error) {event.target.value = locale;notify(error.message,true);return;}}
  locale = event.target.value;storageWrite('localStorage','token-admin-language',locale);translateStatic();renderAuth();
  if(settings) {renderSettings();renderManual();renderSecurity();renderCatalogMethods();renderProductForm();renderProducts();busy(null,() => showView(currentView));busy(null,loadLedger);}
});
for(const id of ['site-form','methods-form','notifications-form']) $(`#${id}`).addEventListener('submit',event => {event.preventDefault();if(catalogMutating || uploadsInFlight) return;busy($('button[type="submit"]',event.currentTarget),saveSettings);});
setFormDirtyHandlers();
$('#add-method').addEventListener('click',() => {if(catalogMutating || uploadsInFlight) return;settings.methods = collectMethods();const type = $('#new-method-type').value;settings.methods.push({id:`${type}-${crypto.randomUUID().slice(0,8)}`,type,name:{waffo:'Waffo Pancake',stripe:'Stripe',paypal:'PayPal',custom:t('customMethod')}[type],description:'',enabled:false,qr_url:'',checkout_url:'',config:{}});markDirty();renderMethods();renderCatalogMethods();});
$('#methods-form').addEventListener('click',event => {const button = event.target.closest('[data-remove-method]');if(!button || catalogMutating || uploadsInFlight || !confirm(t('removeConfirm'))) return;settings.methods = collectMethods();settings.methods.splice(Number(button.dataset.removeMethod),1);markDirty();renderMethods();renderCatalogMethods();});
$('#methods-form').addEventListener('change',event => {
  if(!event.target.matches('[data-upload-index]')) return;
  const input = event.target, file = input.files?.[0], methodId = settings.methods[Number(input.dataset.uploadIndex)]?.id;if(!file || !methodId) return;
  busy(null,async () => {if(!['image/png','image/jpeg','image/webp'].includes(file.type) || file.size > 2 * 1024 * 1024) throw new Error(t('uploadInvalid'));input.disabled = true;uploadsInFlight++;$('#add-method').disabled = true;$$('[data-remove-method]').forEach(button => {button.disabled = true;});try {const data = new FormData();data.append('file',file);const uploaded = await api('/api/admin/upload',{method:'POST',body:data});const index = settings.methods.findIndex(method => method.id === methodId);if(index < 0) return;$(`[name="method-${index}-qr_url"]`).value = uploaded.url;markDirty();settings.methods = collectMethods();renderMethods();} finally {uploadsInFlight--;input.disabled = false;$('#add-method').disabled = !!uploadsInFlight;$$('[data-remove-method]').forEach(button => {button.disabled = !!uploadsInFlight;});}});
});
$('#manual-form').addEventListener('submit',event => {event.preventDefault();const form = event.currentTarget;busy($('button[type="submit"]',form),async () => {const data = new FormData(form), currency = data.get('manual-currency'), method = settings.methods.find(value => value.id === data.get('manual-method'));await idempotentMutation('/api/admin/donations','POST',{amount_minor:amountMinor(data.get('manual-amount'),currency),currency,method_id:method?.id || '',method_name:method?.name || t('offline'),name:data.get('manual-name'),email:data.get('manual-email'),message:data.get('manual-message'),public:data.has('manual-public'),paid_at:isoDate(data.get('manual-paid_at')),reference:data.get('manual-reference')});renderManual();ledgerOffset = 0;notify(t('manualRecorded'));try {await loadLedger();} catch(error) {notify(`${t('manualRecorded')} ${error.message}`,true);}});});
$('#ledger-status').addEventListener('change',() => {ledgerOffset = 0;busy(null,loadLedger);});
$('#ledger-refresh').addEventListener('click',event => busy(event.currentTarget,loadLedger));
$('#ledger-prev').addEventListener('click',event => {ledgerOffset = Math.max(0,ledgerOffset - pageSize);busy(event.currentTarget,loadLedger);});
$('#ledger-next').addEventListener('click',event => {ledgerOffset += pageSize;busy(event.currentTarget,loadLedger);});
$('#ledger-list').addEventListener('click',event => {const button = event.target.closest('[data-show-confirm]');if(!button) return;const form = $$('.confirm-form').find(value => value.dataset.confirmId === button.dataset.showConfirm);form.hidden = !form.hidden;button.setAttribute('aria-expanded',String(!form.hidden));if(!form.hidden) $('input',form).focus();});
$('#ledger-list').addEventListener('submit',event => {const form = event.target.closest('[data-confirm-id]');if(!form) return;event.preventDefault();busy($('button[type="submit"]',form),async () => {const data = new FormData(form),id = form.dataset.confirmId;await api(`/api/admin/donations/${encodeURIComponent(id)}/confirm`,{method:'POST',body:{reference:data.get(`confirm-reference-${id}`),paid_at:isoDate(data.get(`confirm-paid-${id}`))}});await loadLedger();notify(t('confirmed'));});});
$('#notifications-refresh').addEventListener('click',event => busy(event.currentTarget,loadNotifications));
$('#notifications-list').addEventListener('click',event => {const button = event.target.closest('[data-retry-notification]');if(button) busy(button,async () => {await api(`/api/admin/notifications/${encodeURIComponent(button.dataset.retryNotification)}/retry`,{method:'POST',body:{}});await loadNotifications();notify(t('retryScheduled'));});});
$('#stats-refresh').addEventListener('click',event => busy(event.currentTarget,loadStats));
$('#stats-currency').addEventListener('change',() => busy(null,loadStats));
$('#reveal-token').addEventListener('click',() => {$('#stats-token').type = $('#stats-token').type === 'password' ? 'text' : 'password';});
$('#copy-token').addEventListener('click',event => busy(event.currentTarget,async () => {try {await navigator.clipboard.writeText($('#stats-token').value);} catch {throw new Error(t('clipboardError'));}notify(t('tokenCopied'));}));
$('#rotate-token').addEventListener('click',event => {if(!confirm(t('rotateConfirm'))) return;busy(event.currentTarget,async () => {const next = collectSettings();const bytes = crypto.getRandomValues(new Uint8Array(32));next.stats_token = encodeBuffer(bytes.buffer);await api('/api/admin/settings',{method:'PUT',body:next});settings = await api('/api/admin/settings');dirty = false;renderSettings();renderSecurity();notify(t('tokenRotated'));});});
$('#load-stores').addEventListener('click',event => busy(event.currentTarget,loadStores));
$('#load-products').addEventListener('click',event => busy(event.currentTarget,loadProducts));
$('#catalog-method').addEventListener('change',() => {syncCatalogIdentity(settings.methods.find(method => method.id === $('#catalog-method').value));});
$('#catalog-store').addEventListener('change',() => {catalogEpoch++;$('#load-products').disabled = !$('#catalog-store').value;$('#use-store').disabled = !$('#catalog-store').value;editingProduct = null;renderProductForm();if($('#catalog-store').value) busy(null,loadProducts);else $('#products-list').innerHTML = '';});
$('#use-store').addEventListener('click',event => busyCatalog(event.currentTarget,async () => {const method = currentCatalogMethod(), index = settings.methods.findIndex(value => value.id === method.id),store = $('#catalog-store').value;const changed = method.config?.store_id !== store;$(`[name="method-${index}-config-store_id"]`).value = store;if(changed) {$(`[name="method-${index}-config-product_id"]`).value = '';$(`[name="method-${index}-enabled"]`).checked = false;}await saveSettings();if(changed) notify(t('storeChanged'));}));
$('#product-form').addEventListener('submit',event => {
  event.preventDefault();const form = event.currentTarget;
  busyCatalog($('button[type="submit"]',form),async () => {
    const method = currentCatalogMethod(), store = $('#catalog-store').value;if(!store) throw new Error(t('selectWaffoStore'));
    const data = new FormData(form), currency = data.get('product-currency');
    if(!data.get('product-tax_category')) throw new Error(t('selectTaxCategory'));
    const body = {method_id:method.id,store_id:store,name:String(data.get('product-name')).trim(),description:String(data.get('product-description')).trim(),currency,amount_minor:amountMinor(data.get('product-amount'),currency),tax_category:data.get('product-tax_category'),publish:data.has('product-publish')};
    const range = waffoRanges[currency];
    if(!range || body.amount_minor < range[0] || body.amount_minor > range[1]) throw new Error(`${t('startingPrice')}: ${money(range[0],currency)} – ${money(range[1],currency)}`);
    const editing = !!editingProduct;if(editing) body.status = data.get('product-status');
    if(editing) body.prices = editingProduct.prices || {};
    const result = await idempotentMutation(`/api/admin/waffo/products${editing ? `/${encodeURIComponent(editingProduct.id)}` : ''}`,editing ? 'PUT' : 'POST',body);
    const productId = result?.id || result?.product?.id || result?.product_id;
    editingProduct = !editing ? result?.product || {...body,id:productId,prices:{[currency]:{amount_minor:body.amount_minor,tax_category:body.tax_category}}} : null;
    createdProductShown = !editing;
    renderProductForm();
    const successMessage = t(editing ? 'productUpdated' : 'productCreated');
    notify(successMessage);
    try {
      if(!editing && data.has('product-select') && productId) {const index = settings.methods.findIndex(value => value.id === method.id);$(`[name="method-${index}-config-tax_category"]`).value = body.tax_category;await selectProduct(productId,store,method.id);}
      await loadProducts();notify(successMessage);
    } catch(error) {notify(`${successMessage} ${error.message}`,true);}
  });
});
$('#product-form').addEventListener('click',event => {if(event.target.closest('#cancel-product')) {editingProduct = null;createdProductShown = false;renderProductForm();}});
$('#product-form').addEventListener('change',event => {
  if(event.target.name !== 'product-currency') return;
  const currency = event.target.value, range = waffoRanges[currency], input = $('[name="product-amount"]');
  try {const amount = amountMinor(input.value,currency);if(amount < range[0] || amount > range[1]) input.value = majorAmount(range[0],currency);} catch {input.value = majorAmount(range[0],currency);}
});
$('#products-list').addEventListener('click',event => {
  const editButton = event.target.closest('[data-edit-product]'),useButton = event.target.closest('[data-use-product]'),deleteButton = event.target.closest('[data-deactivate-product]');
  if(editButton) {editingProduct = catalogProducts.find(value => value.id === editButton.dataset.editProduct);createdProductShown = false;renderProductForm();$('#product-form').scrollIntoView({behavior:window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'instant' : 'smooth',block:'start'});$('[name="product-name"]').focus();}
  if(useButton) busyCatalog(useButton,async () => {const product = catalogProducts.find(value => value.id === useButton.dataset.useProduct), methodId = currentCatalogMethod().id;const currency = Object.keys(product.prices || {})[0];const index = settings.methods.findIndex(value => value.id === methodId);if(product.prices?.[currency]?.tax_category) $(`[name="method-${index}-config-tax_category"]`).value = product.prices[currency].tax_category;await selectProduct(useButton.dataset.useProduct,$('#catalog-store').value,methodId);});
  if(deleteButton && confirm(t('deactivateConfirm'))) busyCatalog(deleteButton,async () => {await idempotentMutation(`/api/admin/waffo/products/${encodeURIComponent(deleteButton.dataset.deactivateProduct)}`,'DELETE',{method_id:currentCatalogMethod().id});notify(t('productDeactivated'));await loadProducts();});
});
window.addEventListener('beforeunload',event => {if(dirty) {event.preventDefault();event.returnValue = '';}});
async function boot() {
  $('#auth-retry').hidden = true;
  const result = await busy(null,async () => {await checkAuth();if(auth.authenticated) await loadAdmin();return true;},$('#auth-error'));
  if(!result) {$('#auth-loading').hidden = true;$('#auth-ready').hidden = false;$('#auth-retry').hidden = false;}
}
$('#auth-retry').addEventListener('click',boot);
translateStatic();
boot();
