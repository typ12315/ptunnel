// PortTunnel 客户端入口。
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"ptunnel/internal/agent"
	"ptunnel/internal/config"
)

var version = "dev"

func main() {
	var cfgPath string
	flag.StringVar(&cfgPath, "c", "client.yaml", "配置文件路径")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.LoadClient(cfgPath)
	if err != nil {
		logger.Error("加载配置失败", "err", err)
		os.Exit(1)
	}
	ag, err := agent.New(cfg, logger, version)
	if err != nil {
		logger.Error("初始化客户端失败", "err", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	logger.Info("客户端启动", "server", cfg.Server.Addr, "version", version)
	if err := ag.Run(ctx); err != nil {
		logger.Error("客户端退出", "err", err)
		os.Exit(1)
	}
	logger.Info("客户端已退出")
}
