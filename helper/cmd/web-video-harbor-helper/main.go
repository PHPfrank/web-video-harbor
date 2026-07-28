package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"web-video-harbor/helper/internal/api"
	appconfig "web-video-harbor/helper/internal/config"
	"web-video-harbor/helper/internal/settings"
	"web-video-harbor/helper/internal/tasks"
	"web-video-harbor/helper/internal/ytdlp"
)

var version = "dev"

type appDeps struct {
	defaultConfigPath       func() (string, error)
	loadConfig              func(string) (appconfig.Config, error)
	openSettings            func(string) *settings.Store
	lookPath                func(string) (string, error)
	probePlatformDownloader func(context.Context) (ytdlp.ProbeResult, error)
	probeRuntime            func(context.Context) (ytdlp.RuntimeResult, error)
	serve                   func(context.Context, appconfig.Config, string, string, ytdlp.ProbeResult, ytdlp.RuntimeResult, *settings.Store) error
}

func defaultAppDeps() appDeps {
	return appDeps{
		defaultConfigPath:       appconfig.DefaultPath,
		loadConfig:              appconfig.Load,
		openSettings:            settings.Open,
		lookPath:                exec.LookPath,
		probePlatformDownloader: ytdlp.Probe,
		probeRuntime:            ytdlp.ProbeRuntime,
		serve: func(ctx context.Context, cfg appconfig.Config, appVersion, ffmpegPath string, platform ytdlp.ProbeResult, runtime ytdlp.RuntimeResult, compatibility *settings.Store) error {
			return serveHelper(ctx, cfg, appVersion, ffmpegPath, platform, runtime, compatibility, net.Listen)
		},
	}
}

func run(args []string, stdout io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runContext(ctx, args, stdout, os.Stderr, defaultAppDeps())
}

func runContext(ctx context.Context, args []string, stdout, stderr io.Writer, deps appDeps) int {
	flags := flag.NewFlagSet("web-video-harbor-helper", flag.ContinueOnError)
	flags.SetOutput(stderr)
	showVersion := flags.Bool("version", false, "显示版本")
	configPath := flags.String("config", "", "配置文件路径")
	printToken := flags.Bool("print-token", false, "打印配对密钥后退出")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "不支持额外的位置参数")
		return 2
	}
	if *showVersion {
		fmt.Fprintf(stdout, "web-video-harbor-helper %s\n", version)
		return 0
	}
	path := *configPath
	if path == "" {
		var err error
		path, err = deps.defaultConfigPath()
		if err != nil {
			fmt.Fprintf(stderr, "无法确定配置位置：%v\n", err)
			return 1
		}
	}
	cfg, err := deps.loadConfig(path)
	if err != nil {
		fmt.Fprintf(stderr, "无法加载配置：%v\n", err)
		return 1
	}
	if *printToken {
		fmt.Fprintln(stdout, cfg.Token)
		return 0
	}
	settingsPath, err := settings.PathForConfig(path)
	if err != nil {
		fmt.Fprintf(stderr, "无法确定本地设置位置：%v\n", err)
		return 1
	}
	compatibility := deps.openSettings(settingsPath)

	ffmpegPath, findErr := deps.lookPath("ffmpeg")
	ffmpegStatus := "可用"
	if findErr != nil {
		ffmpegPath = ""
		ffmpegStatus = "未安装"
	}
	platform, probeErr := deps.probePlatformDownloader(ctx)
	platformStatus := "不可用"
	if probeErr == nil {
		platformStatus = fmt.Sprintf("可用（%s）", platform.Version)
	} else {
		platform = ytdlp.ProbeResult{}
	}
	runtimeResult, runtimeErr := deps.probeRuntime(ctx)
	runtimeStatus := "不可用"
	if runtimeErr == nil {
		runtimeStatus = fmt.Sprintf("可用（%s）", runtimeResult.Version)
	} else {
		runtimeResult = ytdlp.RuntimeResult{}
	}
	fmt.Fprintf(stdout, "本地助手监听：%s\n下载目录：%s\nFFmpeg: %s\n平台解析器: %s\nJavaScript 解析环境: %s\n", cfg.Address, cfg.DownloadDir, ffmpegStatus, platformStatus, runtimeStatus)
	if err := deps.serve(ctx, cfg, version, ffmpegPath, platform, runtimeResult, compatibility); err != nil {
		fmt.Fprintf(stderr, "本地助手运行失败：%v\n", err)
		return 1
	}
	return 0
}

type listenFunc func(network, address string) (net.Listener, error)

func serveHelper(ctx context.Context, cfg appconfig.Config, appVersion, ffmpegPath string, platform ytdlp.ProbeResult, runtime ytdlp.RuntimeResult, compatibility *settings.Store, listen listenFunc) error {
	if compatibility == nil {
		return errors.New("local settings store is required")
	}
	manager := tasks.NewManager()
	engine, err := api.NewEngine(manager, cfg.DownloadDir, nil, ffmpegPath, platform, runtime, compatibility)
	if err != nil {
		return fmt.Errorf("create task engine: %w", err)
	}
	defer func() { _ = finishEngineAndClosePlatform(engine, platform, runtime) }()
	apiServer, err := api.New(api.Options{
		Token: cfg.Token, Version: appVersion, FFmpegAvailable: ffmpegPath != "",
		PlatformDownloaderAvailable: platform.Path != "", PlatformDownloaderVersion: platform.Version,
		JavaScriptRuntimeAvailable: runtime.Path != "", JavaScriptRuntimeVersion: runtime.Version,
		DownloadDir: cfg.DownloadDir, Inspector: api.NewMediaInspector(nil),
		Tasks: engine, Revealer: api.FinderRevealer{}, Settings: compatibility,
	})
	if err != nil {
		return fmt.Errorf("create API server: %w", err)
	}
	listener, err := listen("tcp", cfg.Address)
	if err != nil {
		return fmt.Errorf("listen on configured loopback address: %w", err)
	}
	httpServer := apiServer.HTTPServer(cfg.Address)
	serveDone := make(chan error, 1)
	go func() { serveDone <- httpServer.Serve(listener) }()

	select {
	case err := <-serveDone:
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		shutdownErr := shutdownServices(shutdownCtx, httpServer.Shutdown, engine)
		cancel()
		if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			return errors.Join(fmt.Errorf("serve loopback API: %w", err), shutdownErr)
		}
		return shutdownErr
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		shutdownErr := shutdownServices(shutdownCtx, httpServer.Shutdown, engine)
		cancel()
		serveErr := <-serveDone
		if shutdownErr != nil {
			return fmt.Errorf("shut down loopback API: %w", shutdownErr)
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) && !errors.Is(serveErr, net.ErrClosed) {
			return fmt.Errorf("serve loopback API: %w", serveErr)
		}
		return nil
	}
}

type engineShutdowner interface {
	Shutdown(context.Context) error
}

type platformCloser interface {
	Close() error
}

// finishEngineAndClosePlatform performs an unbounded final worker join after
// the user-facing shutdown deadline. The executable snapshot is closed only
// after no Runner can still hold an active lease.
func finishEngineAndClosePlatform(engine engineShutdowner, platform platformCloser, additional ...platformCloser) error {
	result := errors.Join(engine.Shutdown(context.Background()), platform.Close())
	for _, closer := range additional {
		result = errors.Join(result, closer.Close())
	}
	return result
}

func shutdownServices(ctx context.Context, shutdownHTTP func(context.Context) error, engine engineShutdowner) error {
	results := make(chan error, 2)
	go func() { results <- shutdownHTTP(ctx) }()
	go func() { results <- engine.Shutdown(ctx) }()
	first := <-results
	second := <-results
	return errors.Join(first, second)
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout))
}
