# HTTP 与程序接口

运行程序是一个内置网页的 Go 可执行文件，SQLite 文件为 `donate.sqlite`。JSON 错误格式为 `{ "error": "说明" }`。所有金额 `amount_minor` / `total_minor` 均为整数最小货币单位；USD、EUR、GBP、CNY、TWD、HKD 为两位小数，JPY 为整数。不同币种从不直接相加。

## 前台接口

| 请求 | 用途 |
| --- | --- |
| `GET /api/site` | 公开网站文本、语言与币种设置、启用的支付方式、统计和同意公开的感谢记录 |
| `GET /terms`、`GET /privacy` | 独立政策页面，`?lang=en` 等选择已配置的语言；直接读取后台文本 |
| `POST /api/donations` | 创建待确认捐赠并返回支付链接或二维码 |
| `GET /api/projects` | 公开启用项目及筹款进度，返回 `{projects:[...]}` |
| `GET /api/projects/{id}` | 单个启用项目，未知或归档项目返回 404 |
| `GET /api/donations/{id}?token=STATUS_TOKEN` | 以独立访问令牌读取付款状态及待付款的二维码、说明和链接 |
| `POST /api/donations/{id}/cancel` | 以 JSON `{status_token}` 取消未完成的付款等待；要求本站 Origin |
| `GET /api/stats?currency=USD` | 只统计已确认的捐赠，返回选定币种及分币种汇总 |
| `GET /api/private/stats?currency=USD` | 同类聚合数据，要求 `Authorization: Bearer STATS_TOKEN` |
| `GET /badge.svg?currency=USD&lang=en&period=30d` | 可公开嵌入的 SVG 捐款总额与笔数，不包含捐赠者资料 |
| `GET /healthz` | 应用与数据库健康检查 |
| `GET /api/source` | 源码 URL、真实 GitHub Star 数或 null、抓取时间与缓存状态 |
| `POST /api/webhooks/{provider}?method_id=METHOD_ID` | 支付平台验签回调，provider 为 waffo、stripe 或 paypal |
| `GET /api/paypal/return?token=ORDER_ID&donation=ID&status_token=STATUS_TOKEN` | PayPal 返回后在服务器完成 capture，再跳转到状态页 |

公开网站配置包含 `name`、`tagline`、`description`、`footer`、`currency`、`presets`、`collect_name`、`collect_email`、`collect_message`、`contact_email`、`languages`、`default_language`、`currencies`、`language_currencies`、`terms`、`privacy`、`translations`。`presets` 是以主要单位表示的整数，默认 `[5,15,50,100]`，不是最小货币单位。`translations` 为语言到 `{tagline,description,footer,terms,privacy}` 的映射。

默认语言包含 `zh-CN`、`zh-TW`、`en`，对应默认币种 CNY、TWD、USD。语言按保存的选择、浏览器语言、站点默认依次确定；切换语言时使用后台对应币种配置，访客可单独改币种。

`methods` 只有 `{id,type,name,description,qr_url,checkout_url}`，不包含商户凭据。`recent` 只有同意公开的已确认记录 `{name,amount_minor,currency,method,paid_at,message}`，永不包含邮箱。

捐赠请求示例（先在后台创建并启用 `custom-main`）：

```sh
curl http://localhost:8080/api/donations \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: 25ed2b75-18cf-46f0-ace6-f471ae3ae2fc' \
  --data '{"amount_minor":1500,"currency":"USD","method_id":"custom-main","name":"","email":"","message":"","public":false,"accepted_terms":true}'
```

返回 `{id,status,checkout_url,qr_url,instructions,status_token,expires_at,can_cancel}`。未到账为 `pending`，到账为 `confirmed`，全额退款为 `refunded`，主动取消为 `cancelled`，线上等待超时为 `expired`。`accepted_terms:true` 表示捐赠人已阅读协议和隐私说明，前端在操作附近提供可打开的文本。`status_token` 是访问凭据，不应公开分享状态链接。自定义方式只有管理员核实后才确认；在线方式只有验签回调、服务端 PayPal capture 或服务器核验链上到账才确认，网页返回参数不构成付款证明。

托管支付待付款记录默认 45 分钟后过期，后台定时处理，读取状态时也检查期限；重启不会延长期限。自定义二维码和线下记录不自动过期。取消需原状态令牌；已登录捐赠者还需自己的 `X-CSRF-Token`，已关联账号的记录须匹配该账号。取消重复请求安全，已到账或退款返回 409，不执行退款。托管支付取消或过期后不再返回继续付款的入口，同一幂等请求不会复活该订单。支付渠道已提交的付款可能晚到，通过真实验签的成功仍会入账、统计并只通知一次。

支付返回页只在重新读取服务器的 `confirmed` 状态后显示感谢页及金额；刷新仍重新核实。前端把状态令牌保存在当前标签页的 `sessionStorage` 中，并从可分享的网址移除，不缓存付款成功状态。

`Idempotency-Key` 为 20–80 位 ASCII 字母、数字、下划线或连字符。一次操作重试保持相同请求和 key；不同内容使用原 key 返回 409。结果不明的线上结账超过 5 小时后停止自动创建重试，需要先核对渠道，避免跨渠道幂等缓存期限重复创建。已知结账链接直接恢复。平台的币种、金额等本地限制在入库前检查。

统计返回 `{count,total_minor,currency,by_currency:[{currency,count,total_minor}],methods:[{method_id,method_name,currency,count,total_minor}],daily:[{date,currency,count,total_minor}]}`，同一次响应来自一致的数据库快照。`daily` 为最近 90 天、UTC 日期。线上 `paid_at` 是本站处理支付确认的时间；手动记录可填写实际到账时间。通知同时提供事件 `created_at`。

`/badge.svg` 只汇总选定币种的 `confirmed` 记录，无需登录或统计令牌。`period=all|7d|30d|year`，7/30 天按 UTC 当前时刻滚动，`year` 从 UTC 当年 1 月 1 日起算，未来到账时间不计入。`lang=zh-CN|zh-TW|en`，`layout=receipt|compact`，`theme=dark|light|transparent`，`width=240..1200`；默认 `all`、`en`、`receipt`、`dark`、宽度 480（紧凑版 440）。`currency` 默认站点币种。`title` 最多 40 字，`amount_label`、`count_label` 最多 24 字，均为单行纯文本；`animation=none|steam`，默认无动画。非法参数返回 400。SVG 使用 `Cache-Control: public, max-age=300` 和内容 ETag，支持 HEAD 与条件请求；外部图片代理可能有额外缓存。

## 链上收款

`/api/site` 的 `crypto_options` 只公开已启用的 `{network,name,family,asset,label}`，不包含节点凭据。创建 `crypto` 订单要求 USD，以及 `crypto_network`、`crypto_asset`。返回额外的 `crypto` 收据，冻结该订单的地址、合约、精度、容差与确认策略；其 `state` 与捐赠的总体 `status` 分开。应付与到账的原子单位为十进制字符串，避免浮点精度损失。已确认统计记录实际到账折算成 USD 分；原应付额与完整链上精度保留在收据。

| 请求 | 用途 |
| --- | --- |
| `POST /api/donations/{id}/transactions` | JSON `{status_token,network,tx_id}`；核验交易并返回收据 |
| `POST /api/donations/{id}/recheck` | JSON `{status_token}`；重新核验已提交的等待确认交易 |
| `GET /api/donations/{id}/events?token=STATUS_TOKEN` | SSE `receipt` 事件；重连先发送当前权威收据 |
| `GET /api/donations/{id}/qr?token=STATUS_TOKEN` | 本地生成付款请求 PNG；`address=1` 生成纯地址二维码 |
| `POST /api/donations/{id}/wallet` | JSON `{status_token,account}`；提供 Solana 最近 blockhash，不代签或广播 |
| `POST /api/crypto/events` | 独立签名的链事件提示；只能唤醒核验，不能直接认定到账 |
| `POST /api/admin/crypto/check` | 管理员 JSON `{network}`；检查主网身份、代币合约与精度 |
| `GET /api/admin/crypto/transactions/{id}` | 管理员查看链上收据与核验结果 |

写请求要求本站 Origin 和原收据令牌；管理接口另需管理员会话与 CSRF。一笔实际链上的交易只能认领一次，补款累加最终确认的净到账，未达到最低金额不计成功。链上转账没有本地取消或自动退款。默认发起期限 30 分钟，之后最多再核验 24 小时；晚到账策略、容差和网络可配置。节点中断显示等待核验原因，不虚报失败或成功。签名、事件重试、钱包兼容性与完整配置见 [CRYPTO.md](CRYPTO.md)。

## 项目与公开致谢

后台创建项目使用 `{id,name,url,currency,target_minor,active}`，`id` 为 1–64 位小写字母、数字或中间连字符，创建后固定。`target_minor` 为正整数最小货币单位，例如 CNY 100000 是 ¥1,000。项目链接可空，否则要求 HTTPS。项目返回额外的 `{raised_minor,count,progress,donate_url,badge_url,created_at,updated_at}`；`progress` 为 0–100 的百分比，超出目标的实际已筹金额不会截断。目标进度只累计该项目、目标币种、已经确认且到账时间不在未来的记录，退款不计入。`by_currency` 分币种提供 `{currency,total_minor,count}`；其他币种单独展示，不自动折算或混加。

捐赠请求和后台手工录入可增加 `project_id`，为空表示通用捐赠。非空时必须是启用项目且币种有效，否则拒绝创建；旧记录不自动归属项目。已创建记录不能改项目。付款状态、后台记录、CSV 和私人到账通知包含项目标识；项目改名不改变捐款归属。

`/?project=magicnet` 显示该项目进度，默认选中目标币种；捐赠者可选择站点启用的其他币种，稳定币支付仍以 USD 计价。`/badge.svg?project=magicnet` 展示其已筹金额、目标及进度，使用全部时间与项目币种；显式传入不一致币种或非 `all` 周期返回 400。未知或归档项目返回 404。SVG 不含个人信息；在 README 外层使用项目 `donate_url` 作为点击链接。

新增 `public_thanks` 是每笔捐赠独立的布尔许可，默认 `false`，允许维护者在专门制作的致谢网页或小游戏中展示昵称，未填写昵称则按匿名处理。它与原 `public`（本站公开称呼及留言）互不推断，旧记录不会自动获得新用途许可。后台可核对、筛选授权，待付款记录不作为已完成捐赠致谢；邮箱始终不公开。

公告使用 `site.announcement`、可选 `site.announcement_url`，以及 `site.translations[语言].announcement`。文本为纯文本，最多 2000 字节；链接须为不含用户凭据的绝对 HTTPS 地址，最多 2048 字节。当前语言的公告文本为空时不显示，占位不留空白；有链接但全部公告文本为空时拒绝保存。

## 捐赠者账号

账号可选，访客捐赠不需要登录。用户直接创建 Passkey，之后使用 Passkey 登录；没有用户密码入口，也不授予管理员权限。

| 请求 | 用途 |
| --- | --- |
| `GET /api/donor/session` | 用户登录状态、当前账号及会话 CSRF token |
| `POST /api/donor/register/begin` | 新账号的 Passkey 注册参数；已登录时添加备用 Passkey |
| `POST /api/donor/register/finish` | 验证注册结果并建立用户会话 |
| `POST /api/donor/login/begin` | 可发现 Passkey 的登录参数 |
| `POST /api/donor/login/finish` | 验证签名并建立用户会话 |
| `POST /api/donor/logout` | 撤销当前用户会话 |
| `GET /api/donor/donations?limit=20&offset=0` | 当前账号的捐赠历史，返回 `{donations,total,limit,offset}` |

用户 Cookie 与后台 Cookie 独立，采用 HttpOnly、SameSite Strict；HTTPS 下为 Secure。注册和登录挑战有期限、绑定发起浏览器且只能消费一次，要求设备用户验证和精确 Origin。已登录用户的写请求使用该用户会话的 `X-CSRF-Token`。

首页登录也接受当前有效的管理员 Passkey。验签仍使用后台保存的权威凭证及签名计数，只建立普通用户会话，并将管理员关联到固定的用户 ID；不会签发后台 Cookie，也不会把用户 Passkey 加入管理员凭证集合。管理员命令行重置后旧凭证失效，关联账号及捐赠历史保留；该账号另外绑定的用户备用 Passkey 仍按普通用户凭证验证。

`POST /api/donations` 仅由服务器从当前会话取得账号 ID。客户端不能指定捐款归属；匿名记录不会因相同姓名或邮箱自动归入账号。身份参与幂等请求核对，换账号后不能复用原操作取得其他人的付款状态凭据。用户历史不返回状态 token，不公开账号标识或其他用户的私人信息。后台记录额外提供 `donor_user_id`，空字符串表示访客；这是固定身份标识，不代表已授予任何权益。已登录捐款的私人通知也包含 `donor_user_id`，访客通知省略该字段。

## 后台登录

首页 logo 在 3 秒内激活 5 次进入 `/admin`。隐藏入口只是界面选择，全部管理接口仍要求认证。

| 请求 | 内容 |
| --- | --- |
| `GET /api/auth/status` | `{initialized,authenticated,needs_passkey,csrf_token,passkey_count}` |
| `POST /api/auth/password` | `{password}`，只签发能绑定 Passkey 的首次登录会话 |
| `POST /api/auth/register/begin` | `{}`，返回 `{publicKey:...}` 注册参数 |
| `POST /api/auth/register/finish` | 序列化 WebAuthn 注册响应 |
| `POST /api/auth/login/begin` | `{}`，返回 `{publicKey:...}` 登录参数 |
| `POST /api/auth/login/finish` | 序列化 WebAuthn 签名响应 |
| `POST /api/auth/logout` | `{}` |

密码只通过 CLI 获取或重置。绑定后删除密码文件并关闭密码登录；只有重置才能重新启用首次登录。密码会话 `authenticated:false,needs_passkey:true`，没有管理权限。设备验证必需，挑战有期限且只能消费一次，重置撤销所有旧凭据、会话和挑战。Cookie 为 HttpOnly、SameSite Strict，HTTPS 时为 Secure。注册、退出及后台写操作使用 `X-CSRF-Token`，与配置的精确 Origin 匹配。密码和 Passkey 登录入口也检查来源。

## 后台管理

| 请求 | 内容 / 返回 |
| --- | --- |
| `GET /api/admin/settings` | `{site,methods,webhook,smtp,stats_token}`，仅认证管理员可读取凭据 |
| `PUT /api/admin/settings` | 完整设置对象；返回 `{saved:true}` |
| `GET /api/admin/projects` | 全部项目，包括已归档项目及各自进度 |
| `POST /api/admin/projects` | 创建项目，要求 Idempotency-Key，相同请求重试返回原结果 |
| `PUT /api/admin/projects/{id}` | 编辑名称、链接、目标和状态；币种有捐款后不能修改，ID 固定 |
| `POST /api/admin/upload` | multipart `file`，PNG/JPEG/WebP ≤2MB，完整有效图像，16–8192px，≤3200万像素；返回 `{url}` |
| `GET /api/admin/donations?status=&limit=100&offset=0` | `{donations:[...],total}`；状态可空或 pending/confirmed/refunded/cancelled/expired，可加 `project_id` 与 `public_thanks=true\|false` 筛选 |
| `POST /api/admin/donations` | `{amount_minor,currency,method_id,method_name,name,email,message,public,public_thanks,project_id,paid_at,reference}`，手动确认，支持 Idempotency-Key；公开致谢许可默认 false，需实际获得捐赠者授权 |
| `POST /api/admin/donations/{id}/confirm` | `{reference,paid_at}`，只确认自定义方式，重复确认不重复记账或通知 |
| `GET /api/admin/export` | 捐赠 CSV，用户输入防止表格公式执行 |
| `GET /api/admin/notifications` | `{notifications:[{id,event_id,kind,status,attempts,last_error,created_at}]}` |
| `POST /api/admin/notifications/{id}/retry` | `{}`，只重试 failed 通知，保留投递 ID |

后台捐赠记录额外包含 `method_type`、`source`、`created_at` 和 `reference`，没有状态访问令牌或完整支付 URL。方法设置为 `{id,type,name,description,enabled,qr_url,checkout_url,config}`。已产生线上订单的方式不能删除或替换商户、环境、凭据、店铺；应添加新的方式并停用旧方式，保留原配置处理回调。未完成创建的 Waffo 结账会保护原产品配置，避免同一重试改变请求内容。

Waffo 商品接口：`GET /api/admin/waffo/stores?method_id=...`、`GET /api/admin/waffo/products?method_id=...&store_id=...`，以及 `POST /api/admin/waffo/products`、`PUT/DELETE /api/admin/waffo/products/{id}`。所有变更要求 Idempotency-Key，SQLite 持久保存请求哈希与结果。PUT 必须包含完整 `prices`。创建或更新已成功、后续发布失败时保存商品 ID 并只恢复剩余步骤；初始结果不明且超过 23 小时不再自动创建。删除按照平台能力停用商品。请求字段和平台限制见 [Waffo 商品文档](WAFFO-CATALOG.md)、[支付文档](PAYMENTS.md)。

Webhook 为 `{url,secret,enabled}`；SMTP 为 `{host,port,username,password,from,to,enabled}`，邮件发送到维护者而非自动发给捐赠人。通知采用数据库事务内队列、最多 8 次重试；后台只返回安全元数据，签名格式见 README。公网 webhook 必须 HTTPS，公网 SMTP 使用 TLS，不接受 URL 重定向到任意地址。私网开发通知由显式 `DONATE_ALLOW_PRIVATE_WEBHOOKS=true` 开启。

## 命令行

`donate [--data-dir PATH] [--addr HOST:PORT] [--public-url ORIGIN] serve|admin password|admin reset|backup PATH|version`。无命令时为 serve。数据库 0600、目录 0700；备份为一致 SQLite 快照且拒绝覆盖已有文件，二维码需另备份。完整安装、可信代理、备份和恢复见 [部署文档](DEPLOYMENT.md)。
