# 发布镜像

目标仓库：`miwyf/cfsm-monitor-go`，镜像：`ghcr.io/miwyf/cfsm-monitor-go`。

1. 推送到 `main` 后，CI 测试通过即构建并发布 Linux AMD64/ARM64 的 `:latest` 镜像。PR 和手动运行 CI 只检查构建。
2. 发布固定版本时，更新 `VERSION` 和 `CHANGELOG.md`，提交并推送到 `main`。确认 CI 通过后创建并推送与 `VERSION` 一致的标签。

   ```sh
   git tag v0.2.3
   git push origin v0.2.3
   ```

3. 标签 CI 测试通过后，发布对应的 Linux AMD64/ARM64 版本镜像，例如 `:0.2.3`。
4. 发布后，在 GitHub 个人主页的 Packages 中确认包可见性为 Public，允许用户匿名拉取。
5. 验证公开拉取与多架构清单：

   ```sh
   docker buildx imagetools inspect ghcr.io/miwyf/cfsm-monitor-go:0.2.3
   docker --config "$(mktemp -d)" pull ghcr.io/miwyf/cfsm-monitor-go:latest
   ```

工作流使用 `GITHUB_TOKEN` 的 `packages: write` 权限，无需配置个人令牌。实现参考 [GitHub 镜像发布文档](https://docs.github.com/en/actions/tutorials/publish-packages/publish-docker-images)。
