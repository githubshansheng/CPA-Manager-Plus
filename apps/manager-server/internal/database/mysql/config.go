package mysql

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	driver "github.com/go-sql-driver/mysql"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	TLSVerifyIdentity = "verify_identity"
	TLSDisabled       = "disabled"
)

var databaseNamePattern = regexp.MustCompile(`^[A-Za-z0-9_$-]{1,64}$`)

type Config struct {
	Host            string        `json:"host"`
	Port            int           `json:"port"`
	Database        string        `json:"database"`
	Username        string        `json:"username"`
	Password        string        `json:"-"`
	TLSMode         string        `json:"tlsMode"`
	TLSCAPath       string        `json:"tlsCaPath,omitempty"`
	TLSCertPath     string        `json:"tlsCertPath,omitempty"`
	TLSKeyPath      string        `json:"tlsKeyPath,omitempty"`
	ConnectTimeout  time.Duration `json:"connectTimeout,omitempty"`
	ReadTimeout     time.Duration `json:"readTimeout,omitempty"`
	WriteTimeout    time.Duration `json:"writeTimeout,omitempty"`
	MaxOpenConns    int           `json:"maxOpenConns,omitempty"`
	MaxIdleConns    int           `json:"maxIdleConns,omitempty"`
	ConnMaxIdleTime time.Duration `json:"connMaxIdleTime,omitempty"`
}
type RedactedConfig struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Database    string `json:"database"`
	Username    string `json:"username"`
	TLSMode     string `json:"tlsMode"`
	TLSCAPath   string `json:"tlsCaPath,omitempty"`
	TLSCertPath string `json:"tlsCertPath,omitempty"`
	TLSKeyPath  string `json:"tlsKeyPath,omitempty"`
	HasPassword bool   `json:"hasPassword"`
}

func (c Config) Redacted() RedactedConfig {
	c = c.withDefaults()
	return RedactedConfig{Host: maskHost(c.Host), Port: c.Port, Database: c.Database, Username: c.Username,
		TLSMode: c.TLSMode, TLSCAPath: c.TLSCAPath, TLSCertPath: c.TLSCertPath,
		TLSKeyPath: c.TLSKeyPath, HasPassword: c.Password != ""}
}
func (c Config) Endpoint() string {
	c = c.withDefaults()
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}
func (c Config) MaskedEndpoint() string {
	c = c.withDefaults()
	return net.JoinHostPort(maskHost(c.Host), strconv.Itoa(c.Port))
}
func maskHost(host string) string {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if strings.EqualFold(host, "localhost") {
		return host
	}
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return fmt.Sprintf("%d.%d.%d.***", v4[0], v4[1], v4[2])
		}
		parts := strings.Split(host, ":")
		if len(parts) > 2 {
			return parts[0] + ":" + parts[1] + ":…"
		}
		return "…"
	}
	labels := strings.Split(host, ".")
	if len(labels) > 1 {
		labels[0] = "***"
		return strings.Join(labels, ".")
	}
	if len(host) <= 1 {
		return "*"
	}
	return host[:1] + strings.Repeat("*", min(len(host)-1, 6))
}
func (c Config) Validate() error {
	c = c.withDefaults()
	if strings.TrimSpace(c.Host) == "" {
		return errors.New("mysql host is required")
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("mysql port %d is outside 1..65535", c.Port)
	}
	if strings.TrimSpace(c.Username) == "" {
		return errors.New("mysql username is required")
	}
	if !databaseNamePattern.MatchString(c.Database) {
		return errors.New("mysql database must be 1-64 ASCII letters, digits, _, $, or -")
	}
	if c.TLSMode != TLSVerifyIdentity && c.TLSMode != TLSDisabled {
		return fmt.Errorf("unsupported mysql TLS mode %q", c.TLSMode)
	}
	if (c.TLSCertPath == "") != (c.TLSKeyPath == "") {
		return errors.New("mysql TLS certificate and key must be configured together")
	}
	for name, value := range map[string]time.Duration{
		"connect timeout": c.ConnectTimeout, "read timeout": c.ReadTimeout,
		"write timeout": c.WriteTimeout, "connection max idle time": c.ConnMaxIdleTime,
	} {
		if value < 0 {
			return fmt.Errorf("mysql %s cannot be negative", name)
		}
	}
	if c.MaxOpenConns < 0 || c.MaxIdleConns < 0 {
		return errors.New("mysql connection limits cannot be negative")
	}
	return nil
}

// DSN returns a credential-bearing driver string for server-side use only.
// It must never be logged, serialized, or returned from an HTTP handler.
func (c Config) DSN() (string, error) {
	cfg, err := c.driverConfig()
	if err != nil {
		return "", err
	}
	return cfg.FormatDSN(), nil
}
func OpenBackend(ctx context.Context, config Config) (database.Backend, error) {
	db, err := Open(ctx, config)
	if err != nil {
		return nil, err
	}
	return database.NewSQLBackend(database.BackendMySQL, db), nil
}
func Open(ctx context.Context, config Config) (*sql.DB, error) {
	diagnostic, err := open(ctx, config, "")
	if err != nil {
		return nil, err
	}
	var version, comment string
	if err := diagnostic.QueryRowContext(ctx, `SELECT @@version, @@version_comment`).Scan(&version, &comment); err != nil {
		_ = diagnostic.Close()
		return nil, fmt.Errorf("inspect mysql server version: %w", err)
	}
	collation, err := database.MySQLStorageCollation(version, comment)
	if closeErr := diagnostic.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	return open(ctx, config, collation)
}

// openDiagnostic deliberately postpones the version-specific session
// collation. Test and Open use it to select the strongest supported NO PAD
// collation before creating the operational pool.
func openDiagnostic(ctx context.Context, config Config) (*sql.DB, error) {
	return open(ctx, config, "")
}
func open(ctx context.Context, config Config, sessionCollation string) (*sql.DB, error) {
	cfg := config.withDefaults()
	driverConfig, err := cfg.driverConfigWithSessionCollation(sessionCollation)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("mysql", driverConfig.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping mysql %s: %w", cfg.Endpoint(), err)
	}
	var serverWaitTimeoutSeconds int64
	if err := db.QueryRowContext(ctx, `SELECT @@session.wait_timeout`).Scan(&serverWaitTimeoutSeconds); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("inspect mysql session wait_timeout: %w", err)
	}
	// MySQL closes an idle connection after wait_timeout. Keep the Go pool's
	// idle lifetime safely below that boundary so database/sql retires the
	// connection before the next worker receives a socket the server has
	// already aborted. This is especially important for small local MySQL
	// configurations whose wait_timeout is commonly only 120 seconds.
	db.SetConnMaxIdleTime(effectiveConnMaxIdleTime(cfg.ConnMaxIdleTime, serverWaitTimeoutSeconds))
	return db, nil
}

func effectiveConnMaxIdleTime(configured time.Duration, serverWaitTimeoutSeconds int64) time.Duration {
	if configured <= 0 || serverWaitTimeoutSeconds <= 0 {
		return configured
	}
	serverTimeout := time.Duration(serverWaitTimeoutSeconds) * time.Second
	safeTimeout := serverTimeout / 2
	if safeTimeout <= 0 || configured <= safeTimeout {
		return configured
	}
	return safeTimeout
}
func (c Config) driverConfig() (*driver.Config, error) {
	return c.driverConfigWithSessionCollation(database.MySQLBinaryCollation)
}
func (c Config) driverConfigWithSessionCollation(
	sessionCollation string,
) (*driver.Config, error) {
	c = c.withDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	driverConfig := driver.NewConfig()
	driverConfig.User = c.Username
	driverConfig.Passwd = c.Password
	driverConfig.Net = "tcp"
	driverConfig.Addr = c.Endpoint()
	driverConfig.DBName = c.Database
	// The classic MySQL handshake carries only a one-byte collation ID.
	// utf8mb4_0900_bin is collation 309, so go-sql-driver/mysql cannot encode it
	// in the handshake and rejects it as an unknown collation.  Start with the
	// older binary utf8mb4 collation (ID 46), then establish the required MySQL
	// 8 NO PAD collation as a session variable before Open returns the
	// connection to callers.
	driverConfig.Collation = "utf8mb4_bin"
	driverConfig.ParseTime = true
	driverConfig.Loc = time.UTC
	driverConfig.Timeout = c.ConnectTimeout
	driverConfig.ReadTimeout = c.ReadTimeout
	driverConfig.WriteTimeout = c.WriteTimeout
	driverConfig.RejectReadOnly = true
	driverConfig.Params = map[string]string{
		"charset":   database.MySQLCharacterSet,
		"time_zone": "'+00:00'",
	}
	if sessionCollation != "" {
		driverConfig.Params["collation_connection"] = sessionCollation
	}
	if c.TLSMode == TLSDisabled {
		driverConfig.TLSConfig = "false"
		return driverConfig, nil
	}
	tlsConfig, identity, err := c.tlsConfig()
	if err != nil {
		return nil, err
	}
	name := "cpamp_" + identity
	if err := driver.RegisterTLSConfig(name, tlsConfig); err != nil {
		return nil, fmt.Errorf("register mysql TLS config: %w", err)
	}
	driverConfig.TLSConfig = name
	return driverConfig, nil
}
func (c Config) tlsConfig() (*tls.Config, string, error) {
	serverName := strings.Trim(strings.TrimSpace(c.Host), "[]")
	config := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName}
	if c.TLSCAPath != "" {
		pem, err := os.ReadFile(c.TLSCAPath)
		if err != nil {
			return nil, "", fmt.Errorf("read mysql CA: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, "", errors.New("mysql CA file contains no certificates")
		}
		config.RootCAs = pool
	}
	if c.TLSCertPath != "" {
		certificate, err := tls.LoadX509KeyPair(c.TLSCertPath, c.TLSKeyPath)
		if err != nil {
			return nil, "", fmt.Errorf("load mysql client certificate: %w", err)
		}
		config.Certificates = []tls.Certificate{certificate}
	}
	digest := sha256.Sum256([]byte(strings.Join([]string{serverName, c.TLSCAPath, c.TLSCertPath, c.TLSKeyPath}, "\x00")))
	return config, hex.EncodeToString(digest[:8]), nil
}
func (c Config) withDefaults() Config {
	c.Host = strings.TrimSpace(c.Host)
	c.Database = strings.TrimSpace(c.Database)
	c.Username = strings.TrimSpace(c.Username)
	c.TLSMode = strings.TrimSpace(strings.ToLower(c.TLSMode))
	if c.Port == 0 {
		c.Port = 3306
	}
	if c.TLSMode == "" {
		c.TLSMode = TLSVerifyIdentity
	}
	if c.ConnectTimeout == 0 {
		c.ConnectTimeout = 5 * time.Second
	}
	// Keep driver-level I/O deadlines disabled by default. Large migration and
	// derived-rebuild statements can be quiet on the wire for several minutes;
	// a short ReadTimeout closes the connection and is reported as "invalid
	// connection" at transaction commit. Callers that need a stricter bound
	// should set ReadTimeout/WriteTimeout explicitly and pass a context with
	// the desired operation lifetime.
	if c.MaxOpenConns == 0 {
		c.MaxOpenConns = 16
	}
	if c.MaxIdleConns == 0 {
		c.MaxIdleConns = 4
	}
	if c.ConnMaxIdleTime == 0 {
		c.ConnMaxIdleTime = 5 * time.Minute
	}
	return c
}
