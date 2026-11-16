# 发布镜像

目标仓库：`miwyf/cfsm-monitor-go`，镜像：`ghcr.io/miwyf/cfsm-monitor-go`。

1. 将源码推送到 GitHub 仓库，确认普通 CI 通过。
2. 修改 `VERSION`、更新 `CHANGELOG.md` 并提交，创建与版本一致的标签，例如：

   ```sh
   git tag v0.2.1
   git push origin main
   git push origin v0.2.1
   ```

3. 标签 CI 测试通过后，发布 Linux AMD64/ARM64 镜像，生成 `:0.2.1` 和 `:latest`。普通分支、PR 和手动运行 CI 只检查构建。
4. 首次发布后，在 GitHub 个人主页的 Packages 中打开该镜像，将包可见性设为 Public，才能让用户匿名拉取。
5. 验证公开拉取与多架构清单，再移除 README 中的「待首次发布」说明：

   ```sh
   docker buildx imagetools inspect ghcr.io/miwyf/cfsm-monitor-go:0.2.1
   docker pull ghcr.io/miwyf/cfsm-monitor-go:latest
   ```

工作流使用 `GITHUB_TOKEN` 的 `packages: write` 权限，无需配置个人令牌。实现参考 [GitHub 镜像发布文档](https://docs.github.com/en/actions/tutorials/publish-packages/publish-docker-images)。
