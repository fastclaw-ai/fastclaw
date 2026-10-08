package config

import (
	"os"
	"strconv"
)

// EnvConfig is the bootstrap configuration: storage DSN, gateway port,
// sandbox backend. Read at process start from FASTCLAW_* environment
// variables — there is no config file. Everything user-facing
// (providers, channels, agents) lives in the database.
//
// Set these as `FASTCLAW_<UPPER_SNAKE_CASE>` (or the explicit name in
// the `env:` tag below) at the process / container level. systemd unit,
// docker-compose, k8s deployment env are the canonical places.
type EnvConfig struct {
	Gateway EnvGateway
	Storage EnvStorage
	Sandbox EnvSandbox
	Redis   EnvRedis
	Log     EnvLog
}

type EnvGateway struct {
	Port int    // FASTCLAW_PORT       — default 18953
	Bind string // FASTCLAW_BIND       — "loopback" (default) or "all"
}

type EnvStorage struct {
	Type        string // FASTCLAW_STORAGE_TYPE  — "sqlite" (default) or "postgres"
	DSN         string // FASTCLAW_STORAGE_DSN   — empty = sqlite at $FASTCLAW_HOME/fastclaw.db
	AutoMigrate bool   // FASTCLAW_STORAGE_AUTO_MIGRATE — default true
}

type EnvRedis struct {
	Enabled  bool   // FASTCLAW_REDIS_ENABLED — enable Redis-backed bus + leases
	Addr     string // FASTCLAW_REDIS_ADDR    — host:port, e.g. redis:6379
	Username string // FASTCLAW_REDIS_USERNAME
	Password string // FASTCLAW_REDIS_PASSWORD
	DB       int    // FASTCLAW_REDIS_DB
	Prefix   string // FASTCLAW_REDIS_PREFIX  — key prefix, default "fastclaw"
}

type EnvSandbox struct {
	Enabled         bool   // FASTCLAW_SANDBOX_ENABLED
	Backend         string // FASTCLAW_SANDBOX_BACKEND  — "docker", "e2b", or "boxlite"
	Image           string // FASTCLAW_SANDBOX_IMAGE
	E2BKey          string // E2B_API_KEY
	E2BAPIURL       string // E2B_API_URL
	E2BDomain       string // E2B_DOMAIN
	BoxliteURL      string // FASTCLAW_SANDBOX_BOXLITE_URL — full base URL e.g. https://api.boxlite.ai/v1
	BoxliteClientID string // FASTCLAW_SANDBOX_BOXLITE_CLIENT_ID — default "default"
	BoxliteKey      string // BOXLITE_API_KEY — apikey sent as Authorization: Bearer
	BoxlitePrefix   string // FASTCLAW_SANDBOX_BOXLITE_PREFIX — workspace prefix, default "default"
}

type EnvLog struct {
	Level string // FASTCLAW_LOG_LEVEL    — "debug" / "info" / "warn" / "error"
	Debug bool   // FASTCLAW_DEBUG_MODE   — enable verbose debug output (prompt dump, etc.)
}

// LoadEnv reads the bootstrap configuration from FASTCLAW_* environment
// variables. There is no config file: deployment-time settings are part
// of the deployment manifest (systemd / docker-compose / k8s env).
func LoadEnv() *EnvConfig {
	cfg := &EnvConfig{
		// Defaults — used when the env var isn't set. AutoMigrate=true
		// makes a fresh sqlite install boot without manual schema steps.
		Storage: EnvStorage{AutoMigrate: true},
	}

	if v := os.Getenv("FASTCLAW_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.Gateway.Port = p
		}
	}
	if v := os.Getenv("FASTCLAW_BIND"); v != "" {
		cfg.Gateway.Bind = v
	}

	if v := os.Getenv("FASTCLAW_STORAGE_TYPE"); v != "" {
		cfg.Storage.Type = v
	}
	if v := os.Getenv("FASTCLAW_STORAGE_DSN"); v != "" {
		cfg.Storage.DSN = v
	}
	if v := os.Getenv("FASTCLAW_STORAGE_AUTO_MIGRATE"); v != "" {
		cfg.Storage.AutoMigrate = v == "true" || v == "1"
	}

	if v := os.Getenv("FASTCLAW_REDIS_ENABLED"); v != "" {
		cfg.Redis.Enabled = v == "true" || v == "1"
	}
	if v := os.Getenv("FASTCLAW_REDIS_ADDR"); v != "" {
		cfg.Redis.Addr = v
		cfg.Redis.Enabled = true
	}
	if v := os.Getenv("FASTCLAW_REDIS_USERNAME"); v != "" {
		cfg.Redis.Username = v
	}
	if v := os.Getenv("FASTCLAW_REDIS_PASSWORD"); v != "" {
		cfg.Redis.Password = v
	}
	if v := os.Getenv("FASTCLAW_REDIS_DB"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Redis.DB = n
		}
	}
	if v := os.Getenv("FASTCLAW_REDIS_PREFIX"); v != "" {
		cfg.Redis.Prefix = v
	}

	if v := os.Getenv("FASTCLAW_SANDBOX_ENABLED"); v != "" {
		cfg.Sandbox.Enabled = v == "true" || v == "1"
	}
	if v := os.Getenv("FASTCLAW_SANDBOX_BACKEND"); v != "" {
		cfg.Sandbox.Backend = v
		// Setting a backend implies the operator wants sandbox on; this
		// mirrors the previous LoadEnv behavior.
		cfg.Sandbox.Enabled = true
	}
	if v := os.Getenv("FASTCLAW_SANDBOX_IMAGE"); v != "" {
		cfg.Sandbox.Image = v
	}
	if v := os.Getenv("E2B_API_KEY"); v != "" {
		cfg.Sandbox.E2BKey = v
	}
	if v := os.Getenv("E2B_API_URL"); v != "" {
		cfg.Sandbox.E2BAPIURL = v
	}
	if v := os.Getenv("E2B_DOMAIN"); v != "" {
		cfg.Sandbox.E2BDomain = v
	}
	if v := os.Getenv("FASTCLAW_SANDBOX_BOXLITE_URL"); v != "" {
		cfg.Sandbox.BoxliteURL = v
	}
	if v := os.Getenv("FASTCLAW_SANDBOX_BOXLITE_CLIENT_ID"); v != "" {
		cfg.Sandbox.BoxliteClientID = v
	}
	if v := os.Getenv("BOXLITE_API_KEY"); v != "" {
		cfg.Sandbox.BoxliteKey = v
	}
	if v := os.Getenv("FASTCLAW_SANDBOX_BOXLITE_PREFIX"); v != "" {
		cfg.Sandbox.BoxlitePrefix = v
	}

	if v := os.Getenv("FASTCLAW_LOG_LEVEL"); v != "" {
		cfg.Log.Level = v
	}
	if v := os.Getenv("FASTCLAW_DEBUG_MODE"); v == "true" || v == "1" {
		cfg.Log.Debug = true
	}
	return cfg
}

// applyObjectStoreEnv reads FASTCLAW_OBJECT_STORE_* env vars into cfg.
func applyObjectStoreEnv(cfg *Config) {
	read := func(key string) string { return os.Getenv("FASTCLAW_OBJECT_STORE_" + key) }
	if v := read("TYPE"); v != "" {
		cfg.ObjectStore.Type = v
	}
	if v := read("LOCAL_ROOT"); v != "" {
		cfg.ObjectStore.Local.Root = v
	}
	if v := read("REGION"); v != "" {
		cfg.ObjectStore.S3.Region = v
	}
	if v := read("BUCKET"); v != "" {
		cfg.ObjectStore.S3.Bucket = v
	}
	if v := read("PREFIX"); v != "" {
		cfg.ObjectStore.S3.Prefix = v
	}
	if v := read("ACCESSKEY"); v != "" {
		cfg.ObjectStore.S3.AccessKey = v
	}
	if v := read("SECRETKEY"); v != "" {
		cfg.ObjectStore.S3.SecretKey = v
	}
	if v := read("ACCOUNTID"); v != "" {
		cfg.ObjectStore.AccountID = v
	}
	if v := read("ENDPOINT"); v != "" {
		cfg.ObjectStore.S3.Endpoint = v
	}
	if v := read("USESSL"); v != "" {
		cfg.ObjectStore.S3.UseSSL = v == "true" || v == "1"
	}
	if v := read("ALIYUN_INTERNAL"); v != "" {
		cfg.ObjectStore.AliyunIntern = v == "true" || v == "1"
	}
}

// debugMode is read once at package init. All debug-gated output in
// the codebase checks this via DebugMode().
var debugMode = os.Getenv("FASTCLAW_DEBUG_MODE") == "true" || os.Getenv("FASTCLAW_DEBUG_MODE") == "1"

// DebugMode returns true when FASTCLAW_DEBUG_MODE=true|1. Use this to
// gate verbose output (prompt dumps, request traces, etc.) that is
// useful during development but noisy in production.
func DebugMode() bool { return debugMode }

// ScrubBootSecrets removes credential-bearing env vars from the
// process environment AFTER bootstrap config has been read. Call once
// from the daemon entry point after gateway construction.
//
// Why: every shell command the agent runs inherits the daemon's env
// by default. Even with subprocess-level scrubbing (see tools/env_scrub.go),
// a subprocess can read /proc/<daemon_pid>/environ as the same UID
// and recover anything still set on the parent. Unsetting at the
// parent closes that path.
//
// Trade-off: the runtime hot-reload paths in gateway.go
// (readObjectStoreCfg / readSystemSandboxCfg) re-call LoadEnv and
// would see empty values for these keys after scrub. That's
// intentional — env is treated as a one-time bootstrap override,
// not a live config source. Operators wanting to rotate credentials
// at runtime should edit the DB-stored config via admin UI.
func ScrubBootSecrets() {
	keys := []string{
		"FASTCLAW_STORAGE_DSN",
		"FASTCLAW_OBJECT_STORE_TYPE",
		"FASTCLAW_OBJECT_STORE_LOCAL_ROOT",
		"FASTCLAW_OBJECT_STORE_REGION",
		"FASTCLAW_OBJECT_STORE_BUCKET",
		"FASTCLAW_OBJECT_STORE_PREFIX",
		"FASTCLAW_OBJECT_STORE_ACCESSKEY",
		"FASTCLAW_OBJECT_STORE_SECRETKEY",
		"FASTCLAW_OBJECT_STORE_ACCOUNTID",
		"FASTCLAW_OBJECT_STORE_ENDPOINT",
		"FASTCLAW_OBJECT_STORE_USESSL",
		"FASTCLAW_OBJECT_STORE_ALIYUN_INTERNAL",
		"FASTCLAW_REDIS_PASSWORD",
		"BOXLITE_API_KEY",
		"E2B_API_KEY",
	}
	for _, k := range keys {
		_ = os.Unsetenv(k)
	}
}

// ApplyToConfig overlays env-derived values onto a runtime Config. Used
// by gateway boot to layer FASTCLAW_OBJECT_STORE_* on top of the DB-
// stored object-store namespace.
func (e *EnvConfig) ApplyToConfig(cfg *Config) {
	if e.Gateway.Port > 0 {
		cfg.Gateway.Port = e.Gateway.Port
	}
	if e.Gateway.Bind != "" {
		cfg.Gateway.Bind = e.Gateway.Bind
	}
	if e.Storage.Type != "" {
		cfg.Storage.Type = e.Storage.Type
	}
	if e.Storage.DSN != "" {
		cfg.Storage.DSN = e.Storage.DSN
	}
	if e.Storage.AutoMigrate {
		cfg.Storage.AutoMigrate = true
	}
	if e.Sandbox.Enabled {
		cfg.Sandbox.Enabled = true
		if e.Sandbox.Backend != "" {
			cfg.Sandbox.Backend = e.Sandbox.Backend
		}
		if e.Sandbox.Image != "" {
			cfg.Sandbox.Image = e.Sandbox.Image
		}
		if e.Sandbox.E2BKey != "" {
			cfg.Sandbox.E2BKey = e.Sandbox.E2BKey
		}
		if e.Sandbox.E2BAPIURL != "" {
			cfg.Sandbox.E2BAPIURL = e.Sandbox.E2BAPIURL
		}
		if e.Sandbox.E2BDomain != "" {
			cfg.Sandbox.E2BDomain = e.Sandbox.E2BDomain
		}
		if e.Sandbox.BoxliteURL != "" {
			cfg.Sandbox.BoxliteURL = e.Sandbox.BoxliteURL
		}
		if e.Sandbox.BoxliteClientID != "" {
			cfg.Sandbox.BoxliteClientID = e.Sandbox.BoxliteClientID
		}
		if e.Sandbox.BoxliteKey != "" {
			cfg.Sandbox.BoxliteKey = e.Sandbox.BoxliteKey
		}
		if e.Sandbox.BoxlitePrefix != "" {
			cfg.Sandbox.BoxlitePrefix = e.Sandbox.BoxlitePrefix
		}
	}
	applyObjectStoreEnv(cfg)
}
