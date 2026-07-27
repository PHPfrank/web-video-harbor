# 第三方组件说明

本项目的本地助手使用 Go 标准库及 `golang.org/x/sys v0.20.0` 构建。预构建二进制包含 Go 运行时、标准库与 `x/sys` 的相关部分；本压缩包附带适用于这些 Go Authors 代码的完整 BSD-3-Clause [Go LICENSE](licenses/Go-LICENSE.txt)。Go 官方项目信息见 [go.dev](https://go.dev/)，`x/sys` 的源码、版本与许可信息见 [golang/sys](https://pkg.go.dev/golang.org/x/sys@v0.20.0) 及其 [LICENSE](https://cs.opensource.google/go/x/sys/+/v0.20.0:LICENSE)。

本项目固定分发 `yt-dlp 2026.07.04` 的官方 macOS universal 可执行文件，用于解析 YouTube 与哔哩哔哩无需登录即可观看的公开视频。上游项目与源码见 [github.com/yt-dlp/yt-dlp](https://github.com/yt-dlp/yt-dlp/tree/2026.07.04)。安装包同时附带该版本发布的完整第三方许可证清单：[yt-dlp THIRD_PARTY_LICENSES](licenses/yt-dlp-THIRD_PARTY_LICENSES.txt)。本项目不会静默更新该组件。

FFmpeg 是下载和合并 M3U8/HLS 时需要的外部程序，本压缩包不分发 FFmpeg。FFmpeg 的实际许可证取决于用户安装的构建选项；项目说明与许可证信息见 [ffmpeg.org](https://ffmpeg.org/) 和 [FFmpeg Legal](https://ffmpeg.org/legal.html)。

Google Chrome 是加载扩展所需的外部浏览器，本压缩包不分发 Chrome。Chrome 的产品条款见 [Google Chrome](https://www.google.com/chrome/terms/)，其开源基础 Chromium 的第三方许可证信息见 [Chromium](https://www.chromium.org/Home/)。
