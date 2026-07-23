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

	"web-video-downloader/helper/internal/api"
	appconfig "web-video-downloader/helper/internal/config"
	"web-video-downloader/helper/internal/tasks"
)

const version = "dev"

type appDeps struct {
	defaultConfigPath func() (string, error)
	loadConfig        func(string) (appconfig.Config, error)
	lookPath          func(string) (string, error)
	serve             func(context.Context, appconfig.Config, string, string) error
}

func defaultAppDeps() appDeps {
	return appDeps{
		defaultConfigPath: appconfig.DefaultPath,
		loadConfig:        appconfig.Load,
		lookPath:          exec.LookPath,
		serve: func(ctx context.Context, cfg appconfig.Config, appVersion, ffmpegPath string) error {
			return serveHelper(ctx, cfg, appVersion, ffmpegPath, net.Listen)
		},
	}
}

func run(args []string, stdout io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runContext(ctx, args, stdout, os.Stderr, defaultAppDeps())
}

func runContext(ctx context.Context, args []string, stdout, stderr io.Writer, deps appDeps) int {
	flags := flag.NewFlagSet("web-video-helper", flag.ContinueOnError)
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
		fmt.Fprintf(stdout, "web-video-helper %s\n", version)
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

	ffmpegPath, findErr := deps.lookPath("ffmpeg")
	ffmpegStatus := "可用"
	if findErr != nil {
		ffmpegPath = ""
		ffmpegStatus = "未安装"
	}
	fmt.Fprintf(stdout, "本地助手监听：%s\n下载目录：%s\nFFmpeg: %s\n", cfg.Address, cfg.DownloadDir, ffmpegStatus)
	if err := deps.serve(ctx, cfg, version, ffmpegPath); err != nil {
		fmt.Fprintf(stderr, "本地助手运行失败：%v\n", err)
		return 1
	}
	return 0
}

type listenFunc func(network, address string) (net.Listener, error)

func serveHelper(ctx context.Context, cfg appconfig.Config, appVersion, ffmpegPath string, listen listenFunc) error {
	manager := tasks.NewManager()
	engine, err := api.NewEngine(manager, cfg.DownloadDir, nil, ffmpegPath)
	if err != nil {
		return fmt.Errorf("create task engine: %w", err)
	}
	apiServer, err := api.New(api.Options{
		Token: cfg.Token, Version: appVersion, FFmpegAvailable: ffmpegPath != "",
		DownloadDir: cfg.DownloadDir, Inspector: api.NewMediaInspector(nil),
		Tasks: engine, Revealer: api.FinderRevealer{},
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
		if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
			return nil
		}
		return fmt.Errorf("serve loopback API: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		shutdownErr := httpServer.Shutdown(shutdownCtx)
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

func main() {
	os.Exit(run(os.Args[1:], os.Stdout))
}
