# CFSM Monitor · Go Docker 看板

独立的只读 Web 看板，从现有 [CF-Server-Monitor](https://github.com/huilang-me/CF-Server-Monitor) 批量采集数据。Go 后端内嵌前端，不依赖 Home Assistant，也不需要 Node.js 运行环境。

版本号见 [VERSION](VERSION)，更新记录见 [CHANGELOG.md](CHANGELOG.md)。本项目采用 [MIT License](LICENSE)，第三方许可见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。

- **访客**直接查看 CFSM 中没有隐藏的节点。
- **管理员**使用此 Docker 的独立账号登录，查看全部节点并设置数据源。
- 隐藏规则沿用 CFSM 的 `is_hidden`，由 Go 后端过滤；公开接口、统计和图表都不会包含隐藏节点。缺少或无法识别隐藏标记的节点仅管理员可见。
- 主页面显示节点流量和按日期排列的每日明细表。节点流量右侧可切换卡片和列表，浏览器记住选择。列表逐行显示今日、昨日、周期已用、剩余、总配额、使用率、重置日期与状态，联动原有筛选和排序。保留浅色配色，不展示 CPU、内存、磁盘和实时网速。
- 点击卡片、节点列表行或每日明细的节点名称，弹出该节点的流量图，支持 7/14/30 天切换和点击日期。弹窗不会修改主页面筛选，可通过关闭按钮、Escape 或点击弹窗外关闭。节点消失、数据不可用或权限变化后不可见时，关闭弹窗并清除内容。
- 节点流量和每日明细均可单独收起或展开，浏览器记住折叠状态。收起后保留标题和展开按钮。
- 卡片显示周期已用/总配额、剩余、今日、昨日、近 7 日均和重置日期；每日表默认 7 天，可展开 30 天。用量突增标红，部分记录标 `*`，缺样保留 `—`。
- 默认 300 秒一次批量采集，所有浏览器共用缓存。浏览器每 30 秒读取本地缓存，不额外请求 CFSM。
- 独立后台 `/admin` 提供数据源设置、采集状态与立即采集、历史查询与清理、管理员账号修改。配置和每日记录存入 SQLite，App 口令不回显、不传给访客。

视觉样式参考 [LuminaPlus v1.2.24](https://github.com/volcano-1025/CFSM-Theme-LuminaPlus)：使用其浅色配色、卡片表面、按钮、标签与表格风格；配额条采用同一组 OKLCH 色标和 18 段用量规则，用量数字采用相同的绿色→琥珀→红色变化。柱状图保持实心。此处固定参考版本，不随原 CFSM 的主题设置自动变化。相关许可见 [LuminaPlus.LICENSE](third-party/LuminaPlus.LICENSE)。

## Docker 启动

```sh
docker compose up -d --build
```

无需预先配置账号密码。需要调整端口或绑定地址时，可复制 `.env.example` 为 `.env` 后修改运行参数。

默认访问 `http://127.0.0.1:8080`。如需局域网访问，在 `.env` 设置 `BIND_ADDRESS=0.0.0.0`；使用 `PORT` 调整宿主机端口。

1. 首次打开网页，进入「创建管理员」，自行填写账号、密码（至少 6 位）及确认密码，创建后自动登录。
2. 打开「后台管理 → 数据源设置」，填 CFSM 根地址，例如 `https://status.example.com`。也可以直接访问 `/admin` 登录。
3. 输入 CFSM「设置 → 安全设置 → App 访问口令」中的 **16–32 位 App 口令**。这里不是 Agent 的 `API_SECRET`，也不是 CFSM 管理员密码。
4. 设置采集间隔，点击「验证连接并保存」。只有成功读取节点列表才会保存。
5. 退出登录，确认公开页只显示没有隐藏的节点。

修改数据源时口令留空表示沿用，始终不显示已保存的口令。更换地址或口令会重新开始本地每日流量记录，避免把不同数据源的同名 ID 合并。

启动无需预填 CFSM 地址或口令，首次配置前公开页显示「等待管理员配置 CFSM 数据源」。

## 配置及更新

| 环境变量 | 说明 |
| --- | --- |
| `BIND_ADDRESS` | Compose 宿主机绑定地址，默认 `127.0.0.1` |
| `PORT` | Compose 宿主机端口，默认 `8080` |
| `COOKIE_SECURE` | 经 HTTPS 访问时设为 `true`；本地 HTTP 用 `false` |
| `TZ` | 每日统计及重置日期时区，默认 `Asia/Shanghai` |
| `DATA_DIR` | 数据目录，容器内固定默认 `/data`；本机运行默认 `./data` |
| `LISTEN_ADDR` | 服务监听地址，默认 `:8080` |

CFSM 地址、App 口令和间隔通过后台设置，不写在 `.env` 中。容器以 UID/GID `10001:10001` 运行，根文件系统只读，数据写入 `cfsm-data` 命名卷。改用绑定目录时需要让此 UID 可写。

更新源码或更改运行参数后运行：

```sh
docker compose up -d --build --force-recreate
```

管理员通过网页首次创建，密码以哈希保存到 SQLite。没有默认账号密码，初始化成功后创建入口关闭；并发初始化只能有一个成功。之后在后台修改账号，保存后全部会话失效，重新登录。账号密码不通过环境变量设置。更新已有数据库时保留原账号。

重启保留账号、数据源及每日记录，登录会话需要重新建立。`docker compose down` 保留数据，**`docker compose down -v` 会删除账号、配置及记录**。

镜像可通过 Buildx 构建 Linux AMD64/ARM64，例如：

```sh
docker buildx build --platform linux/arm64 -t cfsm-monitor-go:arm64 --load .
```

## 数据与权限边界

服务端仅调用 `GET /api/servers`，使用 `X-App-Password`，不提供服务器修改、探针上报或电源控制；原 CFSM 及 Agent 继续运行。该行为与上游 [App API 文档](https://github.com/huilang-me/CF-Server-Monitor/blob/main/API.md) 对齐。

隐藏状态在下一次成功采集时更新，默认最大同步间隔约 5 分钟，加请求时间。采集失败时停止返回旧数据，以免已在 CFSM 改为隐藏的节点继续公开。Cloudflare 站点级挑战仍可能阻止服务端请求，需要 CFSM 站点允许该只读访问。

密码以 PBKDF2-SHA256 哈希和随机盐存入 SQLite；登录使用随机、12 小时有效的 HttpOnly/SameSite Cookie，退出后服务端立即作废。登录限流不信任客户端转发 IP。经反向代理公开提供服务时应使用 HTTPS，并设置 `COOKIE_SECURE=true`。

数据库为 `/data/cfsm.db`，使用 WAL 日志和事务，文件权限为 `0600`。数据库及其 `-wal`、`-shm` 文件包含私人配置和流量历史，不应公开分享；`.env` 同样属于私人配置。旧版 `settings.json` 和 `traffic.json` 不再读取，也不提供迁移。首次启用 SQLite 从新库开始。

数据库表分别保存设置、管理员、节点、采样基准和每日流量，不需要安装 MySQL 或 PostgreSQL。SQLite 驱动使用纯 Go 的 [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite)，不需要 CGO。许可见 [SQLite.LICENSE](third-party/SQLite.LICENSE) 和 [SQLite-3RD-PARTY.md](third-party/SQLite-3RD-PARTY.md)。浏览器及公开 API 不接收 CFSM 原始响应、Agent 密钥、IP 地址、备注或未过滤的全局统计。

流量配额按 CFSM 的 GiB 口径，计数按 `total` / `ul` / `dl` / `max` 配置计算。历史默认保留 90 个本地日，后台可设 30–3650 天；看板展示最近 30 天，图表可切换 7/14/30 日，更早记录可在后台按节点和日期查询，每页 100 条。缩短保留天数立即清理过期记录，清空历史同时重置采样基准，下次采集重新开始统计。

首次接入、重启期间的缺样或计数重置不会补成完整历史；跨午夜的短间隔按时间分摊并标记估算，跨日长中断保留空缺。重置日根据 CFSM 的 `reset_day` 和配置时区推算，不代表服务商实际结算时间。

## 流量关注提示

- **今日用量突增 / 红色日期**：与节点此前 7 个自然日内完整记录的中位数比较，至少有 3 天基准，当前单日已记录用量超过基准的 3 倍且达到 1 GiB 时提示。今天只用截至采集时的累计量，不按时间推算全天；历史日期也只使用该日之前的基准。
- **可能提前用尽**：此前 7 个自然日内至少有 3 天完整记录时，用这些记录的日均量估算剩余流量是否能支撑到下次重置；这是用量趋势估算。
- **余量不足 10% / 配额已用尽**：直接根据 CFSM 配额及周期已用量显示。
- **积累基准中**：完整历史不足 3 天，暂不判突增；每日缺样显示 `—`，部分记录显示 `*`，点击日期可看说明。部分日和缺样不作为比较基准，也不标为突增。正常跨午夜分摊的完整日记录可参与基准，但明细保留估算说明。

默认按关注程度排序，避免单个总览或合计图掩盖某台节点的用量变化。实际记录从本看板接入后开始，不会将 CFSM 的周期已用量伪装成今日流量。

## 本地开发与验证

需要 Go 1.26 或以上，直接 `go run .`，然后打开网页创建管理员。

```sh
go test ./...
go test -race ./...
go vet ./...
node --check web/app.js
node --check web/admin.js
node --test tests/traffic-view.test.mjs
node --test tests/theme.test.mjs
docker compose build
```

测试覆盖首次网页初始化、并发防重复创建、初始化权限与重启关闭、匿名/管理员可见范围、退出及过期会话、请求来源和限流、SQLite 持久化与事务回滚、旧 JSON 不导入、账号修改与重启、历史保留与清空、后台权限及分页、配置口令不回显、禁止携带口令跟随重定向、采集失败、隐藏状态变更、并发读取与每日流量边界。

健康检查 `GET /healthz` 检查进程和数据库连接；数据源尚未配置或采集失败时，`GET /api/servers` 返回 `503`，两者含义不同。后台「立即采集」至少间隔 30 秒。

## 版本与 GitHub 发布

`VERSION` 是应用版本的唯一来源，编译时嵌入程序。看板和后台页脚、`GET /api/version`、启动日志和以下命令显示同一版本：

```sh
go run . --version
docker run --rm cfsm-monitor-go:local --version
```

发布新版本时修改 `VERSION` 并更新 `CHANGELOG.md`，重新测试和构建。Git 标签使用 `v` 前缀，例如 `v0.1.0`。CI 检查标签与 `VERSION` 一致，执行 Go 竞态测试、静态检查、前端测试及 AMD64/ARM64 镜像构建。Actions 固定到已核对的提交，配置见 [.github/workflows/ci.yml](.github/workflows/ci.yml)。CI 不推送镜像、不创建 Release。

推送前确定 GitHub 账号/组织、仓库名和可见性，再添加对应远程地址。提交文件应包含源码、测试、许可、文档和 `.env.example`；`.env`、数据库、流量记录、截图、日志及测试归档保留在仓库之外。初始源码不包含任何实际 CFSM 地址、App 口令或管理员配置。
