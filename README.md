# CFSM Monitor Go

基于 [CF-Server-Monitor](https://github.com/huilang-me/CF-Server-Monitor) 的独立流量看板，查看各节点的每日用量、周期用量、剩余流量、总配额和重置日期。

- 访客查看公开节点，管理员登录后查看隐藏节点。
- 支持卡片、列表、每日明细、节点流量图和浅色/暗色主题。
- 支持多个飞书、企业微信、钉钉通知渠道，可多选节点，设置流量预警和每日播报。
- Go 单程序运行，SQLite 保存配置和历史，无需 Home Assistant 或额外数据库。

## 安装

镜像：`ghcr.io/miwyf/cfsm-monitor-go:latest`，支持 Linux AMD64 / ARM64。

### 1. Docker Compose（推荐）

下载 [compose.yaml](compose.yaml)，放到一个空目录，在该目录执行：

```sh
docker compose pull
docker compose up -d
```

默认访问 `http://127.0.0.1:8080`。

修改端口或允许其他设备访问：在同目录新建 `.env`，填写以下内容后重新执行启动命令。

```dotenv
BIND_ADDRESS=0.0.0.0
PORT=8080
```

其他设备访问 `http://服务器IP:8080`。

### 2. Docker Run

拉取镜像并启动：

```sh
docker pull ghcr.io/miwyf/cfsm-monitor-go:latest
docker run -d --name cfsm-monitor --restart unless-stopped -p 127.0.0.1:8080:8080 -e TZ=Asia/Shanghai -v cfsm-data:/data ghcr.io/miwyf/cfsm-monitor-go:latest
```

默认访问 `http://127.0.0.1:8080`。允许其他设备访问时，将端口参数改为 `-p 8080:8080`。

### 3. 1Panel

1. 打开「容器 → 编排 → 创建」，名称填 `cfsm-monitor-go`，选择「编辑」，粘贴以下内容后启动（[1Panel 编排说明](https://1panel.pro/docs/v2/user_manual/containers/composes/)）：

   ```yaml
   services:
     cfsm-monitor:
       image: ghcr.io/miwyf/cfsm-monitor-go:latest
       pull_policy: always
       restart: unless-stopped
       ports:
         - "8080:8080"
       environment:
         TZ: Asia/Shanghai
         COOKIE_SECURE: "false"
       volumes:
         - cfsm-data:/data

   volumes:
     cfsm-data:
   ```

2. 访问 `http://服务器IP:8080` 创建管理员，再配置 CFSM 地址和 App 访问口令。端口被占用时修改左侧端口，如 `"8081:8080"`。

数据保存在命名卷中，更新时拉取新镜像并重建容器，保留原数据卷。

### 4. 手动部署（源码编译）

下载并解压源码，进入项目目录。直接运行需要 Go 1.26 或以上。

Linux / macOS：

```sh
go build -trimpath -o cfsm-monitor .
./cfsm-monitor
```

Windows PowerShell：

```powershell
go build -trimpath -o cfsm-monitor.exe .
.\cfsm-monitor.exe
```

访问 `http://127.0.0.1:8080`，数据默认保存到当前目录的 `data/`。

使用 Docker 从源码构建：

```sh
docker compose -f compose.yaml -f compose.dev.yaml up -d --build
```

## 首次配置

1. 打开网页，自行创建管理员账号，密码至少 6 位。
2. 进入「后台管理 → 数据源设置」，填写 CFSM 根地址，如 `https://status.example.com`。
3. 填写 CFSM「设置 → 安全设置 → App 访问口令」中的 16–32 位口令，点击「验证连接并保存」。请使用 App 访问口令，不是 Agent 的 `API_SECRET`。

没有默认账号密码，CFSM 地址和口令均通过后台设置。

## 消息推送

进入「后台管理 → 消息推送」，添加渠道并填写机器人 Webhook；飞书、钉钉启用加签时填写签名密钥。微信渠道使用企业微信群机器人。

| 设置 | 说明 |
| --- | --- |
| 节点范围 | 公开、隐藏、全部或指定节点；指定节点可多选 |
| 流量预警 | 配额阈值、单日用量、流量突增、预计重置前用尽 |
| 每日播报 | 默认每天 08:00，可设置最多 4 个时间，按后台时区执行 |
| 播报内容 | 每个节点独立分块，名称、昨日用量、当前剩余/总配额、重置日期分行显示，节点之间留空行 |

保存后点击「测试发送」，确认群内收到消息。历史从本看板接入后开始记录；`*` 表示部分记录，`—` 表示无样本。

## 运行参数

| 环境变量 | 默认值 | 用途 |
| --- | --- | --- |
| `BIND_ADDRESS` | `127.0.0.1` | Compose 宿主机绑定地址 |
| `PORT` | `8080` | Compose 宿主机端口 |
| `TZ` | `Asia/Shanghai` | 流量统计和通知时区 |
| `COOKIE_SECURE` | `false` | 经 HTTPS 访问时设为 `true` |
| `DATA_DIR` | Docker：`/data`；手动：`./data` | 数据目录 |
| `LISTEN_ADDR` | `:8080` | 程序监听地址 |

Compose 通过 `.env` 设置参数；Docker Run 使用 `-e`；手动运行使用系统环境变量。

## 更新与数据

- **Compose**：执行 `docker compose pull`，再执行 `docker compose up -d`。
- **Docker Run**：执行 `docker pull ghcr.io/miwyf/cfsm-monitor-go:latest`，删除旧容器 `docker rm -f cfsm-monitor` 后重新执行启动命令，使用原数据卷。
- **1Panel**：拉取新镜像并重建容器，保留原数据卷。
- **源码编译**：停止程序，替换为新版源码后重新编译、启动，保留原数据目录或数据卷。

数据库文件为数据目录下的 `cfsm.db`。Compose 使用 `cfsm-data` 命名卷，Docker Run 使用上面 `-v` 指定的命名卷；更新时保留原数据卷。

`docker compose down` 保留数据，**`docker compose down -v` 会删除账号、配置和历史**。每日历史默认保留 90 天，可在后台调整。

## 版本与许可

[版本号](VERSION) · [更新记录](CHANGELOG.md) · [发布说明](docs/releasing.md) · [MIT License](LICENSE) · [第三方许可](THIRD_PARTY_NOTICES.md)
