# HTTP 与程序接口

运行程序是一个内置网页的 Go 可执行文件，SQLite 文件为 `donate.sqlite`。JSON 错误格式为 `{ "error": "说明" }`。所有金额 `amount_minor` / `total_minor` 均为整数最小货币单位；USD、EUR、GBP、CNY、TWD、HKD 为两位小数，JPY 为整数。不同币种从不直接相加。

## 前台接口

| 请求 | 用途 |
| --- | --- |
| `GET /api/site` | 公开网站文本、语言与币种设置、启用的支付方式、统计和同意公开的感谢记录 |
| `POST /api/donations` | 创建待确认捐赠并返回支付链接或二维码 |
| `GET /api/donations/{id}?token=STATUS_TOKEN` | 以独立访问令牌读取付款状态及待付款的二维码、说明和链接 |
| `GET /api/stats?currency=USD` | 只统计已确认的捐赠，返回选定币种及分币种汇总 |
| `GET /api/private/stats?currency=USD` | 同类聚合数据，要求 `Authorization: Bearer STATS_TOKEN` |
| `GET /healthz` | 应用与数据库健康检查 |
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

返回 `{id,status,checkout_url,qr_url,instructions,status_token}`。未到账为 `pending`，到账为 `confirmed`，全额退款为 `refunded`。`accepted_terms:true` 表示捐赠人已阅读协议和隐私说明，前端在操作附近提供可打开的文本。`status_token` 是访问凭据，不应公开分享状态链接。自定义方式只有管理员核实后才确认；在线方式只有验签回调或服务端 PayPal capture 才确认，网页返回参数不构成付款证明。

`Idempotency-Key` 为 20–80 位 ASCII 字母、数字、下划线或连字符。一次操作重试保持相同请求和 key；不同内容使用原 key 返回 409。结果不明的线上结账超过 5 小时后停止自动创建重试，需要先核对渠道，避免跨渠道幂等缓存期限重复创建。已知结账链接直接恢复。平台的币种、金额等本地限制在入库前检查。

统计返回 `{count,total_minor,currency,by_currency:[{currency,count,total_minor}],methods:[{method_id,method_name,currency,count,total_minor}],daily:[{date,currency,count,total_minor}]}`，同一次响应来自一致的数据库快照。`daily` 为最近 90 天、UTC 日期。线上 `paid_at` 是本站处理支付确认的时间；手动记录可填写实际到账时间。通知同时提供事件 `created_at`。

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
| `POST /api/admin/upload` | multipart `file`，PNG/JPEG/WebP ≤2MB，完整有效图像，16–8192px，≤3200万像素；返回 `{url}` |
| `GET /api/admin/donations?status=&limit=100&offset=0` | `{donations:[...],total}`，status 可空或 pending/confirmed/refunded |
| `POST /api/admin/donations` | `{amount_minor,currency,method_id,method_name,name,email,message,public,paid_at,reference}`，手动确认，支持 Idempotency-Key |
| `POST /api/admin/donations/{id}/confirm` | `{reference,paid_at}`，只确认自定义方式，重复确认不重复记账或通知 |
| `GET /api/admin/export` | 捐赠 CSV，用户输入防止表格公式执行 |
| `GET /api/admin/notifications` | `{notifications:[{id,event_id,kind,status,attempts,last_error,created_at}]}` |
| `POST /api/admin/notifications/{id}/retry` | `{}`，只重试 failed 通知，保留投递 ID |

后台捐赠记录额外包含 `method_type`、`source`、`created_at` 和 `reference`，没有状态访问令牌或完整支付 URL。方法设置为 `{id,type,name,description,enabled,qr_url,checkout_url,config}`。已产生线上订单的方式不能删除或替换商户、环境、凭据、店铺；应添加新的方式并停用旧方式，保留原配置处理回调。未完成创建的 Waffo 结账会保护原产品配置，避免同一重试改变请求内容。

Waffo 商品接口：`GET /api/admin/waffo/stores?method_id=...`、`GET /api/admin/waffo/products?method_id=...&store_id=...`，以及 `POST /api/admin/waffo/products`、`PUT/DELETE /api/admin/waffo/products/{id}`。所有变更要求 Idempotency-Key，SQLite 持久保存请求哈希与结果。PUT 必须包含完整 `prices`。创建或更新已成功、后续发布失败时保存商品 ID 并只恢复剩余步骤；初始结果不明且超过 23 小时不再自动创建。删除按照平台能力停用商品。请求字段和平台限制见 [Waffo 商品文档](WAFFO-CATALOG.md)、[支付文档](PAYMENTS.md)。

Webhook 为 `{url,secret,enabled}`；SMTP 为 `{host,port,username,password,from,to,enabled}`，邮件发送到维护者而非自动发给捐赠人。通知采用数据库事务内队列、最多 8 次重试；后台只返回安全元数据，签名格式见 README。公网 webhook 必须 HTTPS，公网 SMTP 使用 TLS，不接受 URL 重定向到任意地址。私网开发通知由显式 `DONATE_ALLOW_PRIVATE_WEBHOOKS=true` 开启。

## 命令行

`donate [--data-dir PATH] [--addr HOST:PORT] [--public-url ORIGIN] serve|admin password|admin reset|backup PATH|version`。无命令时为 serve。数据库 0600、目录 0700；备份为一致 SQLite 快照且拒绝覆盖已有文件，二维码需另备份。完整安装、可信代理、备份和恢复见 [部署文档](DEPLOYMENT.md)。
