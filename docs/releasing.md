# 发布镜像

目标仓库：`miwyf/cfsm-monitor-go`，镜像：`ghcr.io/miwyf/cfsm-monitor-go`。

1. 推送到 `main` 后，CI 测试通过即构建并发布 Linux AMD64/ARM64 的 `:latest` 镜像。PR 和手动运行 CI 只检查构建。
2. 发布固定版本时，更新 `VERSION` 和 `CHANGELOG.md`，提交并推送到 `main`。确认 CI 通过后创建并推送与 `VERSION` 一致的标签。

   ```sh
   git tag v0.3.0
   git push origin v0.3.0
   ```

3. 标签 CI 发布对应的 Linux AMD64/ARM64 版本镜像和 GitHub Release。Release 附带两种架构的程序，供应用内升级。程序文件名包含 `--update-compat` 输出的运行环境和 SQLite 架构版本。
4. 发布后，在 GitHub 个人主页的 Packages 中确认包可见性为 Public，允许用户匿名拉取。
5. 验证公开拉取与多架构清单：

   ```sh
   docker buildx imagetools inspect ghcr.io/miwyf/cfsm-monitor-go:0.3.0
   docker --config "$(mktemp -d)" pull ghcr.io/miwyf/cfsm-monitor-go:latest
   ```

应用只从本仓库的正式 GitHub Release 下载版本更高且兼容的程序，并校验 GitHub API 提供的文件大小和 SHA256。需要变更镜像运行环境、启动协议或做不兼容的数据变更时，应同步提高程序的运行环境兼容版本或 SQLite 架构版本。旧底座会提示拉取新版镜像。

工作流使用 `GITHUB_TOKEN` 发布镜像和 GitHub Release，无需配置个人令牌。实现参考 [GitHub 镜像发布文档](https://docs.github.com/en/actions/tutorials/publish-packages/publish-docker-images)。
