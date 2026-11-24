// PortTunnel 服务端入口。
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"ptunnel/internal/audit"
	"ptunnel/internal/auth"
	"ptunnel/internal/config"
	"ptunnel/internal/pki"
	"ptunnel/internal/server/api"
	"ptunnel/internal/server/clientmgr"
	"ptunnel/internal/server/portmgr"
	"ptunnel/internal/server/proxy"
	"ptunnel/internal/sqllog"
	"ptunnel/internal/store"
	"ptunnel/internal/tunnel"
)

var version = "dev"

func main() {
	var cfgPath string
	var resetPwd string
	flag.StringVar(&cfgPath, "c", "server.yaml", "配置文件路径")
	flag.StringVar(&resetPwd, "reset-password", "", "重置管理员口令后退出（需配合 -c 指定配置）")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if resetPwd != "" {
		if err := resetAdminPassword(cfgPath, resetPwd, logger); err != nil {
			logger.Error("重置口令失败", "err", err)
			os.Exit(1)
		}
		return
	}

	if err := run(cfgPath, logger); err != nil {
		logger.Error("服务端启动失败", "err", err)
		os.Exit(1)
	}
}

// resetAdminPassword 用于忘记管理口令时离线重置（需停止服务端后执行）。
func resetAdminPassword(cfgPath, password string, logger *slog.Logger) error {
	if len(password) < 8 {
		return fmt.Errorf("新口令至少 8 位")
	}
	cfg, err := config.LoadServer(cfgPath)
	if err != nil {
		return err
	}
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	username := os.Getenv("PTUNNEL_ADMIN_USER")
	if username == "" {
		username = "admin"
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	user, err := st.GetUserByName(username)
	if err != nil {
		return err
	}
	if user == nil {
		if err := st.CreateUser(username, hash, "admin"); err != nil {
			return err
		}
	} else if err := st.UpdateUserPassword(username, hash); err != nil {
		return err
	}
	logger.Info("管理员口令已重置，请重启服务端", "username", username)
	return nil
}

func run(cfgPath string, logger *slog.Logger) error {
	cfg, err := config.LoadServer(cfgPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.Storage.DataDir, 0o750); err != nil {
		return err
	}

	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	logger.Info("数据库已就绪", "driver", st.Driver())

	if err := bootstrapAdmin(st, logger); err != nil {
		return err
	}
	applySettingOverrides(st, cfg, logger)

	ca, err := pki.LoadOrCreateCA(cfg.Storage.DataDir)
	if err != nil {
		return fmt.Errorf("初始化 CA 失败: %w", err)
	}
	tunnelCert, err := ca.EnsureServerCert(cfg.Storage.DataDir, "tunnel", cfg.Server.TunnelCertHosts, 3*365*24*time.Hour)
	if err != nil {
		return fmt.Errorf("生成隧道证书失败: %w", err)
	}
	adminHosts := append([]string{"localhost", "127.0.0.1"}, cfg.Server.TunnelCertHosts...)
	adminCert, err := ca.EnsureServerCert(cfg.Storage.DataDir, "admin", adminHosts, 3*365*24*time.Hour)
	if err != nil {
		return fmt.Errorf("生成管理面证书失败: %w", err)
	}

	// 清理上次残留状态
	st.ResetProxyStates()
	if n, err := st.MarkUnclean(); err == nil && n > 0 {
		logger.Info("已收尾悬挂的连接审计记录", "count", n)
	}

	aw := audit.NewWriter(st, audit.Config{
		Mode:          cfg.Audit.ConnLogMode,
		SampleRate:    cfg.Audit.ConnLogSampleRate,
		QueueSize:     cfg.Audit.ConnLogQueueSize,
		RetentionDays: cfg.Audit.ConnLogRetention,
		MACLookup:     cfg.Audit.MACLookup,
	}, logger)
	aw.Start()
	defer aw.Close()

	// SQL 语句审计（本地文件，按天分割 + gzip）
	var sqlw *sqllog.Writer
	if cfg.Audit.SQLLogEnabled {
		sqlw, err = sqllog.NewWriter(cfg.Audit.SQLLogDir, cfg.Audit.SQLLogRetentionDays, logger)
		if err != nil {
			return err
		}
		sqlw.Start()
		defer sqlw.Close()
		logger.Info("SQL 语句审计已启用", "dir", cfg.Audit.SQLLogDir, "retention_days", cfg.Audit.SQLLogRetentionDays)
	}

	mgr := clientmgr.New(st, logger, clientmgr.Options{
		Heartbeat:         cfg.Tunnel.Heartbeat.D(),
		IdleTimeout:       cfg.Tunnel.IdleTimeout.D(),
		HandshakeTimeout:  cfg.Tunnel.HandshakeTimeout.D(),
		MaxStreams:        cfg.Tunnel.MaxStreams,
		MaxConnsPerClient: cfg.Tunnel.MaxConnsPerClient,
		AutoApprove:       cfg.Tunnel.AutoApproveLocal,
		PublicBind:        cfg.Server.PublicBind,
		DefaultMaxConns:   cfg.Tunnel.MaxConnsPerProxy,
	})
	ph := proxy.New(mgr, st, aw, sqlw, logger, cfg.Tunnel.MaxConnsPerProxy, cfg.Audit.SQLStatementMaxLen, cfg.Audit.MACLookup, cfg.Tunnel.HalfCloseGrace.D())
	ports := portmgr.New(ph.PortHandler, func(s portmgr.State) {
		st.SetProxyState(s.ProxyID, s.State, s.Msg)
	})

	sessKey, err := auth.LoadOrCreateKey(st.GetSetting, st.SetSetting)
	if err != nil {
		return fmt.Errorf("加载会话密钥失败: %w", err)
	}
	sessMgr := auth.NewSessionManager(sessKey, cfg.Auth.SessionTTL.D())
	apiSrv := api.New(cfg, st, mgr, ports, ph, aw, sqlw, sessMgr, ca, logger, version)

	// 客户端本地登记并自动通过审批后，需要重建端口监听
	mgr.OnProxiesRegistered = apiSrv.SyncPorts
	apiSrv.SyncPorts()

	// 审计保留清理
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go aw.RunRetention(ctx)

	// 隧道端口：同时承载 mTLS 隧道与注册接口（按 ALPN 分流）
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{tunnelCert},
		MinVersion:   tls.VersionTLS12,
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    ca.Pool,
		NextProtos:   []string{tunnel.ALPN, "http/1.1"},
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return nil // 注册接口允许无客户端证书
			}
			leaf, err := x509.ParseCertificate(rawCerts[0])
			if err != nil {
				return err
			}
			cl, err := st.GetClientByCertSerial(pki.SerialOf(leaf))
			if err != nil {
				return err
			}
			if cl == nil {
				return fmt.Errorf("客户端证书未登记或已轮换")
			}
			if cl.RevokedAt != nil {
				return fmt.Errorf("客户端证书已吊销")
			}
			if !cl.Enabled {
				return fmt.Errorf("客户端已禁用")
			}
			return nil
		},
	}
	ln, err := net.Listen("tcp", cfg.Server.TunnelAddr)
	if err != nil {
		return fmt.Errorf("监听隧道端口失败: %w", err)
	}
	al := &alpnListener{Listener: tls.NewListener(ln, tlsCfg), onTunnel: func(c net.Conn) {
		if err := mgr.Attach(c); err != nil {
			logger.Warn("隧道会话结束", "err", err, "remote", c.RemoteAddr().String())
			_ = c.Close()
		}
	}}
	tunnelHTTP := &http.Server{Handler: apiSrv.ClientHandler(), ReadHeaderTimeout: 15 * time.Second}
	go func() {
		if err := tunnelHTTP.Serve(al); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("隧道端口服务异常", "err", err)
		}
	}()
	logger.Info("隧道端口已监听", "addr", cfg.Server.TunnelAddr, "alpn", tunnel.ALPN)

	// 管理面 HTTPS
	adminSrv := &http.Server{
		Addr:              cfg.Server.AdminAddr,
		Handler:           apiSrv.Handler(),
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{adminCert}, MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: 15 * time.Second,
	}
	adminErr := make(chan error, 1)
	go func() {
		logger.Info("管理面已启动", "addr", cfg.Server.AdminAddr, "url", "https://"+displayAddr(cfg.Server.AdminAddr))
		if err := adminSrv.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			adminErr <- err
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	// 可选的 pprof 调试端口（默认关闭，建议只监听内网/本机）
	var pprofSrv *http.Server
	if addr := cfg.Server.PProfAddr; addr != "" {
		pprofSrv = &http.Server{Addr: addr, Handler: http.DefaultServeMux, ReadHeaderTimeout: 10 * time.Second}
		go func() {
			logger.Warn("pprof 调试端口已启动，请仅在内网或本机开放", "addr", addr)
			if err := pprofSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("pprof 服务异常退出", "err", err)
			}
		}()
	}

	select {
	case s := <-sig:
		logger.Info("收到退出信号", "signal", s.String())
	case err := <-adminErr:
		logger.Error("管理面异常退出", "err", err)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	mgr.KickAll(5)
	_ = adminSrv.Shutdown(shutdownCtx)
	if pprofSrv != nil {
		_ = pprofSrv.Shutdown(shutdownCtx)
	}
	_ = tunnelHTTP.Shutdown(shutdownCtx)
	ports.CloseAll()
	cancel()
	logger.Info("服务端已退出")
	return nil
}

// applySettingOverrides 用管理台持久化的设置覆盖配置文件中的可调项（重启后生效）。
func applySettingOverrides(st *store.Store, cfg *config.ServerConfig, logger *slog.Logger) {
	if v := st.GetSetting("conn_log_mode", ""); v == "full" || v == "sample" || v == "off" {
		if v != cfg.Audit.ConnLogMode {
			logger.Info("应用管理台设置", "conn_log_mode", cfg.Audit.ConnLogMode+" -> "+v)
		}
		cfg.Audit.ConnLogMode = v
	}
	if v := st.GetSetting("conn_log_retention_days", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Audit.ConnLogRetention = n
		}
	}
}

// openStore 按配置打开本地 SQLite 或 MySQL。
func openStore(cfg *config.ServerConfig) (*store.Store, error) {
	return store.Open(store.Options{
		Driver:          cfg.Storage.Driver,
		SQLitePath:      cfg.Storage.SQLitePath,
		DSN:             cfg.Storage.MySQL.DSN,
		MaxOpenConns:    cfg.Storage.MySQL.MaxOpenConns,
		MaxIdleConns:    cfg.Storage.MySQL.MaxIdleConns,
		ConnMaxLifetime: cfg.Storage.MySQL.ConnMaxLifetime.D(),
	})
}

func bootstrapAdmin(st *store.Store, logger *slog.Logger) error {
	n, err := st.CountUsers()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	username := os.Getenv("PTUNNEL_ADMIN_USER")
	if username == "" {
		username = "admin"
	}
	password := os.Getenv("PTUNNEL_ADMIN_PASSWORD")
	generated := false
	if password == "" {
		password = auth.RandomToken()[:12]
		generated = true
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	if err := st.CreateUser(username, hash, "admin"); err != nil {
		return err
	}
	if generated {
		logger.Warn("已创建初始管理员账号，请尽快登录修改口令", "username", username, "password", password)
	} else {
		logger.Info("已创建初始管理员账号", "username", username)
	}
	return nil
}

func displayAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// alpnListener 按 ALPN 分流：隧道连接交给回调处理，其余（HTTP/1.1）交给 HTTP 服务器。
type alpnListener struct {
	net.Listener
	onTunnel func(net.Conn)
}

func (l *alpnListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		tc, ok := c.(*tls.Conn)
		if !ok {
			_ = c.Close()
			continue
		}
		_ = tc.SetDeadline(time.Now().Add(15 * time.Second))
		if err := tc.Handshake(); err != nil {
			_ = tc.Close()
			continue
		}
		_ = tc.SetDeadline(time.Time{})
		if strings.EqualFold(tc.ConnectionState().NegotiatedProtocol, tunnel.ALPN) {
			if len(tc.ConnectionState().PeerCertificates) == 0 {
				_ = tc.Close()
				continue
			}
			go l.onTunnel(tc)
			continue
		}
		return tc, nil
	}
}
