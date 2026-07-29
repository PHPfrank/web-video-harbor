# WebVideoHarbor（网页视频港）

WebVideoHarbor Community Edition 是一个完全免费、源码公开、本地运行的网页媒体技术项目，用于技术学习与交流。它由 Chrome 扩展和 macOS 本地助手组成，用于学习网页媒体识别、非加密 HLS 分片处理以及浏览器与本地程序通信。

当前社区版本：**v1.0.0**。

## 下载与安装

- [下载 macOS v1.0.0 安装包](https://github.com/PHPfrank/web-video-harbor/releases/download/v1.0.0/WebVideoHarbor-macOS-v1.0.0.zip)
- [查看 v1.0.0 发布说明](https://github.com/PHPfrank/web-video-harbor/releases/tag/v1.0.0)
- [阅读安装使用说明](docs/安装使用说明.md)

安装包同时支持 Apple Silicon 和 Intel Mac。下载后解压即可按安装说明操作，普通用户不需要安装 Go 或自行编译。安装包 SHA-256：

```text
6e877454e925317733fbad49fe3d802d8e85c80b05f54bf0e55139491d146979
```

## 默认功能

- 识别并下载网页直接暴露的 HTTP(S) MP4 和 WebM。
- 下载并合并非加密、非 DRM 的 M3U8/HLS，支持单画质和多画质清单。
- 显示下载进度，支持取消、重试和在 Finder 中定位文件。
- 媒体识别和文件处理都在用户的 Mac 上完成，不上传视频或下载记录。

M3U8/HLS 合并需要 FFmpeg。默认下载目录为 `~/Downloads/WebVideoHarbor/`，本地状态保存在 `~/Library/Application Support/WebVideoHarbor/`。

## 从源码运行（开发者）

下面的步骤面向需要查看或修改源码的开发者。普通用户请直接使用上面的 macOS 安装包。详细操作请阅读 [安装使用说明](docs/安装使用说明.md)。从项目根目录开始：

1. 安装 Go 和 FFmpeg，然后构建并启动本地助手。
2. 在 Chrome 的扩展管理页中“加载已解压的扩展程序”，选择 `extension/` 文件夹。
3. 运行助手的 `--print-token` 命令，将配对密钥保存到扩展设置页。
4. 打开有权保存的网页媒体，播放数秒后打开扩展并重新扫描。

## 隐私与本地安全

本地助手只监听 `127.0.0.1:17432`，且除健康检查外的操作均需要随机配对密钥。扩展不读取或导出 Cookie、Authorization 授权头、请求体或页面正文；扩展和本地助手不包含分析、遥测、广告 SDK、行为跟踪或远程授权服务。详细说明见 [隐私说明](PRIVACY.md)。

## 可选推荐资源

项目官网提供一页独立的[推荐资源](https://phpfrank.github.io/web-video-harbor/recommendations.html)，供有需要的用户自行查看存储设备和云服务。该页面会清晰披露推广链接；如果用户通过链接购买，项目维护者可能获得佣金，但推荐与购买都不影响 WebVideoHarbor 的免费功能。扩展只提供入口，不保存推广参数，也不跟踪点击。

## 兼容性与使用边界

项目保留了针对 YouTube、哔哩哔哩及微信视频号页面的实验性平台兼容能力，但默认关闭。用户需要在扩展设置页中阅读说明并主动确认后才能开启；扩展和本地助手会同时执行该开关。

该兼容模块只尝试处理无需登录便可公开访问的单个视频，或页面已向 Chrome 暴露的普通 MP4/非加密 M3U8。它不读取 Cookie，不支持登录、会员、付费、私有、加密、DRM、地区限制或机器人验证绕过。完整边界见 [使用边界](docs/使用边界.md)。

请只下载自己创作、已获授权、已进入公有领域，或当地法律明确允许保存的内容。免费、源码公开或技术学习用途不等于获得第三方内容的下载授权。

## 源码许可与第三方组件

当前主分支以及未来版本中由 WebVideoHarbor 提供的源代码使用 [PolyForm Noncommercial License 1.0.0](LICENSE)。该许可证允许个人学习、研究、测试、业余项目及其中列出的其他非商业用途，也允许在非商业目的下修改和分发。

未经 PHPfrank 事先书面许可，不得将这些版本用于商业产品、收费服务、广告获利、企业内部商业活动或其他商业用途。如果无法确定某项使用是否属于非商业用途，请在使用前通过官方仓库联系 PHPfrank。

历史授权不追溯变更：`v1.0.0` 标签以及截至提交 [`b56aa8c`](https://github.com/PHPfrank/web-video-harbor/commit/b56aa8c) 的仓库修订仍按当时的 MIT License 使用。

源码许可证不授予 WebVideoHarbor、网页视频港或官方视觉标识的品牌使用权，详见 [品牌政策](TRADEMARKS.md)。完整 macOS 包包含固定版本的 yt-dlp 和 Deno，并调用用户安装的 FFmpeg；这些组件继续适用其各自的上游许可条款，详见 [第三方组件说明](THIRD_PARTY_NOTICES.md)。

## 从旧版升级

依次停止旧助手、替换安装包、在 Chrome 中重新加载扩展，再启动新助手。用户状态不在安装包目录内，因此会保留现有配对状态和下载目录设置。请参阅 [安装使用说明](docs/安装使用说明.md)。
