package controlapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/three-b0dy/OpenSurge-for-Linux/internal/config"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/device"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/doctor"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/gateway"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/linuxnet"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/mihomo"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/process"
	"github.com/three-b0dy/OpenSurge-for-Linux/internal/runtime"
)

type Options struct {
	ConfigPath         string
	Addr               string
	StoreDir           string
	Runner             GatewayClient
	ConfigRunner       ConfigurationRunner
	AdminStore         AdminStore
	TLSCertFile        string
	TLSKeyFile         string
	InterfaceInspector linuxnet.InterfaceInspector
	ListInterfaces     func(context.Context) ([]InterfaceOption, error)
	DiscoverNeighbors  func(context.Context, string) ([]linuxnet.Neighbor, error)
	Static             http.Handler
	Credentials        SourceCredentialStore
}

type Server struct {
	configPath         string
	addr               string
	store              *Store
	runner             GatewayClient
	configRunner       ConfigurationRunner
	interfaceInspector linuxnet.InterfaceInspector
	listInterfaces     func(context.Context) ([]InterfaceOption, error)
	discoverNeighbors  func(context.Context, string) ([]linuxnet.Neighbor, error)
	static             http.Handler
	credentials        SourceCredentialStore
	adminStore         AdminStore
	tlsCertFile        string
	tlsKeyFile         string
	managementHost     string
	managementPort     string
	fetchConnections   func(context.Context, config.Config) (mihomo.ConnectionsSnapshot, error)
	fetchProxyHealth   func(context.Context, config.Config) (mihomo.ProxyHealthSnapshot, error)
	measureProxyDelay  func(context.Context, config.Config, string, string, time.Duration) mihomo.ProxyDelayResult
	probeConnectivity  func(context.Context, config.Config, ConnectivityTarget) ConnectivityResult
	trafficSampler     *trafficRateSampler
	baseURL            string

	mu            sync.Mutex
	sessions      map[string]time.Time
	loginFailures map[string][]time.Time
}

const webSessionIdleTimeout = 12 * time.Hour

const (
	loginFailureWindow = 15 * time.Minute
	maxLoginFailures   = 5
)

func New(options Options) (*Server, error) {
	if options.ConfigPath == "" {
		return nil, fmt.Errorf("config path is required")
	}
	configPath, err := filepath.Abs(options.ConfigPath)
	if err != nil {
		return nil, err
	}
	if options.Addr == "" {
		managedConfig, loadErr := config.LoadRuntime(configPath)
		if loadErr != nil {
			return nil, fmt.Errorf("load management configuration: %w", loadErr)
		}
		options.Addr = managedConfig.Management.Listen
		if options.TLSCertFile == "" {
			options.TLSCertFile = managedConfig.Management.TLSCertFile
		}
		if options.TLSKeyFile == "" {
			options.TLSKeyFile = managedConfig.Management.TLSKeyFile
		}
	}
	if options.TLSCertFile == "" || options.TLSKeyFile == "" {
		if managedConfig, loadErr := config.LoadRuntime(configPath); loadErr == nil {
			if options.TLSCertFile == "" {
				options.TLSCertFile = managedConfig.Management.TLSCertFile
			}
			if options.TLSKeyFile == "" {
				options.TLSKeyFile = managedConfig.Management.TLSKeyFile
			}
		}
	}
	host, port, err := parseManagementListener(options.Addr)
	if err != nil {
		return nil, err
	}
	if options.TLSCertFile == "" {
		options.TLSCertFile = DefaultTLSCertFile
	}
	if options.TLSKeyFile == "" {
		options.TLSKeyFile = DefaultTLSKeyFile
	}
	if options.StoreDir == "" {
		options.StoreDir = "/var/lib/opensurge"
	}
	store := NewStore(options.StoreDir)
	if err := store.Ensure(); err != nil {
		return nil, err
	}
	if options.Runner == nil {
		options.Runner = UnixGatewayClient{SocketPath: "/run/opensurge/gateway.sock"}
	}
	if options.ConfigRunner == nil {
		if runner, ok := options.Runner.(ConfigurationRunner); ok {
			options.ConfigRunner = runner
		} else {
			options.ConfigRunner = UnixGatewayClient{SocketPath: "/run/opensurge/gateway.sock"}
		}
	}
	if options.AdminStore == nil {
		options.AdminStore = NewFileAdminStore(store.Dir())
	}
	if options.InterfaceInspector == nil {
		options.InterfaceInspector = linuxnet.NewIPRoute(func(_ context.Context, name string, args ...string) ([]byte, error) {
			return process.Output(name, args...)
		})
	}
	if options.ListInterfaces == nil {
		options.ListInterfaces = func(ctx context.Context) ([]InterfaceOption, error) {
			return listLinuxInterfaces(ctx, options.InterfaceInspector)
		}
	}
	if options.DiscoverNeighbors == nil {
		inspector := options.InterfaceInspector
		options.DiscoverNeighbors = func(ctx context.Context, name string) ([]linuxnet.Neighbor, error) {
			return inspector.Neighbors(ctx, name)
		}
	}
	if options.Credentials == nil {
		fileCredentials := NewFileCredentialStore(store.Dir())
		if err := migrateSourceCredentials(context.Background(), store, fileCredentials); err != nil {
			return nil, err
		}
		if err := migrateLegacyKeychainCredentials(context.Background(), store, fileCredentials, LegacyKeychainCredentialReader{}); err != nil {
			return nil, err
		}
		options.Credentials = fileCredentials
	} else if err := migrateSourceCredentials(context.Background(), store, options.Credentials); err != nil {
		return nil, err
	}
	return &Server{
		configPath:         configPath,
		addr:               options.Addr,
		store:              store,
		runner:             options.Runner,
		configRunner:       options.ConfigRunner,
		interfaceInspector: options.InterfaceInspector,
		listInterfaces:     options.ListInterfaces,
		discoverNeighbors:  options.DiscoverNeighbors,
		static:             options.Static,
		credentials:        options.Credentials,
		adminStore:         options.AdminStore,
		tlsCertFile:        options.TLSCertFile,
		tlsKeyFile:         options.TLSKeyFile,
		managementHost:     host,
		managementPort:     port,
		fetchConnections:   mihomo.FetchConnections,
		fetchProxyHealth:   mihomo.FetchProxyHealth,
		measureProxyDelay:  mihomo.MeasureProxyDelay,
		probeConnectivity:  probeConnectivityTarget,
		trafficSampler:     newTrafficRateSampler(),
		baseURL:            "https://" + options.Addr,
		sessions:           map[string]time.Time{},
		loginFailures:      map[string][]time.Time{},
	}, nil
}

func listLinuxInterfaces(ctx context.Context, inspector linuxnet.InterfaceInspector) ([]InterfaceOption, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	result := make([]InterfaceOption, 0, len(interfaces))
	for _, iface := range interfaces {
		prefixes, err := inspector.Addresses(ctx, iface.Name)
		if err != nil {
			return nil, err
		}
		ipv4 := make([]string, 0, len(prefixes))
		for _, prefix := range prefixes {
			if prefix.Addr().Is4() {
				ipv4 = append(ipv4, prefix.String())
			}
		}
		result = append(result, InterfaceOption{Name: iface.Name, IPv4: ipv4})
	}
	return result, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/setup", s.handleAuthSetup)
	mux.HandleFunc("POST /api/v1/auth/login", s.handleAuthLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", s.handleAuthLogout)
	mux.HandleFunc("GET /api/v1/auth/status", s.handleAuthStatus)
	mux.Handle("GET /api/v1/overview", s.auth(http.HandlerFunc(s.handleOverview)))
	mux.Handle("GET /api/v1/config", s.auth(http.HandlerFunc(s.handleControlConfig)))
	mux.Handle("PUT /api/v1/config", s.auth(http.HandlerFunc(s.handleControlConfig)))
	mux.Handle("POST /api/v1/gateway/start", s.auth(http.HandlerFunc(s.handleGatewayAction)))
	mux.Handle("POST /api/v1/gateway/stop", s.auth(http.HandlerFunc(s.handleGatewayAction)))
	mux.Handle("POST /api/v1/gateway/reload", s.auth(http.HandlerFunc(s.handleGatewayAction)))
	mux.Handle("POST /api/v1/gateway/restart-mihomo", s.auth(http.HandlerFunc(s.handleGatewayAction)))
	mux.Handle("GET /api/v1/recovery", s.auth(http.HandlerFunc(s.handleRecovery)))
	mux.Handle("POST /api/v1/recovery", s.auth(http.HandlerFunc(s.handleRecovery)))
	mux.Handle("POST /api/v1/recovery/router-restored", s.auth(http.HandlerFunc(s.handleRouterRestored)))
	mux.Handle("POST /api/v1/recovery/client-validated", s.auth(http.HandlerFunc(s.handleClientValidated)))
	mux.Handle("POST /api/v1/recovery/client-validation-skip", s.auth(http.HandlerFunc(s.handleClientValidationSkip)))
	mux.Handle("GET /api/v1/network/interfaces", s.auth(http.HandlerFunc(s.handleNetworkInterfaces)))
	mux.Handle("GET /api/v1/sources", s.auth(http.HandlerFunc(s.handleSources)))
	mux.Handle("POST /api/v1/sources", s.auth(http.HandlerFunc(s.handleSources)))
	mux.Handle("POST /api/v1/sources/{id}/refresh", s.auth(http.HandlerFunc(s.handleSourceRefresh)))
	mux.Handle("POST /api/v1/sources/{id}/apply", s.auth(http.HandlerFunc(s.handleSourceApply)))
	mux.Handle("GET /api/v1/device-policy", s.auth(http.HandlerFunc(s.handleDevicePolicy)))
	mux.Handle("PUT /api/v1/device-policy", s.auth(http.HandlerFunc(s.handleDevicePolicy)))
	mux.Handle("GET /api/v1/devices", s.auth(http.HandlerFunc(s.handleDevices)))
	mux.Handle("GET /api/v1/device-traffic", s.auth(http.HandlerFunc(s.handleDeviceTraffic)))
	mux.Handle("POST /api/v1/devices/{device}/selectors/{slot}", s.auth(http.HandlerFunc(s.handleDeviceSelection)))
	mux.Handle("GET /api/v1/policies", s.auth(http.HandlerFunc(s.handlePolicies)))
	mux.Handle("POST /api/v1/policies/{group}/selection", s.auth(http.HandlerFunc(s.handlePolicySelection)))
	mux.Handle("GET /api/v1/proxy-health", s.auth(http.HandlerFunc(s.handleProxyHealth)))
	mux.Handle("POST /api/v1/proxy-health/tests", s.auth(http.HandlerFunc(s.handleProxyHealthTests)))
	mux.Handle("GET /api/v1/connectivity", s.auth(http.HandlerFunc(s.handleConnectivity)))
	mux.Handle("POST /api/v1/connectivity/tests", s.auth(http.HandlerFunc(s.handleConnectivityTests)))
	mux.Handle("GET /api/v1/providers", s.auth(http.HandlerFunc(s.handleProviders)))
	mux.Handle("GET /api/v1/diagnostics", s.auth(http.HandlerFunc(s.handleDiagnostics)))
	mux.Handle("POST /api/v1/providers/{name}/refresh", s.auth(http.HandlerFunc(s.handleProviderRefresh)))
	mux.Handle("GET /api/v1/operations/{id}", s.auth(http.HandlerFunc(s.handleOperation)))
	mux.Handle("GET /api/v1/operations", s.auth(http.HandlerFunc(s.handleOperations)))
	mux.Handle("GET /api/v1/events", s.auth(http.HandlerFunc(s.handleEvents)))
	if s.static != nil {
		mux.Handle("/", s.static)
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "Web UI is not built", http.StatusServiceUnavailable)
		})
	}
	return s.securityHeaders(mux)
}

func (s *Server) handleControlConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.Load(s.configPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, "config_invalid", err.Error())
		return
	}
	revision := fileDigest(s.configPath)
	if r.Method == http.MethodGet {
		w.Header().Set("ETag", `"`+revision+`"`)
		writeJSON(w, http.StatusOK, controlConfigFrom(cfg, revision))
		return
	}
	match := strings.Trim(r.Header.Get("If-Match"), `"`)
	if match == "" || match != revision {
		writeError(w, http.StatusConflict, "revision_conflict", "If-Match must contain the current config revision")
		return
	}
	var input ControlConfig
	if err := decodeJSON(r, &input, 256<<10); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	payload, _ := json.Marshal(input)
	newRevision, err := s.configRunner.ApplyControlConfig(r.Context(), s.configPath, match, payload)
	if err != nil {
		status, code := http.StatusUnprocessableEntity, "config_validation_failed"
		if strings.Contains(err.Error(), "revision conflict") {
			status, code = http.StatusConflict, "revision_conflict"
		}
		if strings.Contains(err.Error(), "must be stopped") {
			status, code = http.StatusConflict, "gateway_running"
		}
		writeError(w, status, code, err.Error())
		return
	}
	cfg, err = config.Load(s.configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "config_reload_failed", err.Error())
		return
	}
	w.Header().Set("ETag", `"`+newRevision+`"`)
	writeJSON(w, http.StatusOK, controlConfigFrom(cfg, newRevision))
}

func controlConfigFrom(cfg config.Config, revision string) ControlConfig {
	dnsUpstream := strings.TrimSpace(cfg.DNS.Upstream)
	if dnsUpstream == "" {
		dnsUpstream = config.MihomoDNSUpstream
	}
	return ControlConfig{
		SchemaVersion: SchemaVersion, Revision: revision,
		Gateway:      GatewayConfigInput{Mode: cfg.Gateway.Mode, Interface: cfg.Gateway.Interface, LANIP: cfg.Gateway.LANIP, UpstreamInterface: cfg.Gateway.UpstreamInterface},
		DHCP:         DHCPConfigInput{Enabled: cfg.DHCP.Enabled, RangeStart: cfg.DHCP.RangeStart, RangeEnd: cfg.DHCP.RangeEnd, LeaseTime: cfg.DHCP.LeaseTime, Domain: cfg.DHCP.Domain},
		DNS:          DNSConfigInput{Listen: cfg.DNS.Listen, Upstream: dnsUpstream},
		Transparent:  TransparentConfigInput{Mode: cfg.Transparent.Mode, StrictRoute: cfg.Transparent.TUNStrictRoute},
		DevicePolicy: DevicePolicyConfigInput{Enabled: cfg.DevicePolicy.File != "", ProtectedIPv4: append([]string{}, cfg.DevicePolicy.ProtectedIPv4...)},
	}
}

func (s *Server) Serve(ctx context.Context) error {
	tlsConfig, err := s.tlsConfigForServe()
	if err != nil {
		return err
	}
	listener, err := tls.Listen("tcp4", s.addr, tlsConfig)
	if err != nil {
		return err
	}
	defer listener.Close()
	return s.serveWithListener(ctx, listener)
}

func (s *Server) serveWithListener(ctx context.Context, listener net.Listener) error {
	descriptor := map[string]any{"schema_version": SchemaVersion, "url": s.baseURL, "pid": os.Getpid()}
	data, _ := json.MarshalIndent(descriptor, "", "  ")
	if err := writeAtomic(filepath.Join(s.store.Dir(), "control-endpoint.json"), append(data, '\n'), 0o600); err != nil {
		return err
	}
	httpServer := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	err := httpServer.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.allowedManagementHost(r.Host) {
			writeError(w, http.StatusForbidden, "invalid_host", "request host is not allowed")
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) allowedManagementHost(value string) bool {
	return value == s.managementHost || value == net.JoinHostPort(s.managementHost, s.managementPort)
}

func parseManagementListener(address string) (string, string, error) {
	host, portText, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return "", "", fmt.Errorf("management listener must be an IPv4 host:port: %w", err)
	}
	ip := net.ParseIP(host).To4()
	if ip == nil || ip.IsUnspecified() || ip.IsLoopback() {
		return "", "", fmt.Errorf("management listener must be a non-loopback IPv4 address")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", "", fmt.Errorf("management listener port must be between 1 and 65535")
	}
	return ip.String(), strconv.Itoa(port), nil
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.sessionAuthenticated(w, r) {
			writeError(w, http.StatusUnauthorized, "authentication_required", "administrator login is required")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			origin := r.Header.Get("Origin")
			if origin != s.baseURL {
				writeError(w, http.StatusForbidden, "origin_rejected", "mutation origin is not allowed")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func setWebSessionCookie(w http.ResponseWriter, session string, now time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     "opensurge_session",
		Value:    session,
		Path:     "/",
		Expires:  now.Add(webSessionIdleTimeout),
		MaxAge:   int(webSessionIdleTimeout / time.Second),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) handleAuthSetup(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r, s.baseURL) {
		writeError(w, http.StatusForbidden, "origin_rejected", "authentication origin is not allowed")
		return
	}
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &request, 16<<10); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	s.purgeAuthState(time.Now())
	if err := s.adminStore.Set(request.Username, request.Password); err != nil {
		if errors.Is(err, ErrAdminAlreadyInitialized) {
			writeError(w, http.StatusConflict, "admin_initialized", "administrator setup has already been completed")
			return
		}
		if strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "at least 12") {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "setup_failed", "administrator setup failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r, s.baseURL) {
		writeError(w, http.StatusForbidden, "origin_rejected", "authentication origin is not allowed")
		return
	}
	ip := requestSourceIP(r)
	if s.loginLocked(ip) {
		writeError(w, http.StatusTooManyRequests, "login_rate_limited", "login temporarily locked for this source address")
		return
	}
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &request, 16<<10); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := s.adminStore.Authenticate(request.Username, request.Password); err != nil {
		if errors.Is(err, ErrAdminNotInitialized) {
			writeError(w, http.StatusConflict, "setup_required", "administrator setup is required")
			return
		}
		if errors.Is(err, ErrInvalidCredentials) {
			s.recordLoginFailure(ip, time.Now())
			writeError(w, http.StatusUnauthorized, "authentication_required", "username or password is incorrect")
			return
		}
		writeError(w, http.StatusInternalServerError, "login_failed", "administrator authentication failed")
		return
	}
	s.clearLoginFailures(ip)
	session := randomToken(32)
	now := time.Now()
	s.mu.Lock()
	s.purgeAuthStateLocked(now)
	s.sessions[session] = now.Add(webSessionIdleTimeout)
	s.mu.Unlock()
	setWebSessionCookie(w, session, now)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r, s.baseURL) {
		writeError(w, http.StatusForbidden, "origin_rejected", "authentication origin is not allowed")
		return
	}
	now := time.Now()
	s.mu.Lock()
	s.purgeAuthStateLocked(now)
	if cookie, err := r.Cookie("opensurge_session"); err == nil {
		delete(s.sessions, cookie.Value)
	}
	s.mu.Unlock()
	clearWebSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	initialized, err := s.adminStore.Initialized()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "auth_status_failed", "administrator status is unavailable")
		return
	}
	authenticated := s.sessionAuthenticated(w, r)
	writeJSON(w, http.StatusOK, AuthStatus{Initialized: initialized, Authenticated: authenticated})
}

func (s *Server) sessionAuthenticated(w http.ResponseWriter, r *http.Request) bool {
	cookie, err := r.Cookie("opensurge_session")
	if err != nil || cookie.Value == "" {
		s.purgeAuthState(time.Now())
		return false
	}
	now := time.Now()
	s.mu.Lock()
	s.purgeAuthStateLocked(now)
	expires, ok := s.sessions[cookie.Value]
	if ok && now.Before(expires) {
		s.sessions[cookie.Value] = now.Add(webSessionIdleTimeout)
	}
	s.mu.Unlock()
	if !ok || !now.Before(expires) {
		return false
	}
	setWebSessionCookie(w, cookie.Value, now)
	return true
}

func (s *Server) purgeAuthState(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeAuthStateLocked(now)
}

func (s *Server) purgeAuthStateLocked(now time.Time) {
	for session, expires := range s.sessions {
		if !now.Before(expires) {
			delete(s.sessions, session)
		}
	}
	for ip, failures := range s.loginFailures {
		kept := failures[:0]
		for _, failure := range failures {
			if now.Sub(failure) < loginFailureWindow {
				kept = append(kept, failure)
			}
		}
		if len(kept) == 0 {
			delete(s.loginFailures, ip)
		} else {
			s.loginFailures[ip] = kept
		}
	}
}

func (s *Server) loginLocked(ip string) bool {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeAuthStateLocked(now)
	return len(s.loginFailures[ip]) >= maxLoginFailures
}

func (s *Server) recordLoginFailure(ip string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeAuthStateLocked(now)
	s.loginFailures[ip] = append(s.loginFailures[ip], now)
}

func (s *Server) clearLoginFailures(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.loginFailures, ip)
}

func requestSourceIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil && host != "" {
		return host
	}
	if value := strings.TrimSpace(r.RemoteAddr); value != "" {
		return value
	}
	return "unknown"
}

func sameOrigin(r *http.Request, expected string) bool {
	return r.Header.Get("Origin") == expected
}

func clearWebSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     "opensurge_session",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) URL() string { return s.baseURL }

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	overview, err := s.overview(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "overview_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, overview)
}

func (s *Server) overview(ctx context.Context) (Overview, error) {
	cfg, desiredErr := config.Load(s.configPath)
	if desiredErr != nil {
		cfg, _ = config.LoadRuntime(s.configPath)
	}
	manager := gateway.New(cfg)
	status, statusErr := manager.Status(ctx)
	report := doctor.Run(cfg)
	controlDoctorChecks := doctorChecksForControl(report.Checks)
	paths := runtime.NewPaths(cfg)
	leases, _ := device.LoadLeases(paths.LeaseFile)
	if leases == nil {
		leases = []device.Client{}
	}
	if cfg.DevicePolicy.Bundle != nil {
		annotateRegisteredLeaseNames(leases, cfg.DevicePolicy.Bundle.Policy)
	}
	recovery, _ := s.store.Recovery()
	desiredDigest := ""
	if cfg.DevicePolicy.Bundle != nil {
		desiredDigest = cfg.DevicePolicy.Bundle.Digest
	}
	desiredProfileDigest, profileDigestErr := config.MihomoProfileDigest(cfg)
	appliedDigest := ""
	appliedProfileDigest := ""
	if state, exists, _ := runtime.LoadState(paths.StateFile); exists {
		appliedDigest = state.DevicePolicyDigest
		appliedProfileDigest = state.ProfileDigest
	}
	warnings := []string{}
	if desiredErr != nil {
		warnings = append(warnings, "desired configuration: "+desiredErr.Error())
	}
	if profileDigestErr != nil {
		warnings = append(warnings, "desired imported profile: "+profileDigestErr.Error())
	}
	groups, groupErr := mihomo.FetchProxyGroups(ctx, cfg)
	if groups == nil {
		groups = []mihomo.ProxyGroup{}
	}
	groups = mihomo.VisibleProxyGroups(groups)
	if groupErr != nil && status.Gateway == "running" {
		warnings = append(warnings, "mihomo policies unavailable: "+groupErr.Error())
	}
	providers, providerErr := mihomo.FetchProviders(ctx, cfg)
	providers = mihomo.VisibleProviders(providers)
	if providers.ProxyProviders == nil {
		providers.ProxyProviders = []mihomo.ProxyProvider{}
	}
	if providers.RuleProviders == nil {
		providers.RuleProviders = []mihomo.RuleProvider{}
	}
	if providerErr != nil && status.Gateway == "running" {
		warnings = append(warnings, "mihomo providers unavailable: "+providerErr.Error())
	}
	if status.TUNError != "" {
		warnings = append(warnings, "mihomo TUN: "+status.TUNError)
	}
	return Overview{
		SchemaVersion:        SchemaVersion,
		Revision:             fileDigest(s.configPath),
		Topology:             cfg.Gateway.Mode,
		DesiredDigest:        desiredDigest,
		AppliedDigest:        appliedDigest,
		DesiredProfileDigest: desiredProfileDigest,
		AppliedProfileDigest: appliedProfileDigest,
		Drift:                desiredDigest != appliedDigest || desiredProfileDigest != appliedProfileDigest,
		Warnings:             warnings,
		Status:               status,
		StatusError:          errorString(statusErr),
		Doctor:               controlDoctorChecks,
		DoctorHealthy:        doctorHealthyForControl(controlDoctorChecks),
		Leases:               leases,
		Policies:             groups,
		Providers:            providers,
		Recovery:             recovery,
	}, nil
}

func (s *Server) handleGatewayAction(w http.ResponseWriter, r *http.Request) {
	action := strings.TrimPrefix(r.URL.Path, "/api/v1/gateway/")
	if action != "start" && action != "stop" && action != "reload" && action != "restart-mihomo" {
		writeError(w, http.StatusNotFound, "not_found", "unknown gateway action")
		return
	}
	id := r.Header.Get("Idempotency-Key")
	if id != "" {
		if existing, err := s.store.Operation(id); err == nil {
			writeJSON(w, http.StatusAccepted, existing)
			return
		}
	}
	cfg, err := config.LoadRuntime(s.configPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, "config_invalid", err.Error())
		return
	}
	recovery, _ := s.store.Recovery()
	if action == "start" && cfg.Gateway.Mode == config.GatewayModeSameWiFiDHCP {
		if recovery.Stage != RecoveryRouterDHCPDisabledConfirmed {
			writeError(w, http.StatusConflict, "recovery_precondition", "same-LAN DHCP takeover requires persisted confirmation that router DHCP is disabled")
			return
		}
	}
	if action == "reload" {
		status, statusErr := gateway.New(cfg).Status(r.Context())
		if statusErr != nil || status.Gateway != "running" {
			writeError(w, http.StatusConflict, "gateway_not_running", "reload requires a running gateway; use start instead")
			return
		}
		if cfg.Gateway.Mode == config.GatewayModeSameWiFiDHCP && recovery.Stage != RecoveryGatewayActive && recovery.Stage != RecoveryClientValidated && recovery.Stage != RecoveryClientValidationSkipped {
			writeError(w, http.StatusConflict, "recovery_precondition", "same-LAN DHCP takeover can reload only while the gateway is active")
			return
		}
	}
	if action == "restart-mihomo" {
		_, exists, stateErr := runtime.LoadState(runtime.NewPaths(cfg).StateFile)
		if stateErr != nil {
			writeError(w, http.StatusInternalServerError, "runtime_state_invalid", stateErr.Error())
			return
		}
		if !exists {
			writeError(w, http.StatusConflict, "gateway_not_running", "restart-mihomo requires an active gateway runtime; use start instead")
			return
		}
		if cfg.Gateway.Mode == config.GatewayModeSameWiFiDHCP && recovery.Stage != RecoveryGatewayActive && recovery.Stage != RecoveryClientValidated && recovery.Stage != RecoveryClientValidationSkipped {
			writeError(w, http.StatusConflict, "recovery_precondition", "same-LAN DHCP takeover can restart mihomo only while the gateway is active")
			return
		}
	}
	if id == "" {
		id = randomToken(12)
	}
	now := time.Now().UTC()
	op := Operation{SchemaVersion: SchemaVersion, ID: id, Kind: action, State: "running", CreatedAt: now, UpdatedAt: now}
	if err := s.store.SaveOperation(op); err != nil {
		writeError(w, http.StatusInternalServerError, "operation_failed", err.Error())
		return
	}
	go s.runOperation(op, cfg.Gateway.Mode, recovery)
	writeJSON(w, http.StatusAccepted, op)
}

func (s *Server) runOperation(op Operation, topology string, recoveryBefore RecoveryState) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	err := s.runner.Run(ctx, GatewayAction(op.Kind), s.configPath)
	op.UpdatedAt = time.Now().UTC()
	if err != nil {
		op.State = "failed"
		op.Error = err.Error()
		if op.Kind == "start" {
			s.recordStartRecoveryFailure(topology, recoveryBefore, err)
		}
		if op.Kind == "reload" {
			s.recordReloadRecoveryFailure(topology, recoveryBefore, err)
		}
	} else {
		op.State = "succeeded"
		if topology == config.GatewayModeSameWiFiDHCP && (op.Kind == "start" || op.Kind == "stop") {
			recovery, _ := s.store.Recovery()
			recovery.Topology = topology
			if op.Kind == "start" {
				recovery.Stage = RecoveryGatewayActive
				recovery.ClientValidationSkipped = false
			} else {
				recovery.Stage = RecoveryGatewayStopped
			}
			recovery.Required = true
			_ = s.store.SaveRecovery(recovery)
		}
	}
	_ = s.store.SaveOperation(op)
}

func (s *Server) recordStartRecoveryFailure(topology string, recoveryBefore RecoveryState, startErr error) {
	if topology != config.GatewayModeSameWiFiDHCP || recoveryBefore.Stage != RecoveryRouterDHCPDisabledConfirmed {
		return
	}
	cfg, err := config.LoadRuntime(s.configPath)
	if err != nil {
		return
	}
	if _, exists, stateErr := runtime.LoadState(runtime.NewPaths(cfg).StateFile); stateErr != nil || exists {
		return
	}
	recoveryBefore.Required = true
	appendRecoveryNote(&recoveryBefore, "gateway start failed and runtime changes were rolled back; router DHCP may remain disabled; resolve the error and retry, or abandon takeover and recover the LAN: "+startErr.Error())
	_ = s.store.SaveRecovery(recoveryBefore)
}

func (s *Server) recordReloadRecoveryFailure(topology string, recoveryBefore RecoveryState, reloadErr error) {
	if topology != config.GatewayModeSameWiFiDHCP || strings.Contains(reloadErr.Error(), "reload stop failed") {
		return
	}
	cfg, err := config.LoadRuntime(s.configPath)
	if err != nil {
		return
	}
	_, exists, stateErr := runtime.LoadState(runtime.NewPaths(cfg).StateFile)
	restartFailed := strings.Contains(reloadErr.Error(), "reload start failed after gateway stop")
	if stateErr != nil || (!restartFailed && exists) {
		return
	}
	recoveryBefore.Topology = topology
	recoveryBefore.Stage = RecoveryRouterDHCPDisabledConfirmed
	recoveryBefore.Required = true
	if recoveryBefore.RecoveryNotes != "" {
		recoveryBefore.RecoveryNotes += "; "
	}
	recoveryBefore.RecoveryNotes += "gateway reload failed after services stopped; router DHCP remains disabled; retry start or recover the LAN"
	_ = s.store.SaveRecovery(recoveryBefore)
}

func (s *Server) handleOperation(w http.ResponseWriter, r *http.Request) {
	op, err := s.store.Operation(r.PathValue("id"))
	if errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusNotFound, "operation_not_found", "operation not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "operation_invalid", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, op)
}

func appendRecoveryNote(state *RecoveryState, note string) {
	if state.RecoveryNotes != "" {
		state.RecoveryNotes += "; "
	}
	state.RecoveryNotes += note
}

func (s *Server) handleOperations(w http.ResponseWriter, _ *http.Request) {
	operations, err := s.store.Operations(50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "operations_read_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema_version": SchemaVersion, "operations": operations})
}

func (s *Server) handleRecovery(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		state, err := s.store.Recovery()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "recovery_read_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, state)
		return
	}
	var update RecoveryUpdate
	if err := decodeJSON(r, &update, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	current, err := s.store.Recovery()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "recovery_read_failed", err.Error())
		return
	}
	if update.Stage != "" && update.Stage != current.Stage && !allowedRecoveryTransition(current.Stage, update.Stage) {
		writeError(w, http.StatusConflict, "recovery_transition_rejected", "the requested confirmation is not valid for the current gateway checklist stage")
		return
	}
	if update.Stage == RecoveryRouterDHCPDisabledConfirmed {
		cfg, loadErr := config.LoadRuntime(s.configPath)
		if loadErr != nil {
			writeError(w, http.StatusBadRequest, "config_invalid", loadErr.Error())
			return
		}
		if cfg.Gateway.Mode != config.GatewayModeSameWiFiDHCP {
			writeError(w, http.StatusConflict, "same_wifi_config_required", "router DHCP confirmation is only used by same_wifi_dhcp")
			return
		}
	}
	if update.Stage != "" {
		current.Stage = update.Stage
		current.Required = update.Stage != RecoveryIdle && update.Stage != RecoveryComplete
	}
	if update.RecoveryNotes != "" {
		current.RecoveryNotes = update.RecoveryNotes
	}
	if err := s.store.SaveRecovery(current); err != nil {
		writeError(w, http.StatusInternalServerError, "recovery_write_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, current)
}

func allowedRecoveryTransition(from, to string) bool {
	if from == "" {
		from = RecoveryIdle
	}
	if to == RecoveryIdle {
		return true
	}
	switch from {
	case RecoveryIdle, RecoveryComplete:
		return to == RecoveryRouterDHCPDisabledConfirmed
	case RecoveryRouterDHCPDisabledConfirmed:
		return to == RecoveryGatewayActive
	case RecoveryGatewayActive:
		return to == RecoveryClientValidated || to == RecoveryClientValidationSkipped || to == RecoveryGatewayStopped
	case RecoveryClientValidated, RecoveryClientValidationSkipped:
		return to == RecoveryGatewayStopped
	case RecoveryGatewayStopped:
		return to == RecoveryRouterDHCPRestored
	case RecoveryRouterDHCPRestored:
		return to == RecoveryComplete
	default:
		return false
	}
}

func (s *Server) handleNetworkInterfaces(w http.ResponseWriter, r *http.Request) {
	interfaces, err := s.listInterfaces(r.Context())
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "network_interfaces_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, NetworkInterfacesResponse{SchemaVersion: SchemaVersion, Interfaces: interfaces})
}

func (s *Server) handleRouterRestored(w http.ResponseWriter, r *http.Request) {
	state, _ := s.store.Recovery()
	if state.Stage != RecoveryGatewayStopped {
		writeError(w, http.StatusConflict, "recovery_precondition", "stop the gateway before recording router DHCP restoration")
		return
	}
	var confirmation struct {
		Confirmed bool `json:"router_dhcp_restored_confirmed"`
	}
	if err := decodeJSON(r, &confirmation, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !confirmation.Confirmed {
		writeError(w, http.StatusUnprocessableEntity, "router_dhcp_confirmation_required", "confirm router DHCP was restored in the router administration interface")
		return
	}
	state.Stage = RecoveryRouterDHCPRestored
	state.Required = true
	appendRecoveryNote(&state, "router DHCP restoration was confirmed by the operator; OpenSurge did not modify the upstream router")
	if err := s.store.SaveRecovery(state); err != nil {
		writeError(w, http.StatusInternalServerError, "recovery_write_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, NetworkActionResponse{SchemaVersion: SchemaVersion, Recovery: state})
}

func (s *Server) handleClientValidated(w http.ResponseWriter, r *http.Request) {
	state, _ := s.store.Recovery()
	if state.Stage != RecoveryGatewayActive {
		writeError(w, http.StatusConflict, "recovery_precondition", "gateway must be active before recording client validation")
		return
	}
	var request ClientAcceptanceRequest
	if err := decodeJSON(r, &request, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !request.GatewayDNSConfirmed || !request.NoExplicitProxyConfirmed {
		writeError(w, http.StatusUnprocessableEntity, "client_confirmation_required", "confirm client gateway/DNS and no explicit proxy")
		return
	}
	cfg, err := config.LoadRuntime(s.configPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, "config_invalid", err.Error())
		return
	}
	if err := validateClientAcceptance(cfg, request.ClientIPv4); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "client_acceptance_failed", err.Error())
		return
	}
	state.Stage = RecoveryClientValidated
	state.ClientValidationSkipped = false
	state.RecoveryNotes = fmt.Sprintf("client %s: DHCP, DNS, TUN, and no explicit proxy evidence confirmed", request.ClientIPv4)
	state.Required = true
	if err := s.store.SaveRecovery(state); err != nil {
		writeError(w, http.StatusInternalServerError, "recovery_write_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, NetworkActionResponse{SchemaVersion: SchemaVersion, Recovery: state})
}

func (s *Server) handleClientValidationSkip(w http.ResponseWriter, r *http.Request) {
	request := ClientValidationSkipRequest{}
	if err := decodeJSON(r, &request, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !request.SkipConfirmed {
		writeError(w, http.StatusUnprocessableEntity, "skip_confirmation_required", "confirm that client DHCP, DNS, and TUN evidence will not be validated")
		return
	}
	state, _ := s.store.Recovery()
	if state.Stage != RecoveryGatewayActive {
		writeError(w, http.StatusConflict, "recovery_precondition", "gateway must be active before skipping client validation")
		return
	}
	state.Stage = RecoveryClientValidationSkipped
	state.ClientValidationSkipped = true
	appendRecoveryNote(&state, "client DHCP, DNS, and TUN acceptance explicitly skipped by operator; no client-path validation evidence was collected")
	state.Required = true
	if err := s.store.SaveRecovery(state); err != nil {
		writeError(w, http.StatusInternalServerError, "recovery_write_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, NetworkActionResponse{SchemaVersion: SchemaVersion, Recovery: state})
}

func validateClientAcceptance(cfg config.Config, clientIPv4 string) error {
	if net.ParseIP(clientIPv4).To4() == nil {
		return fmt.Errorf("client_ipv4 must be valid IPv4")
	}
	paths := runtime.NewPaths(cfg)
	leases, err := device.LoadLeases(paths.LeaseFile)
	if err != nil {
		return fmt.Errorf("load leases: %w", err)
	}
	for _, lease := range leases {
		if lease.IP == clientIPv4 && lease.Online {
			return nil
		}
	}
	return fmt.Errorf("no active DHCP lease for %s", clientIPv4)
}

func (s *Server) handleSources(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		sources, err := s.store.Sources()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "sources_failed", err.Error())
			return
		}
		sources = s.decorateSourceStates(sources)
		revision := fileDigest(s.configPath)
		w.Header().Set("ETag", `"`+revision+`"`)
		writeJSON(w, http.StatusOK, map[string]any{"schema_version": SchemaVersion, "revision": revision, "sources": publicSources(sources)})
		return
	}
	var source Source
	var err error
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if err = r.ParseMultipartForm(maxSourceSize); err == nil {
			file, header, fileErr := r.FormFile("file")
			if fileErr != nil {
				err = fileErr
			} else {
				defer file.Close()
				name := r.FormValue("name")
				if name == "" {
					name = header.Filename
				}
				source, err = s.importReader(name, r.FormValue("kind"), "file:"+header.Filename, io.LimitReader(file, maxSourceSize+1))
			}
		}
	} else {
		var req SourceImportRequest
		if err = decodeJSON(r, &req, 64<<10); err == nil {
			source, err = s.importURL(r.Context(), req)
		}
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "source_import_failed", err.Error())
		return
	}
	source.SnapshotPath = ""
	source.FetchURL = ""
	writeJSON(w, http.StatusCreated, source)
}

func (s *Server) handleSourceRefresh(w http.ResponseWriter, r *http.Request) {
	source, err := s.sourceByID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "source_not_found", err.Error())
		return
	}
	fetchURL, credentialErr := s.credentials.Get(r.Context(), source.ID)
	if credentialErr != nil || !strings.HasPrefix(fetchURL, "https://") {
		writeError(w, http.StatusConflict, "source_not_refreshable", "only HTTPS sources can be refreshed")
		return
	}
	refreshed, err := s.importURL(r.Context(), SourceImportRequest{Name: source.Name, Kind: source.Kind, URL: fetchURL})
	if err != nil {
		writeError(w, http.StatusBadRequest, "source_refresh_failed", err.Error())
		return
	}
	refreshed.SnapshotPath = ""
	refreshed.FetchURL = ""
	writeJSON(w, http.StatusOK, refreshed)
}

func (s *Server) handleSourceApply(w http.ResponseWriter, r *http.Request) {
	source, err := s.sourceByID(r.PathValue("id"))
	if err != nil || !source.Valid || source.Kind != "mihomo_profile" {
		writeError(w, http.StatusConflict, "source_not_applicable", "source must be a structurally valid mihomo profile")
		return
	}
	cfg, err := config.Load(s.configPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, "config_invalid", err.Error())
		return
	}
	paths := runtime.NewPaths(cfg)
	_, gatewayActive, stateErr := runtime.LoadState(paths.StateFile)
	if stateErr != nil {
		writeError(w, http.StatusInternalServerError, "runtime_state_invalid", stateErr.Error())
		return
	}
	recovery, _ := s.store.Recovery()
	if gatewayActive && cfg.Gateway.Mode == config.GatewayModeSameWiFiDHCP && recovery.Stage != RecoveryGatewayActive && recovery.Stage != RecoveryClientValidated && recovery.Stage != RecoveryClientValidationSkipped {
		writeError(w, http.StatusConflict, "recovery_precondition", "same-LAN DHCP takeover can apply a profile only while the gateway is active")
		return
	}
	match := strings.Trim(r.Header.Get("If-Match"), `"`)
	if match == "" || match != fileDigest(s.configPath) {
		writeError(w, http.StatusConflict, "revision_conflict", "If-Match must contain the current config revision")
		return
	}
	payload, err := os.ReadFile(source.SnapshotPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "source_snapshot_unavailable", err.Error())
		return
	}
	// The privileged configuration runner performs the authoritative mihomo
	// engine validation after writing the candidate beside the persistent
	// geodata cache. Validating here as the UI user would download the same
	// assets into every source snapshot directory, then validate a second time
	// in the privileged gateway service before applying.
	var operation *Operation
	if gatewayActive {
		now := time.Now().UTC()
		op := Operation{SchemaVersion: SchemaVersion, ID: randomToken(12), Kind: "reload", State: "running", CreatedAt: now, UpdatedAt: now}
		if err := s.store.SaveOperation(op); err != nil {
			writeError(w, http.StatusInternalServerError, "operation_failed", err.Error())
			return
		}
		operation = &op
	}
	result, err := s.configRunner.ApplyProfile(r.Context(), s.configPath, match, payload)
	if err == nil && gatewayActive && !result.Reloaded {
		err = fmt.Errorf("running gateway did not reload the selected profile")
	}
	if operation != nil {
		operation.UpdatedAt = time.Now().UTC()
		if err != nil {
			operation.State = "failed"
			operation.Error = err.Error()
		} else {
			operation.State = "succeeded"
		}
		_ = s.store.SaveOperation(*operation)
	}
	if err != nil {
		if gatewayActive {
			s.recordReloadRecoveryFailure(cfg.Gateway.Mode, recovery, err)
		}
		status := http.StatusInternalServerError
		code := "config_apply_failed"
		if strings.Contains(err.Error(), "revision conflict") {
			status, code = http.StatusConflict, "revision_conflict"
		} else if strings.Contains(err.Error(), "mihomo config validation failed") {
			status, code = http.StatusUnprocessableEntity, "mihomo_validation_failed"
		} else if strings.Contains(err.Error(), "apply profile reload failed") {
			status, code = http.StatusConflict, "profile_reload_failed"
		} else if strings.Contains(err.Error(), "did not reload the selected profile") {
			status, code = http.StatusInternalServerError, "profile_reload_incomplete"
		}
		writeError(w, status, code, err.Error())
		return
	}
	sources, _ := s.store.Sources()
	sources = s.decorateSourceStates(sources)
	_ = s.store.SaveSources(sources)
	for _, candidate := range sources {
		if candidate.ID == source.ID {
			source = candidate
			break
		}
	}
	source.SnapshotPath = ""
	source.FetchURL = ""
	w.Header().Set("ETag", `"`+result.Revision+`"`)
	writeJSON(w, http.StatusOK, source)
}

func (s *Server) handleDevicePolicy(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadRuntime(s.configPath)
	if err != nil || cfg.DevicePolicy.File == "" {
		writeError(w, http.StatusConflict, "device_policy_unconfigured", "device_policy.file is not configured")
		return
	}
	if r.Method == http.MethodGet {
		bundle, err := device.LoadPolicyBundle(cfg.DevicePolicy.File)
		if err != nil {
			writeError(w, http.StatusBadRequest, "device_policy_invalid", err.Error())
			return
		}
		w.Header().Set("ETag", `"`+bundle.Digest+`"`)
		writeJSON(w, http.StatusOK, DevicePolicyResponse{SchemaVersion: SchemaVersion, Revision: bundle.Digest, Policy: bundle.Policy})
		return
	}
	current, err := device.LoadPolicyBundle(cfg.DevicePolicy.File)
	if err != nil {
		writeError(w, http.StatusBadRequest, "device_policy_invalid", err.Error())
		return
	}
	match := strings.Trim(r.Header.Get("If-Match"), `"`)
	if match == "" || match != current.Digest {
		writeError(w, http.StatusConflict, "revision_conflict", "If-Match must contain the current device policy revision")
		return
	}
	var policy device.PolicySet
	if err := decodeJSON(r, &policy, maxSourceSize); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := device.ValidatePolicySetForLANWithProtectedForIPOnlyMode(policy, cfg.Gateway.LANIP, cfg.DevicePolicy.ProtectedIPv4, cfg.Gateway.Mode == config.GatewayModeSameLAN); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "device_policy_validation_failed", err.Error())
		return
	}
	if _, err := device.CompilePolicyBundleForIPOnlyMode(policy, cfg.Gateway.Mode == config.GatewayModeSameLAN); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "device_policy_compile_failed", err.Error())
		return
	}
	data, _ := json.Marshal(policy)
	newRevision, err := s.configRunner.ApplyDevicePolicy(r.Context(), s.configPath, match, data)
	if err != nil {
		status := http.StatusInternalServerError
		code := "device_policy_write_failed"
		if strings.Contains(err.Error(), "revision conflict") {
			status, code = http.StatusConflict, "revision_conflict"
		}
		writeError(w, status, code, err.Error())
		return
	}
	w.Header().Set("ETag", `"`+newRevision+`"`)
	writeJSON(w, http.StatusOK, DevicePolicyResponse{SchemaVersion: SchemaVersion, Revision: newRevision, Policy: policy})
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadRuntime(s.configPath)
	if err != nil || cfg.DevicePolicy.File == "" {
		writeJSON(w, http.StatusOK, DevicesResponse{SchemaVersion: SchemaVersion, Devices: []device.CompiledDevice{}, Leases: []device.Client{}, ObservedDevices: []ObservedDevice{}})
		return
	}
	desired, err := device.LoadPolicyBundleForIPOnlyMode(cfg.DevicePolicy.File, cfg.Gateway.Mode == config.GatewayModeSameLAN)
	if err != nil {
		writeError(w, http.StatusBadRequest, "device_policy_invalid", err.Error())
		return
	}
	paths := runtime.NewPaths(cfg)
	leases, _ := device.LoadLeases(paths.LeaseFile)
	if leases == nil {
		leases = []device.Client{}
	}
	response := DevicesResponse{
		SchemaVersion: SchemaVersion,
		DesiredDigest: desired.Digest,
		Drift:         len(desired.Policy.Devices) > 0,
		Devices:       desired.Compiled.Devices,
		DesiredDevices: append([]device.CompiledDevice(nil),
			desired.Compiled.Devices...),
		AppliedDevices:  []device.CompiledDevice{},
		ChangedDevices:  []string{},
		Leases:          leases,
		ObservedDevices: []ObservedDevice{},
	}
	if response.Devices == nil {
		response.Devices = []device.CompiledDevice{}
	}
	if state, exists, _ := runtime.LoadState(paths.StateFile); exists && state.DevicePolicyDigest != "" {
		response.Applied = true
		response.AppliedDigest = state.DevicePolicyDigest
		response.Drift = state.DevicePolicyDigest != desired.Digest
		if applied, err := device.LoadPolicyBundleSnapshot(paths.DevicePolicyApplied); err == nil {
			response.Devices = applied.Compiled.Devices
			response.AppliedDevices = append([]device.CompiledDevice(nil), applied.Compiled.Devices...)
			response.ChangedDevices = changedDeviceIDs(desired.Policy, applied.Policy)
			if response.Devices == nil {
				response.Devices = []device.CompiledDevice{}
			}
		}
	}
	if cfg.Gateway.Mode == config.GatewayModeSameLAN {
		connections, connectionErr := s.fetchConnections(r.Context(), cfg)
		neighbors, neighborErr := s.discoverNeighbors(r.Context(), cfg.Gateway.Interface)
		response.ObservedDevices = observedLANDevices(connections, neighbors, cfg.Gateway.LANIP, desired.Policy.Devices...)
		observationErrors := []string{}
		if connectionErr != nil {
			observationErrors = append(observationErrors, "mihomo connections: "+connectionErr.Error())
		}
		if neighborErr != nil {
			observationErrors = append(observationErrors, "Linux neighbor table: "+neighborErr.Error())
		}
		response.ObservationError = strings.Join(observationErrors, "; ")
	}
	writeJSON(w, http.StatusOK, response)
}

func changedDeviceIDs(desired, applied device.PolicySet) []string {
	appliedIDs := make(map[string]struct{}, len(applied.Devices))
	for _, managed := range applied.Devices {
		appliedIDs[managed.ID] = struct{}{}
	}
	changed := []string{}
	for _, managed := range desired.Devices {
		if _, exists := appliedIDs[managed.ID]; !exists {
			continue
		}
		desiredProjection, desiredErr := compiledDeviceProjection(desired, managed.ID)
		appliedProjection, appliedErr := compiledDeviceProjection(applied, managed.ID)
		if desiredErr != nil || appliedErr != nil || desiredProjection != appliedProjection {
			changed = append(changed, managed.ID)
		}
	}
	return changed
}

func compiledDeviceProjection(policy device.PolicySet, id string) (string, error) {
	managed := make([]device.ManagedDevice, 0, 1)
	for _, candidate := range policy.Devices {
		if candidate.ID == id {
			managed = append(managed, candidate)
			break
		}
	}
	policy.Devices = managed
	compiled, err := device.CompilePolicySet(policy)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(compiled)
	return string(payload), err
}

func (s *Server) handlePolicies(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadRuntime(s.configPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, "config_invalid", err.Error())
		return
	}
	groups, err := mihomo.FetchProxyGroups(r.Context(), cfg)
	if err != nil {
		writeError(w, http.StatusBadGateway, "mihomo_unavailable", err.Error())
		return
	}
	groups = mihomo.VisibleProxyGroups(groups)
	writeJSON(w, http.StatusOK, map[string]any{"schema_version": SchemaVersion, "groups": groups})
}

func (s *Server) handlePolicySelection(w http.ResponseWriter, r *http.Request) {
	var req SelectionRequest
	if err := decodeJSON(r, &req, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	cfg, err := config.LoadRuntime(s.configPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, "config_invalid", err.Error())
		return
	}
	group := r.PathValue("group")
	groups, err := mihomo.FetchProxyGroups(r.Context(), cfg)
	groups = mihomo.VisibleProxyGroups(groups)
	if err != nil || !validSelection(groups, group, req.Policy) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_selection", "group or policy is not available")
		return
	}
	if err := mihomo.SelectProxyGroup(r.Context(), cfg, group, req.Policy); err != nil {
		writeError(w, http.StatusBadGateway, "selection_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema_version": SchemaVersion, "group": group, "selected": req.Policy})
}

func (s *Server) handleDeviceSelection(w http.ResponseWriter, r *http.Request) {
	var req SelectionRequest
	if err := decodeJSON(r, &req, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	cfg, err := config.LoadRuntime(s.configPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, "config_invalid", err.Error())
		return
	}
	paths := runtime.NewPaths(cfg)
	bundle, err := device.LoadPolicyBundleSnapshot(paths.DevicePolicyApplied)
	if err != nil {
		writeError(w, http.StatusConflict, "device_policy_not_applied", err.Error())
		return
	}
	group, err := device.DeviceGroupFromCompiled(bundle.Compiled, r.PathValue("device"), r.PathValue("slot"))
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_device_slot", err.Error())
		return
	}
	groups, err := mihomo.FetchProxyGroups(r.Context(), cfg)
	if err != nil || !validSelection(groups, group, req.Policy) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_selection", "device policy is not available")
		return
	}
	if err := mihomo.SelectProxyGroup(r.Context(), cfg, group, req.Policy); err != nil {
		writeError(w, http.StatusBadGateway, "selection_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema_version": SchemaVersion, "device": r.PathValue("device"), "slot": r.PathValue("slot"), "group": group, "selected": req.Policy})
}

func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadRuntime(s.configPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, "config_invalid", err.Error())
		return
	}
	providers, err := mihomo.FetchProviders(r.Context(), cfg)
	if err != nil {
		writeError(w, http.StatusBadGateway, "mihomo_unavailable", err.Error())
		return
	}
	providers = mihomo.VisibleProviders(providers)
	writeJSON(w, http.StatusOK, map[string]any{"schema_version": SchemaVersion, "providers": providers})
}

func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadRuntime(s.configPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, "config_invalid", err.Error())
		return
	}
	paths := runtime.NewPaths(cfg)
	connections, connectionErr := s.fetchConnections(r.Context(), cfg)
	if connections.Connections == nil {
		connections.Connections = []mihomo.Connection{}
	}
	logs := map[string][]string{
		"mihomo":  tailLines(paths.MihomoLog, 80, cfg),
		"dnsmasq": tailLines(paths.DNSMasqLog, 80, cfg),
	}
	operations, _ := s.store.Operations(20)
	recovery, _ := s.store.Recovery()
	writeJSON(w, http.StatusOK, DiagnosticsResponse{SchemaVersion: SchemaVersion, Revision: fileDigest(s.configPath), Connections: connections, ConnectionError: errorString(connectionErr), Logs: logs, Operations: operations, Recovery: recovery})
}

func tailLines(path string, limit int, cfg config.Config) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return []string{}
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	for index := range lines {
		for _, secret := range []string{cfg.Mihomo.Secret, cfg.UpstreamProxy.Password, cfg.UpstreamProxy.Username} {
			if secret != "" {
				lines[index] = strings.ReplaceAll(lines[index], secret, "[REDACTED]")
			}
		}
	}
	return lines
}

func (s *Server) handleProviderRefresh(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadRuntime(s.configPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, "config_invalid", err.Error())
		return
	}
	name := r.PathValue("name")
	provider, err := mihomo.UpdateProxyProvider(r.Context(), cfg, name)
	if err != nil {
		writeError(w, http.StatusBadGateway, "provider_refresh_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema_version": SchemaVersion, "provider": provider})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusNotImplemented, "streaming_unavailable", "streaming is unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	stateTicker := time.NewTicker(2 * time.Second)
	heartbeat := time.NewTicker(15 * time.Second)
	defer stateTicker.Stop()
	defer heartbeat.Stop()
	last := ""
	sendState := func() {
		state, err := s.stateEvent(r.Context())
		if err != nil {
			return
		}
		signature, _ := json.Marshal(state)
		if string(signature) == last {
			return
		}
		last = string(signature)
		state.At = time.Now().UTC()
		data, _ := json.Marshal(state)
		fmt.Fprintf(w, "event: state\ndata: %s\n\n", data)
		flusher.Flush()
	}
	sendState()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-stateTicker.C:
			sendState()
		case now := <-heartbeat.C:
			fmt.Fprintf(w, "event: heartbeat\ndata: {\"at\":%q}\n\n", now.UTC().Format(time.RFC3339))
			flusher.Flush()
		}
	}
}

func (s *Server) stateEvent(ctx context.Context) (StateEvent, error) {
	cfg, err := config.LoadRuntime(s.configPath)
	if err != nil {
		return StateEvent{}, err
	}
	status, _ := gateway.New(cfg).Status(ctx)
	paths := runtime.NewPaths(cfg)
	desired := ""
	if cfg.DevicePolicy.File != "" {
		if bundle, err := device.LoadPolicyBundle(cfg.DevicePolicy.File); err == nil {
			desired = bundle.Digest
		}
	}
	applied := ""
	desiredProfile, _ := config.MihomoProfileDigest(cfg)
	appliedProfile := ""
	if state, exists, _ := runtime.LoadState(paths.StateFile); exists {
		applied = state.DevicePolicyDigest
		appliedProfile = state.ProfileDigest
	}
	recovery, _ := s.store.Recovery()
	return StateEvent{SchemaVersion: SchemaVersion, Revision: fileDigest(s.configPath), Gateway: status.Gateway, DesiredDigest: desired, AppliedDigest: applied, DesiredProfileDigest: desiredProfile, AppliedProfileDigest: appliedProfile, Drift: desired != applied || desiredProfile != appliedProfile, Recovery: recovery}, nil
}

func (s *Server) sourceByID(id string) (Source, error) {
	sources, err := s.store.Sources()
	if err != nil {
		return Source{}, err
	}
	for _, source := range sources {
		if source.ID == id {
			return source, nil
		}
	}
	return Source{}, fmt.Errorf("source %q not found", id)
}

func publicSources(sources []Source) []Source {
	result := append([]Source{}, sources...)
	for i := range result {
		result[i].Inventory = normalizeInventory(result[i].Inventory)
		if result[i].Versions == nil {
			result[i].Versions = []SourceVersion{}
		}
		if result[i].Diff.ProxiesAdded == nil {
			result[i].Diff = diffInventory(result[i].Diff.PreviousDigest, Inventory{}, Inventory{})
		}
		result[i].SnapshotPath = ""
		result[i].FetchURL = ""
		for version := range result[i].Versions {
			result[i].Versions[version].Inventory = normalizeInventory(result[i].Versions[version].Inventory)
			result[i].Versions[version].SnapshotPath = ""
		}
	}
	return result
}

func validSelection(groups []mihomo.ProxyGroup, group, policy string) bool {
	for _, candidate := range groups {
		if candidate.Name != group {
			continue
		}
		for _, option := range candidate.Options {
			if option == policy {
				return true
			}
		}
	}
	return false
}

func doctorHealthyForControl(checks []doctor.Check) bool {
	for _, check := range checks {
		if !check.OK {
			return false
		}
	}
	return true
}

func doctorChecksForControl(checks []doctor.Check) []doctor.Check {
	result := make([]doctor.Check, 0, len(checks))
	for _, check := range checks {
		if check.Name != "root privileges" {
			result = append(result, check)
		}
	}
	return result
}

func fileDigest(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func decodeJSON(r *http.Request, value any, limit int64) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, limit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("expected one JSON document")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, ErrorResponse{SchemaVersion: SchemaVersion, Error: APIError{Code: code, Message: message}})
}

func randomToken(size int) string {
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		panic(err)
	}
	return hex.EncodeToString(data)
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
