# 第三方许可

本项目自身代码采用 [MIT License](LICENSE)。以下组件保留各自许可及署名：

| 组件 | 用途及版本 | 许可文件 |
| --- | --- | --- |
| [CFSM-Theme-LuminaPlus](https://github.com/volcano-1025/CFSM-Theme-LuminaPlus) | 参考 v1.2.24 的配色及配额色标，固定提交 `391dc27f4ba21067afdfc3b20a4f03cde60919a6` | [MIT](third-party/LuminaPlus.LICENSE)，源码署名保留在 `web/theme.mjs` |
| [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) | v1.62.0，纯 Go SQLite 驱动 | [BSD-3-Clause](third-party/SQLite.LICENSE) 及 [第三方说明](third-party/SQLite-3RD-PARTY.md) |

Go 模块的精确依赖版本和校验值见 `go.mod` 与 `go.sum`。镜像内许可文件位于 `/usr/share/licenses/cfsm-monitor-go/`。

本项目通过只读 App API 连接 [CF-Server-Monitor](https://github.com/huilang-me/CF-Server-Monitor)，不包含其服务端或 Agent 程序。
