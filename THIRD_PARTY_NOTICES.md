# 第三方组件说明

WebVideoHarbor 自身的源代码按根目录 [PolyForm Noncommercial License 1.0.0](LICENSE) 发布。该许可证不会替代、改变或扩展下列第三方组件及其所带组件的许可条款。完整 macOS 包会附带下列需要随包分发的许可文件。

## Go 与 golang.org/x/sys

本地助手使用 Go 标准库及 `golang.org/x/sys v0.20.0` 构建。预构建二进制包含 Go 运行时、标准库与 `x/sys` 的相关部分。

- 上游：[go.dev](https://go.dev/) 与 [golang/sys v0.20.0](https://pkg.go.dev/golang.org/x/sys@v0.20.0)
- 许可：BSD-3-Clause
- 安装包文件：`licenses/Go-LICENSE.txt`

## yt-dlp 2026.07.04

完整 macOS 包固定分发 `yt-dlp 2026.07.04` 官方 macOS universal 可执行文件，用于默认关闭的实验性平台兼容。

- 上游源码：[yt-dlp 2026.07.04](https://github.com/yt-dlp/yt-dlp/tree/2026.07.04)
- 许可：以上游该版本及其内置组件的完整许可清单为准
- 安装包文件：`licenses/yt-dlp-THIRD_PARTY_LICENSES.txt`

WebVideoHarbor 的 PolyForm Noncommercial 许可证不代表 yt-dlp 内置的第三方组件采用相同许可证。项目不会静默更新该可执行文件。

## Deno 2.8.1

完整 macOS 包固定分发 `Deno 2.8.1` 官方 macOS arm64 和 x86_64 可执行文件，仅作为 yt-dlp 解析公开页面时的 JavaScript 运行环境。

- 上游源码：[Deno v2.8.1](https://github.com/denoland/deno/tree/v2.8.1)
- 许可：MIT，以上游完整许可文件为准
- 安装包文件：`licenses/Deno-LICENSE.md`

项目不会静默更新该组件。

## FFmpeg

FFmpeg 是处理 M3U8/HLS 和合并音视频所需的外部程序，WebVideoHarbor 安装包不分发 FFmpeg。FFmpeg 的实际许可取决于用户安装的构建选项。

- 项目与许可信息：[ffmpeg.org](https://ffmpeg.org/) 与 [FFmpeg Legal](https://ffmpeg.org/legal.html)

## Google Chrome / Chromium

Google Chrome 是加载扩展所需的外部浏览器，WebVideoHarbor 安装包不分发 Chrome。

- 产品条款：[Google Chrome](https://www.google.com/chrome/terms/)
- 开源基础与第三方许可：[Chromium](https://www.chromium.org/Home/)
