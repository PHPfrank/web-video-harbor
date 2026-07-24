# WebVideoHarbor 命名设计

## 目标

将尚未公开发布的“网页视频下载器”统一命名为 **WebVideoHarbor**，中文显示名为 **网页视频港**，为 GitHub 开源发布建立清晰、可识别且可延展的项目身份。

## 命名层级

- GitHub 仓库名：`web-video-harbor`
- 英文产品名：`WebVideoHarbor`
- 中文显示名：`网页视频港`
- macOS 发布包：`WebVideoHarbor-macOS.zip`
- 发布包顶层目录：`WebVideoHarbor-macOS/`
- 本地助手：`web-video-harbor-helper`
- Go 模块：`web-video-harbor/helper`
- 本地状态目录：`~/Library/Application Support/WebVideoHarbor/`
- 默认下载目录：`~/Downloads/WebVideoHarbor/`

## 用户界面与文档

扩展弹窗、设置页和 Chrome 扩展名称使用中文显示名“网页视频港”，README 首次出现时使用“WebVideoHarbor（网页视频港）”。安装说明、测试页面、示例任务标题和安全说明同步更新，避免新旧名称混用。

README 同时给出适合 GitHub 的一句话定位：这是一个由 Chrome 扩展与 macOS 本地助手组成、以隐私和安全边界为先的网页视频保存工具，支持浏览器可见的 MP4 与非 DRM HLS/M3U8。

## 兼容性边界

项目尚未正式发布，因此本次直接采用新目录和新二进制名称，不增加旧名称迁移逻辑。固定本地端口、API 路径、配置结构、扩展功能和下载安全策略保持不变。

旧发布 ZIP 不覆盖、不删除；新包以新名称原子发布。测试必须证明包名、顶层目录、二进制、扩展显示名、默认目录和文档已经统一，并继续排除凭证、日志、测试源码和媒体文件。

## 验收标准

1. 仓库生产代码和当前用户文档不再使用旧产品名；历史设计记录可保留原始名称。
2. Go 单元测试、扩展测试、macOS 生命周期测试、打包测试和完整 Chrome 端到端 smoke 全部通过。
3. 新 ZIP 可解压、可重建，包含 arm64 与 x86_64 universal 助手，并拥有稳定 SHA256。
4. 旧 ZIP 保留，新 ZIP 写入任务的集中 `outputs/` 目录。
