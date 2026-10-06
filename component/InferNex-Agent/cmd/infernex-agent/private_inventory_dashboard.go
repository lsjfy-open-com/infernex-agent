package main

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/domainstore"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/inventoryview"
)

const (
	defaultPrivateDashboardListen = "127.0.0.1:8081"
	maxPrivateDashboardTokenBytes = 4096
	privateDashboardHeaderBytes   = 16 << 10
)

type privateInventoryDashboardOptions struct {
	stateDirectory string
	listenAddress  string
	tokenFile      string
	tlsCertificate string
	tlsKey         string
}

type privateInventoryDashboardStore interface {
	inventoryview.Reader
	Scope() string
}

var (
	openPrivateInventoryDashboardStore = func(stateDirectory string, ownerUID int) (privateInventoryDashboardStore, error) {
		return domainstore.Open(stateDirectory, ownerUID)
	}
	runPrivateInventoryDashboardServer = servePrivateInventoryDashboard
)

func runPrivateInventoryDashboard(args []string) error {
	options, err := parsePrivateInventoryDashboardOptions(args)
	if err != nil {
		return err
	}
	token, err := readPrivateInventoryDashboardToken(options.tokenFile)
	if err != nil {
		return err
	}
	store, err := openPrivateInventoryDashboardStore(options.stateDirectory, os.Geteuid())
	if err != nil {
		return errors.New("open private inventory dashboard state")
	}

	mux := http.NewServeMux()
	api := inventoryview.New(store, store.Scope(), token)
	mux.Handle("/api/v1/inventory", api)
	mux.Handle("/api/v1/inventory/", api)
	mux.Handle("/", inventoryview.PageHandler())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runPrivateInventoryDashboardServer(ctx, options, mux)
}

func parsePrivateInventoryDashboardOptions(args []string) (privateInventoryDashboardOptions, error) {
	options := privateInventoryDashboardOptions{}
	flags := privateFlagSet("private-inventory dashboard")
	flags.StringVar(&options.stateDirectory, "state-dir", "", "absolute private inventory state directory")
	flags.StringVar(&options.listenAddress, "listen-address", defaultPrivateDashboardListen, "IP literal and TCP port")
	flags.StringVar(&options.tokenFile, "token-file", "", "protected local bearer-token file")
	flags.StringVar(&options.tlsCertificate, "tls-cert", "", "absolute TLS certificate file")
	flags.StringVar(&options.tlsKey, "tls-key", "", "absolute TLS private-key file")
	if err := flags.Parse(args); err != nil {
		return privateInventoryDashboardOptions{}, err
	}
	if flags.NArg() != 0 || options.stateDirectory == "" || options.tokenFile == "" {
		return privateInventoryDashboardOptions{}, errors.New("dashboard requires --state-dir ABS --token-file ABS [--listen-address IP:PORT]")
	}
	if !filepath.IsAbs(options.stateDirectory) || filepath.Clean(options.stateDirectory) != options.stateDirectory {
		return privateInventoryDashboardOptions{}, errors.New("--state-dir must be an absolute clean path")
	}
	if !filepath.IsAbs(options.tokenFile) || filepath.Clean(options.tokenFile) != options.tokenFile {
		return privateInventoryDashboardOptions{}, errors.New("--token-file must be an absolute clean path")
	}
	if err := validatePrivateInventoryDashboardListener(options); err != nil {
		return privateInventoryDashboardOptions{}, err
	}
	return options, nil
}

func validatePrivateInventoryDashboardListener(options privateInventoryDashboardOptions) error {
	if strings.TrimSpace(options.listenAddress) != options.listenAddress || options.listenAddress == "" {
		return errors.New("--listen-address must be an IP literal and TCP port")
	}
	host, portText, err := net.SplitHostPort(options.listenAddress)
	if err != nil || host == "" {
		return errors.New("--listen-address must be an IP literal and TCP port")
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return errors.New("--listen-address host must be an IP literal")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != portText {
		return errors.New("--listen-address port must be an integer from 1 through 65535")
	}

	hasCertificate := options.tlsCertificate != ""
	hasKey := options.tlsKey != ""
	if hasCertificate != hasKey {
		return errors.New("--tls-cert and --tls-key must be provided together")
	}
	if hasCertificate {
		if !filepath.IsAbs(options.tlsCertificate) || filepath.Clean(options.tlsCertificate) != options.tlsCertificate ||
			!filepath.IsAbs(options.tlsKey) || filepath.Clean(options.tlsKey) != options.tlsKey {
			return errors.New("--tls-cert and --tls-key must be absolute clean paths")
		}
	}
	if !ip.IsLoopback() && !hasCertificate {
		return errors.New("non-loopback dashboard listeners require TLS certificate and key files")
	}
	return nil
}

func readPrivateInventoryDashboardToken(path string) (string, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return "", errors.New("inspect private inventory dashboard token")
	}
	if !privateDashboardTokenMode(before) {
		return "", errors.New("dashboard token file must be a regular file with mode 0600")
	}
	raw, err := readProtectedPrivateFile(path, maxPrivateDashboardTokenBytes)
	if err != nil {
		return "", err
	}
	after, err := os.Lstat(path)
	if err != nil || !privateDashboardTokenMode(after) || !os.SameFile(before, after) {
		return "", errors.New("dashboard token file changed while reading")
	}

	if len(raw) > 0 && raw[len(raw)-1] == '\n' {
		raw = raw[:len(raw)-1]
		if len(raw) > 0 && raw[len(raw)-1] == '\r' {
			raw = raw[:len(raw)-1]
		}
	}
	if len(raw) < 64 || len(raw)%2 != 0 {
		return "", errors.New("dashboard token must be at least 32 bytes encoded as hexadecimal")
	}
	decoded := make([]byte, hex.DecodedLen(len(raw)))
	if _, err := hex.Decode(decoded, raw); err != nil {
		return "", errors.New("dashboard token must be at least 32 bytes encoded as hexadecimal")
	}
	return string(raw), nil
}

func privateDashboardTokenMode(info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Mode().Perm() == 0o600 && info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0
}

func servePrivateInventoryDashboard(ctx context.Context, options privateInventoryDashboardOptions, handler http.Handler) error {
	if ctx == nil || handler == nil {
		return errors.New("private inventory dashboard server is not configured")
	}

	var tlsConfig *tls.Config
	if options.tlsCertificate != "" {
		certificate, err := tls.LoadX509KeyPair(options.tlsCertificate, options.tlsKey)
		if err != nil {
			return errors.New("load private inventory dashboard TLS identity")
		}
		tlsConfig = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	}
	listener, err := net.Listen("tcp", options.listenAddress)
	if err != nil {
		return fmt.Errorf("listen for private inventory dashboard: %w", err)
	}
	if tlsConfig != nil {
		listener = tls.NewListener(listener, tlsConfig)
	}

	server := &http.Server{
		Addr:              options.listenAddress,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    privateDashboardHeaderBytes,
	}
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()

	var serveErr error
	select {
	case err := <-serveErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
		}
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil && serveErr == nil {
		serveErr = err
	}
	return serveErr
}
