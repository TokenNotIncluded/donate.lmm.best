# 部署与维护

程序为纯 Go 静态二进制，网页内嵌，不依赖宿主机的 glibc、Node.js 或数据库服务。目标架构为 Linux amd64（兼容基线 v1）和 arm64。宿主机仍需 CA 证书，以连接支付平台、Webhook 和 SMTP TLS 服务。

## 选择安装方式

| 系统 | 格式 | 安装示例 |
| --- | --- | --- |
| Debian / Ubuntu | `.deb` | `sudo apt install ./donate_<version>_amd64.deb` |
| Fedora / RHEL 等 | `.rpm` | `sudo dnf install ./donate-<version>.x86_64.rpm` |
| Alpine | `.apk` | `sudo apk add --allow-untrusted ./donate_<version>_x86_64.apk` |
| Arch Linux | `.pkg.tar.zst` | `sudo pacman -U ./donate-<version>-x86_64.pkg.tar.zst` |
| 其他发行版 | `.tar.gz` | 解压后运行 `donate` |

文件名以 `dist/` 或 Release 实际产物为准。下载后先使用同一个 Release 的 `checksums.txt` 校验：

```sh
sha256sum --check --ignore-missing checksums.txt
```

原生包会创建受限的 `donate` 服务用户和 `/var/lib/donate` 数据目录，安装 `/etc/donate/donate.env`。包安装不会自动启动或开放监听端口，也不会打印首次密码。Alpine APK 当前未做发行版信任签名，因此本地包安装需要 `--allow-untrusted`；校验和不能替代发布者身份验证。

如要从源代码构建：

```sh
make cross
make snapshot
```

`make install DESTDIR=/tmp/donate-stage PREFIX=/usr` 仅把二进制暂存到 `/tmp/donate-stage/usr/bin/donate`。它不创建用户、安装服务或改变运行中的系统。便携包可直接运行；若要作为系统服务，使用原生包通常更省事。

## systemd

先编辑 `/etc/donate/donate.env`，把 `DONATE_PUBLIC_URL` 改为最终 HTTPS 来源，保留本机监听供反向代理访问：

```dotenv
DONATE_PUBLIC_URL=https://donate.example.com
DONATE_ADDR=127.0.0.1:8080
DONATE_DATA_DIR=/var/lib/donate
```

启动并读取首次密码：

```sh
sudo systemctl enable --now donate
sudo -u donate /usr/bin/donate --data-dir /var/lib/donate admin password
curl --fail http://127.0.0.1:8080/healthz
sudo systemctl status donate
```

密码命令只在服务器终端输出。服务以 `donate` 用户运行，数据目录权限为 `0700`；systemd 限制其只能写入 `/var/lib/donate` 和隔离的临时目录。若更改数据目录，同时通过 `systemctl edit donate` 调整 `WorkingDirectory`、`ReadWritePaths` 和 `StateDirectory`，并设置正确的目录所有者。

服务日志：

```sh
sudo journalctl -u donate -f
```

## Alpine / OpenRC

配置同一个 `/etc/donate/donate.env`，然后：

```sh
sudo rc-update add donate default
sudo rc-service donate start
sudo -u donate /usr/bin/donate --data-dir /var/lib/donate admin password
```

OpenRC 服务使用 `supervise-daemon`，以 `donate` 用户运行。不使用 systemd 或 OpenRC 的发行版可由自己的进程管理器运行 `/usr/bin/donate serve`，设置三个环境变量和受限用户即可。

## HTTPS 与域名

Passkey 绑定到来源与域名。正式环境必须使用 HTTPS，并在首次绑定前设置最终 `DONATE_PUBLIC_URL`；`localhost` HTTP 只用于本地开发。

Caddy 示例（把域名改成自己的）：

```caddyfile
donate.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

Nginx 则将 HTTPS 站点反向代理到 `http://127.0.0.1:8080`，保留原始 `Host`。单层代理需覆盖客户端带来的转发头：

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto https;
    proxy_set_header X-Forwarded-For $remote_addr;
}
```

`DONATE_TRUSTED_PROXIES` 默认仅信任本机回环地址，逗号分隔 CIDR，设置 `none` 可禁用。Docker 或多层代理需填实际代理地址范围，且代理必须覆盖或追加真实来访地址；服务从右往左读取链，忽略非可信直接连接的转发头。不要填写整个公网范围。支付平台回调也需要能访问公网 HTTPS 站点。不要缓存 `/api/`、`/admin` 或支付回调。服务的 `DONATE_PUBLIC_URL` 是固定可信来源，不从任意请求头决定 Passkey 来源。

## Docker Compose

在项目目录放置 `.env`：

```dotenv
DONATE_PUBLIC_URL=https://donate.example.com
DONATE_HOST_PORT=8080
DONATE_VERSION=dev
```

```sh
docker compose up -d --build
docker compose exec donate donate admin password
docker compose ps
```

命名卷第一次挂载时使用镜像内 `/var/lib/donate` 的所有者 UID/GID `10001`。若改成宿主机目录挂载，先创建目录并显式设置 `10001:10001` 所有者和 `0700` 权限；否则非 root 容器不能写入。不要把数据库目录挂载给其他同时写入的实例。SQLite 模式下运行一个应用实例。

## 丢失 Passkey

首次绑定后，密码不能再登录。所有 Passkey 丢失时，需要服务器命令行权限。重置清除全部 Passkey 和会话，保留配置、捐款和上传文件。

systemd：

```sh
sudo systemctl stop donate
sudo -u donate /usr/bin/donate --data-dir /var/lib/donate admin reset
sudo systemctl start donate
```

Docker Compose：

```sh
docker compose stop donate
docker compose run --rm --no-deps donate admin reset
docker compose up -d donate
```

在网站用命令行输出的新密码登录并绑定新 Passkey。重置属于管理凭据的不可逆变更；如仍有可用 Passkey，应先登录后台添加另一把。不要将首次密码、数据目录备份或统计令牌发布到公共日志。

## 备份与恢复

不要在运行时直接复制 SQLite 主文件：WAL 模式下，最近的记录可能还在单独的 WAL 文件中。`donate backup <path>` 通过 SQLite 一致性快照导出数据库，输出路径必须不存在。

```sh
sudo -u donate /usr/bin/donate --data-dir /var/lib/donate backup /var/lib/donate/backup-2026-10-03.sqlite
```

数据库文件名为 `donate.sqlite`。数据库备份包含站点配置、支付密钥、认证状态和捐款人信息，按敏感文件保管。上传的二维码文件，以及首次绑定前存在的 `bootstrap-password` 首次密码文件位于数据目录中，需要另行备份；完整恢复最稳妥的方式是停机后保存整个数据目录和 `/etc/donate/donate.env`。例如：

```sh
sudo systemctl stop donate
sudo tar -czf /root/donate-backup-2026-10-03.tar.gz /var/lib/donate /etc/donate
sudo chmod 0600 /root/donate-backup-2026-10-03.tar.gz
sudo systemctl start donate
```

恢复前停止服务，先保留当前数据目录，再把备份中的完整目录恢复到相同位置，保持文件所有者和权限。只恢复数据库快照时，用它替换 `donate.sqlite`，清除旧数据库对应的 `donate.sqlite-wal` / `donate.sqlite-shm` 文件，并恢复与该快照匹配的上传文件和首次密码文件；这些操作必须在服务停止时进行。恢复原域名后检查 `healthz`、Passkey 登录、支付配置和统计。旧备份可能重发备份后已交付的通知，接收端应使用事件 ID 去重。

Docker 完整数据卷备份也应停机：

```sh
docker compose stop donate
docker compose run --rm --no-deps --entrypoint tar donate -czf /var/lib/donate/full-backup.tar.gz -C /var/lib/donate --exclude=./full-backup.tar.gz .
docker compose cp donate:/var/lib/donate/full-backup.tar.gz ./donate-full-backup.tar.gz
chmod 0600 ./donate-full-backup.tar.gz
docker compose up -d donate
```

备份文件仍存在数据卷里，转移到安全存储并确认后删除该临时文件，避免占用长期空间。恢复时使用相同的数据卷、域名和应用版本，先停机，保留当前卷，然后还原完整数据和权限。

## 升级与卸载

升级前做一致性备份，保存旧版本产物和配置。安装新原生包后手动重启：

```sh
sudo systemctl restart donate
curl --fail http://127.0.0.1:8080/healthz
```

Compose 使用 `docker compose up -d --build`，保留数据卷。数据结构有变化时，旧程序不一定能读取新数据库；回滚应同时恢复升级前的数据快照，而非只替换程序。卸载包保留数据和服务用户，不自动删除捐款记录。

正式启用前还应实际完成一次测试环境收款、支付平台回调、二维码手动确认、Webhook 失败重试和 Passkey 恢复。程序构建成功或 `/healthz` 返回正常，只证明相应环节可用，不能证明真实支付已到账。
