# Donate

自托管开源项目捐赠站。黑白界面、动态 ASCII 咖啡 logo，支持金额输入、预设金额和随机金额；首页没有后台导航。

Go 单文件程序内嵌网页，SQLite 保存配置、捐款和通知队列。不需要 Node.js、外部数据库或 C 运行库。支持 Linux amd64 / arm64；提供标准 `.deb`、`.rpm`、`.apk`、Arch Linux 包和便携压缩包。

[![Donate](https://donate.lmm.best/badge.svg?project=donate&currency=CNY&lang=zh-CN&period=all&layout=compact&theme=dark&width=360&title=Donate)](https://donate.lmm.best/?project=donate)

支持可配置的 USDT / USDC / USDG 链上收款：钱包带金额支付、本站二维码、手动交易 ID 核验、少付容差、补款、实时到账状态与签名 Webhook。主页提供 GitHub 源码链接和缓存的真实 Star 数。详见 [链上收款配置](docs/CRYPTO.md)。

## 本地运行

需要 `go.mod` 指定的 Go 版本和 Make。

```sh
make build
./build/donate serve
```

打开 <http://localhost:8080>。本地使用 `localhost`，不要把它随意改成局域网 HTTP 地址：Passkey 需要安全来源，正式环境应使用 HTTPS。

在另一个终端取首次登录密码：

```sh
./build/donate admin password
```

3 秒内连续点击首页 logo 5 次进入后台。也可用键盘聚焦 logo 后连续激活 5 次。首次密码登录只能绑定 Passkey；绑定后密码登录永久关闭，后续使用 Passkey。

Passkey 全部丢失时，停止服务并通过命令行重置：

```sh
./build/donate admin reset
```

重置会清除所有 Passkey 和登录会话，生成新的首次登录密码；捐款记录和站点配置保留。重新启动后用新密码绑定新的 Passkey。密码不会写进服务日志，也不能从网站获取。

## 配置

运行环境只负责地址和数据目录，其余内容在后台配置。

| 环境变量 | 默认值 | 用途 |
| --- | --- | --- |
| `DONATE_ADDR` | `127.0.0.1:8080` | 监听地址 |
| `DONATE_DATA_DIR` | `./data` | SQLite、上传文件和认证状态 |
| `DONATE_PUBLIC_URL` | `http://localhost:8080` | 网站实际对外来源；生产环境必须 HTTPS |
| `DONATE_TRUSTED_PROXIES` | `127.0.0.1/32,::1/128` | 可解释 X-Forwarded-For 的代理 CIDR；`none` 禁用 |
| `DONATE_ALLOW_PRIVATE_WEBHOOKS` | `false` | 仅开发时显式允许私网通知目的地 |

也可把 `--data-dir` 写在子命令前：

```sh
./build/donate --data-dir /path/to/data admin password
```

后台可调整站名、文案、金额、币种、语言、服务条款和隐私说明；可选收集捐款人姓名、邮箱和留言。默认支持简体中文、繁体中文和英文。捐款人的邮箱不会出现在公开捐款记录中；公开展示姓名和留言需要捐款人同意。

捐赠者可选择用 Passkey 创建账号或登录，不登录也能捐。登录后的捐款关联到固定账号 ID，用户可以查看自己的记录，并添加备用 Passkey；用户账号没有后台权限。访客捐款不会仅凭相同姓名或邮箱自动归入账号。账号 ID 可供以后扩展小游戏访问或其他福利，当前不承诺或发放权益。

管理员也可用已有的后台 Passkey 在首页登录，捐款归入固定的个人账号；首页登录只建立捐赠者会话。后台仍须单独验证管理员凭证，普通用户及其备用 Passkey 无法进入后台。命令行重置管理员登录后，旧管理员 Passkey 不能再用于登录，新管理员 Passkey 可继续使用原有的捐赠记录。

## 收款

提供 Waffo Pancake、Stripe、PayPal 连接，以及自定义付款链接和上传捐赠二维码。支付平台的商户账号、密钥和回调设置需要自行配置；连接参数见 [支付配置](docs/PAYMENTS.md)。Waffo 的店铺选择和商品管理见 [Waffo 商品配置](docs/WAFFO-CATALOG.md)。

Waffo 的 [商户用途规则](https://docs.waffo.ai/mor/prohibited-products) 禁止慈善、政治、宗教组织等用途，[服务条款](https://waffo.com/en/terms) 也禁止没有真实商品或服务交易的收款。本项目的纯自愿开源资助需 Waffo 明确批准；同一所有者、独立开发者准入或 API 请求成功都不能代替用途批准。支付适配器已实现，可先保存停用的测试配置，批准后再启用生产收款。Waffo 不支持 TWD，站点可以保留 TWD，但应搭配支持该币种的其他方式。

支付平台返回的页面不能证明到账。在线支付仅在通过验证的回调或服务端确认后记为已支付。二维码或自定义链接付款会保留为待确认，核实收款后在后台确认；也可手动录入线下捐款。统计只计算已确认捐款，按币种分别汇总。

到账后显示感谢页。付款等待中可以取消；线上未完成订单默认 45 分钟后过期，重启后继续按原期限处理。取消不会退款，已提交且随后确认的真实付款仍会入账。二维码和线下记录不自动过期。

后台可创建筹款项目、设置币种与目标金额。每个项目有独立捐款链接和进度 SVG。目标币种不限制捐赠币种，捐赠者可选择站点启用的币种或 USD 计价的稳定币支付；各币种已确认、未退款的捐款分别统计，目标进度只计算目标币种，其他币种不自动折算。项目可以归档和恢复，历史记录保留。

捐赠人可以另外勾选「允许公开致谢」，默认不勾选，允许维护者在致谢网页或小游戏中展示昵称，邮箱不公开。该许可与本站公开称呼、留言的选项分别记录。后台也可填写公告文字和链接，留空时首页不显示。

可配置 SMTP 邮件和 Webhook 通知。已确认捐款会产生持久化通知，包含捐款人信息、时间、支付方式、金额和币种；SMTP 支持 465 隐式 TLS 和其他端口的 STARTTLS；Webhook 使用 HMAC 签名并重试失败请求，后台可查看发送状态和手动重试。接收端应按事件 ID 去重。金额字段使用整数最小货币单位，例如 USD 500 表示 5 美元，JPY 500 表示 500 日元。

Webhook 的 `X-Donate-Signature` 为 `sha256=` 加十六进制 HMAC-SHA256，签名对象是未经修改的请求原始正文，密钥为后台配置的 Webhook secret。`X-Donate-Event` 标记事件类型，`X-Donate-Delivery` 标记通知投递 ID。正文包含 `id`、`type`、`created_at` 和 `donation`；后者包含金额、币种、支付方式、捐款人可选信息、到账时间及来源。先验证签名，再解析正文并按正文 `id` 去重。

## 统计接口

```sh
curl http://localhost:8080/api/stats
curl -H 'Authorization: Bearer YOUR_STATS_TOKEN' http://localhost:8080/api/private/stats
```

`/api/stats` 提供公开聚合统计；`/api/private/stats` 使用后台配置的统计令牌，不返回捐款人个人信息。后台另有捐款列表、筛选、手动录入和 CSV 导出。详细接口见 [接口约定](docs/CONTRACT.md)。

后台「公开徽章」可预览捐赠 SVG，复制 GitHub README、HTML 或图片地址。支持黑色、白色和透明背景，收据或横条版式、语言、币种、统计周期和短标题；图片只包含已确认捐款的总额与笔数，不包含捐赠者资料，各币种分别统计。选择项目后会展示已筹金额、目标与进度，使用项目币种及全部时间，点击链接进入对应项目的捐款页。

```markdown
[![Donate](https://donate.example.com/badge.svg?currency=USD&lang=en&period=30d&layout=compact)](https://donate.example.com/)
```

公开图片地址 `/badge.svg` 无需令牌，缓存 5 分钟；GitHub 可能另有图片缓存。`period` 为 `all`、`7d`、`30d` 或 `year`，后者从 UTC 当年 1 月 1 日起算。`layout` 为 `receipt` 或 `compact`，`theme` 为 `dark`、`light` 或 `transparent`，`width` 为 240–1200。`lang` 支持 `zh-CN`、`zh-TW` 和 `en`；`title`、`amount_label`、`count_label` 可分别自定义最多 40、24、24 字的单行文字。

## Docker

```sh
docker compose up -d --build
docker compose exec donate donate admin password
```

Compose 默认只向本机暴露 8080 端口。持久化命名卷由 UID/GID `10001` 的非 root 服务用户使用，容器根文件系统只读。

正式环境在 `.env` 配置最终域名，并通过 Caddy、Nginx 等提供 HTTPS：

```dotenv
DONATE_PUBLIC_URL=https://donate.example.com
```

绑定 Passkey 前确定域名。迁移域名时，原 Passkey 可能无法在新域名登录，需要命令行重置并重新绑定。不要用 `docker compose down -v` 升级，那会删除数据卷。

## 构建与发布

```sh
make check          # go vet 与 race 测试
make cross          # 不依赖 CGO 的 Linux amd64 / arm64 程序
make snapshot       # GoReleaser 2.18.2：本地产生压缩包、原生包和 SHA-256 校验和
```

浏览器验收另外使用 Node.js 24、Playwright 和 Python 3（临时 SQLite 场景），测试在临时数据目录启动应用，覆盖 Passkey、捐赠、后台和窄屏交互，不连接真实支付商户：

```sh
npm ci
npx playwright install chromium
npm run test:browser
```

`make snapshot` 需要 GoReleaser，不会安装软件或发布远程版本。带版本标签的 CI 生成 GitHub 草稿 Release，审核后再公开。Actions 和构建镜像固定到版本/提交摘要；发布文件包含校验和。

安装、systemd / OpenRC、反向代理、备份和恢复步骤见 [部署文档](docs/DEPLOYMENT.md)。MIT 开源。配置和脚本准备完成不代表已经部署，也不代表支付平台真实收款已经验证。
