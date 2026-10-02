# Waffo 店铺与商品管理

先在后台保存一个 `waffo` 支付方式。可以保持停用，只填写商户凭据：

```json
{
  "id": "waffo-main",
  "type": "waffo",
  "name": "Waffo",
  "enabled": false,
  "config": {
    "merchant_id": "MER_...",
    "private_key": "-----BEGIN PRIVATE KEY-----\n...",
    "environment": "test"
  }
}
```

`environment` 必须为 `test` 或 `prod`。商户请求的实际环境由 Waffo API 密钥决定；这里只选择与密钥相同的环境，不能用环境选项把测试密钥变成生产密钥。私钥只由已认证的后台保存和服务器使用，目录查询不会返回私钥。

生产审核绑定店铺已验证的产品网站。官方说明 KYB 通过后域名锁定，变更须联系 Waffo 支持解除原绑定，再审核新域名；没有找到同一所有者或同一主域名下其他子域名自动免审的规则。Go SDK 不比较 `successUrl` 与店铺网站的主机名，checkout 文档也只明确要求绝对 HTTPS 返回地址；这不能证明新网站或新业务用途已获原店铺批准。可先完成部署与测试接入，生产启用应以 Waffo 对新域名的确认或审核结果为准。依据：[域名验证](https://docs.waffo.ai/settings/domain-verification)、[业务审核](https://docs.waffo.ai/settings/business-details)。

后台可读取店铺，选定店铺后读取商品。目录读取不要求已选定商品，也不要求支付方式已启用。

| 请求 | 响应 |
| --- | --- |
| `GET /api/admin/waffo/stores?method_id=waffo-main` | `{ "stores": [{ "id", "name", "slug", "status", "prod_enabled" }] }` |
| `GET /api/admin/waffo/products?method_id=waffo-main&store_id=STO_...` | `{ "products": [{ "id", "store_id", "name", "description", "status", "prices", "has_prod_version" }] }` |

商品 `prices` 按币种返回，例如 `{ "USD": { "amount_minor": 500, "tax_category": "digital_goods" } }`。目录使用签名的 merchant GraphQL，只读查询；商品列表包含启用和停用商品，并按每页 100 条读取，最多 10000 条。REST 与 GraphQL 的价格格式不同，服务器负责转换为整数最小货币单位。

商品列表的实际 schema 使用顶层 `onetimeProducts(storeId: $storeId, limit: $limit, offset: $offset)`，`OnetimeProductFilter` 不包含 `storeId`。单个商品查询 `onetimeProduct(id: $id)` 的 `$id` 类型为 `String!`。部分官网例子仍采用旧筛选字段或 `ID!`；实现以实际只读 introspection 与官方 SDK 示例核对后的 schema 为准。

创建商品：

```http
POST /api/admin/waffo/products
Idempotency-Key: 一个全新随机操作标识
X-CSRF-Token: 后台会话的 CSRF token
Content-Type: application/json

{
  "method_id": "waffo-main",
  "store_id": "STO_...",
  "name": "留一点燃料",
  "description": "支持独立创作。",
  "currency": "USD",
  "amount_minor": 500,
  "tax_category": "digital_goods",
  "publish": false
}
```

商品名称最多 64 个字符。金额必须为正整数；JPY 一单位就是一日元，其余站点支持币种使用百分之一单位。当前 Waffo 文档支持以下价格范围：

| 币种 | 最小金额 | 最大金额 |
| --- | ---: | ---: |
| USD | 1.00 | 10000.00 |
| EUR | 1.00 | 9400.00 |
| GBP | 1.00 | 8150.00 |
| HKD | 8.00 | 77600.00 |
| JPY | 100 | 1600000 |
| CNY | 1.00 | 1000.00 |

站点可配置 TWD，但该币种应使用其他支付方式。税类必须由商户明确选择，值为 `digital_goods`、`saas`、`software`、`ebook`、`online_course`、`consulting` 或 `professional_service`，服务器不会推定捐赠税类。

`publish: false` 不调用发布接口。`publish: true` 明确执行首次发布到生产。Waffo 的 publish 接口只允许第一次发布；之后修改生产商品应使用生产 API 密钥更新内容。商品创建/更新的真实环境由 API 密钥决定。

`PUT /api/admin/waffo/products/{id}` 使用同样的完整商品内容，可额外传 `status: "active"` 或 `"inactive"`。HTTP PUT 的 `prices` 必须携带原完整价格表，格式与返回商品一致；`currency`、`amount_minor`、`tax_category` 更新该表里选定的币种，其余币种保留。后台编辑时会携带原价格表，确保相同操作的重试内容稳定。Go 服务层在没有传入 `prices` 时也会先读取原价格，避免丢失其他币种。

服务端 checkout 使用签名的动态金额覆盖，付款金额来自捐款人选定的金额，而不是该基础价格。站点改用其他币种时，应先为选定的 Waffo 商品添加相应币种基础价格。Waffo 文档包含“商品未配置请求币种”的拒绝情况；动态金额不代表自动支持所有币种。

`DELETE /api/admin/waffo/products/{id}` 的 JSON 请求为 `{ "method_id": "waffo-main" }`，实际动作是停用该商品。Waffo 没有商品永久删除接口；已存在的订单不会被删除。

成功变更返回 `{ "product": { ... } }`。失败返回 `{ "error": "..." }`，已确认的中间商品信息保存在服务器，使用原操作标识重试即可继续。`has_prod_version` 仅在远端返回该事实或首次发布成功时出现，不会从测试商品的 `active` 状态猜测生产状态。

每个新操作生成新 `Idempotency-Key`；相同操作的网络重试保留原 key 与完整请求内容。服务器为创建、更新、状态变更、发布分别生成稳定的 Waffo key，避免一次操作中的不同步骤被当成彼此的重试。商品已创建或更新、但后续发布或状态修改失败时，Go 服务返回已确认的商品和错误，供应用持久保存；继续操作只完成剩余步骤；若发布响应丢失，恢复时读取远端实际发布状态，已经成功的首次发布不会重复执行。对已发布商品编辑生产内容使用生产密钥并设置 `publish:false`；测试版本不能靠再次 publish 覆盖生产版本。

Waffo 的远端缓存只有 24 小时。初始创建结果不明、且没有已确认商品标识的操作，应用在 23 小时后停止自动重放；需检查后台店铺/商品并核对结果。应用的 SQLite 记录对已完成操作长期返回原响应，并拒绝将原 key 用于其他请求。

适配器已实现，可先保存停用的测试配置。Waffo 的 [用途规则](https://docs.waffo.ai/mor/prohibited-products) 禁止慈善、政治、宗教组织等用途，[服务条款](https://waffo.com/en/terms) 也禁止没有真实商品或服务交易的收款。本项目的纯自愿开源资助需 Waffo 明确批准；同一所有者、独立开发者准入、API 请求成功或命名为“开源支持”都不能代替用途批准，也不能把捐赠税类改成软件销售来获得批准。

官方资料：[认证](https://docs.waffo.ai/api-reference/authentication)、[GraphQL 格式](https://docs.waffo.ai/api-reference/endpoints/graphql/overview)、[店铺与商品](https://docs.waffo.ai/api-reference/endpoints/graphql/stores-and-products)、[创建商品](https://docs.waffo.ai/api-reference/endpoints/onetime-products/create-product)、[首次发布](https://docs.waffo.ai/api-reference/endpoints/onetime-products/publish-product)、[商品停用](https://docs.waffo.ai/api-reference/endpoints/onetime-products/update-status)、[支付币种](https://docs.waffo.ai/dashboard/payments)、[商户用途边界](https://docs.waffo.ai/mor/prohibited-products)。

实现基于官方 Go SDK v0.16.0。该版本未暴露 `taxIncluded`；目录基础价格由 SDK 发出，checkout 的税包含金额由付款服务使用官方签名 REST 接口设置。所有请求固定为 Waffo 官方域名，不接受配置中的 API host。
