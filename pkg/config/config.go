package config

import (
	"database/sql"
	"fmt"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gomessguii/logger"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	config_env "github.com/evolution-foundation/evolution-go/pkg/config/env"
)

type Config struct {
	PostgresAuthDB       string
	postgresUsersDB      string
	PostgresHost         string
	PostgresPort         string
	PostgresUser         string
	PostgresPassword     string
	PostgresDB           string
	DatabaseSaveMessages bool
	GlobalApiKey         string
	WaDebug              string
	LogType              string
	WebhookFiles         bool
	ConnectOnStartup     bool
	OsName               string
	AmqpUrl              string
	AmqpGlobalEnabled    bool
	WebhookUrl           string
	ClientName           string
	ApiAudioConverter    string
	ApiAudioConverterKey string
	MinioEndpoint        string
	MinioAccessKey       string
	MinioSecretKey       string
	MinioBucket          string
	MinioUseSSL          bool
	MinioEnabled         bool
	MinioRegion          string
	WhatsappVersionMajor int
	WhatsappVersionMinor int
	WhatsappVersionPatch int
	ProxyProtocol        string
	ProxyHost            string
	ProxyPort            string
	ProxyUsername        string
	ProxyPassword        string
	AmqpGlobalEvents     []string
	AmqpSpecificEvents   []string
	NatsUrl              string
	NatsGlobalEnabled    bool
	NatsGlobalEvents     []string
	EventIgnoreGroup     bool
	EventIgnoreStatus    bool
	QrcodeMaxCount       int
	CheckUserExists      bool
	LicenseGateEnabled   bool
	WAInfoRatePerSecond  float64
	WAInfoBurst          int
	WAInfoMaxWait        time.Duration
	WAInfoCooldown       time.Duration
	GroupSyncInterval    time.Duration
	MessageRetention     time.Duration
	EventRetention       time.Duration
	RemoteMedia          RemoteMediaConfig

	WAOutboundRatePerSecond float64
	WAOutboundBurst         int
	WAOutboundMaxWait       time.Duration
	CampaignBatchSize       int
	CampaignLease           time.Duration
	CampaignPollInterval    time.Duration
	CampaignMaxAttempts     int
	CampaignRetryBase       time.Duration

	// Logger configurations
	LogMaxSize    int
	LogMaxBackups int
	LogMaxAge     int
	LogDirectory  string
	LogCompress   bool
}

type RemoteMediaConfig struct {
	Policy       string
	AllowedHosts []string
	Timeout      time.Duration
	MaxBytes     int64
}

// EnsureDBExists connects to postgres (without the target database) and creates it if it doesn't exist.
func (c *Config) EnsureDBExists(dsn string) error {
	return ensureDBExists(dsn)
}

// ensureDBExists connects to postgres (without the target database) and creates it if it doesn't exist.
func ensureDBExists(dsn string) error {
	dbName, adminDSN, err := extractDBNameAndAdminDSN(dsn)
	if err != nil {
		return err
	}

	db, err := sql.Open("postgres", adminDSN)
	if err != nil {
		return fmt.Errorf("failed to connect to postgres for auto-setup: %v", err)
	}
	defer db.Close()

	var exists bool
	err = db.QueryRow("SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)", dbName).Scan(&exists)
	if err != nil {
		return fmt.Errorf("failed to check database existence: %v", err)
	}

	if !exists {
		logger.LogInfo("[CONFIG] Database %q not found, creating it automatically...", dbName)
		_, err = db.Exec(fmt.Sprintf("CREATE DATABASE %q", dbName))
		if err != nil {
			return fmt.Errorf("failed to create database %q: %v", dbName, err)
		}
		logger.LogInfo("[CONFIG] Database %q created successfully", dbName)
	}

	return nil
}

// extractDBNameAndAdminDSN parses a DSN (URL or key=value) and returns the database name
// and a DSN pointing to the "postgres" maintenance database.
func extractDBNameAndAdminDSN(dsn string) (string, string, error) {
	// Try URL format: postgres://user:pass@host:port/dbname?...
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return "", "", fmt.Errorf("failed to parse DSN URL: %v", err)
		}
		dbName := strings.TrimPrefix(u.Path, "/")
		u.Path = "/postgres"
		return dbName, u.String(), nil
	}

	// Key=value format: host=... user=... password=... dbname=... sslmode=...
	parts := strings.Fields(dsn)
	kvMap := make(map[string]string, len(parts))
	for _, p := range parts {
		kv := strings.SplitN(p, "=", 2)
		if len(kv) == 2 {
			kvMap[kv[0]] = kv[1]
		}
	}
	dbName, ok := kvMap["dbname"]
	if !ok || dbName == "" {
		return "", "", fmt.Errorf("could not extract dbname from DSN")
	}
	kvMap["dbname"] = "postgres"
	adminParts := make([]string, 0, len(kvMap))
	for k, v := range kvMap {
		adminParts = append(adminParts, k+"="+v)
	}
	return dbName, strings.Join(adminParts, " "), nil
}

func (c *Config) CreateUsersDB() (*gorm.DB, error) {
	logger.LogDebug("Connecting to database on: %s", c.postgresUsersDB)

	dbDSN := c.postgresUsersDB

	if c.postgresUsersDB == "" {
		dbDSN = fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable", c.PostgresHost, c.PostgresPort, c.PostgresUser, c.PostgresPassword, c.PostgresDB)
	}

	if err := ensureDBExists(dbDSN); err != nil {
		logger.LogWarn("[CONFIG] Auto-setup failed (will try connecting anyway): %v", err)
	}

	db, err := gorm.Open(
		postgres.Open(dbDSN),
		&gorm.Config{},
	)
	if err != nil {
		return nil, err
	}

	// Configurar pool de conexões no GORM
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("erro ao obter sql.DB do GORM: %v", err)
	}

	// Configurar pool de conexões para evitar conexões ociosas não fechadas
	sqlDB.SetMaxOpenConns(25)                 // Máximo de 25 conexões abertas simultaneamente
	sqlDB.SetMaxIdleConns(5)                  // Máximo de 5 conexões ociosas no pool
	sqlDB.SetConnMaxLifetime(5 * time.Minute) // Reconectar após 5 minutos para evitar timeouts
	sqlDB.SetConnMaxIdleTime(1 * time.Minute) // Fechar conexões ociosas após 1 minuto

	return db, nil
}

func (c *Config) CreateAuthDB() (*sql.DB, error) {
	dbDSN := c.postgresUsersDB

	if c.postgresUsersDB == "" {
		dbDSN = fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable", c.PostgresHost, c.PostgresPort, c.PostgresUser, c.PostgresPassword, c.PostgresDB)
	}

	if err := ensureDBExists(dbDSN); err != nil {
		logger.LogWarn("[CONFIG] Auto-setup failed (will try connecting anyway): %v", err)
	}

	db, err := sql.Open("postgres", dbDSN)
	if err != nil {
		return nil, err
	}

	// Configurar pool de conexões para evitar conexões ociosas não fechadas
	db.SetMaxOpenConns(25)                 // Máximo de 25 conexões abertas simultaneamente
	db.SetMaxIdleConns(5)                  // Máximo de 5 conexões ociosas no pool
	db.SetConnMaxLifetime(5 * time.Minute) // Reconectar após 5 minutos para evitar timeouts
	db.SetConnMaxIdleTime(1 * time.Minute) // Fechar conexões ociosas após 1 minuto

	// Testar a conexão
	err = db.Ping()
	if err != nil {
		return nil, fmt.Errorf("erro ao testar conexão PostgreSQL AUTH: %v", err)
	}

	return db, nil
}

func Load() *Config {
	postgresAuthDB := os.Getenv(config_env.POSTGRES_AUTH_DB)

	postgresUsersDB := os.Getenv(config_env.POSTGRES_USERS_DB)

	postgresHost := os.Getenv(config_env.POSTGRES_HOST)
	postgresPort := os.Getenv(config_env.POSTGRES_PORT)
	postgresUser := os.Getenv(config_env.POSTGRES_USER)
	postgresPassword := os.Getenv(config_env.POSTGRES_PASSWORD)
	postgresDB := os.Getenv(config_env.POSTGRES_DB)

	if postgresUsersDB == "" && (postgresHost == "" || postgresPort == "" || postgresUser == "" || postgresPassword == "" || postgresDB == "") {
		logger.LogFatal("[CONFIG] required database configuration variables are missing. Please check your environment configuration.")
	}

	databaseSaveMessages := os.Getenv(config_env.DATABASE_SAVE_MESSAGES)
	panicIfEmpty(config_env.DATABASE_SAVE_MESSAGES, databaseSaveMessages)

	globalApiKey := os.Getenv(config_env.GLOBAL_API_KEY)
	panicIfEmpty(config_env.GLOBAL_API_KEY, globalApiKey)

	clientName := os.Getenv(config_env.CLIENT_NAME)

	waDebug := os.Getenv(config_env.WA_DEBUG)

	logType := os.Getenv(config_env.LOGTYPE)

	webhookFiles := os.Getenv(config_env.WEBHOOKFILES)
	if webhookFiles == "" {
		webhookFiles = "true"
	}

	connectOnStartup := os.Getenv(config_env.CONNECT_ON_STARTUP)
	if connectOnStartup == "" {
		connectOnStartup = "false"
	}

	osName := os.Getenv(config_env.OS_NAME)

	amqpUrl := os.Getenv(config_env.AMQP_URL)

	// Validate AMQP URL format
	if err := validateAMQPURL(amqpUrl); err != nil {
		logger.LogFatal("[CONFIG] AMQP URL validation failed: %v", err)
	}

	amqpGlobalEnabled := os.Getenv(config_env.AMQP_GLOBAL_ENABLED)

	webhookUrl := os.Getenv(config_env.WEBHOOK_URL)

	apiAudioConverter := os.Getenv(config_env.API_AUDIO_CONVERTER)
	apiAudioConverterKey := os.Getenv(config_env.API_AUDIO_CONVERTER_KEY)

	whatsappVersionMajor := os.Getenv(config_env.WHATSAPP_VERSION_MAJOR)
	whatsappVersionMinor := os.Getenv(config_env.WHATSAPP_VERSION_MINOR)
	whatsappVersionPatch := os.Getenv(config_env.WHATSAPP_VERSION_PATCH)

	proxyProtocol := os.Getenv(config_env.PROXY_PROTOCOL)
	proxyHost := os.Getenv(config_env.PROXY_HOST)
	proxyPort := os.Getenv(config_env.PROXY_PORT)
	proxyUsername := os.Getenv(config_env.PROXY_USERNAME)
	proxyPassword := os.Getenv(config_env.PROXY_PASSWORD)

	eventIgnoreGroup := os.Getenv(config_env.EVENT_IGNORE_GROUP)
	eventIgnoreStatus := os.Getenv(config_env.EVENT_IGNORE_STATUS)
	qrcodeMaxCount := os.Getenv(config_env.QRCODE_MAX_COUNT)
	checkUserExists := os.Getenv(config_env.CHECK_USER_EXISTS)

	if checkUserExists == "" {
		checkUserExists = "true"
	}

	// License gate defaults to enabled (upstream behavior). Set
	// LICENSE_GATE_ENABLED=false to run without the license activation gate and
	// the associated remote heartbeat.
	licenseGateEnabled := os.Getenv(config_env.LICENSE_GATE_ENABLED) != "false"

	waInfoRateValue := os.Getenv(config_env.WA_INFO_RATE)
	if waInfoRateValue == "" {
		waInfoRateValue = "5/min"
	}
	waInfoRatePerSecond, err := parseRatePerSecond(waInfoRateValue)
	if err != nil {
		logger.LogFatal("[CONFIG] invalid %s: %v", config_env.WA_INFO_RATE, err)
	}

	waInfoBurstValue := os.Getenv(config_env.WA_INFO_BURST)
	if waInfoBurstValue == "" {
		waInfoBurstValue = "3"
	}
	waInfoBurst, err := parsePositiveInt(waInfoBurstValue)
	if err != nil {
		logger.LogFatal("[CONFIG] invalid %s: %v", config_env.WA_INFO_BURST, err)
	}

	waInfoMaxWaitValue := os.Getenv(config_env.WA_INFO_MAX_WAIT)
	if waInfoMaxWaitValue == "" {
		waInfoMaxWaitValue = "5s"
	}
	waInfoMaxWait, err := parseNonNegativeDuration(waInfoMaxWaitValue)
	if err != nil {
		logger.LogFatal("[CONFIG] invalid %s: %v", config_env.WA_INFO_MAX_WAIT, err)
	}

	waInfoCooldownValue := os.Getenv(config_env.WA_INFO_COOLDOWN)
	if waInfoCooldownValue == "" {
		waInfoCooldownValue = "90s"
	}
	waInfoCooldown, err := parsePositiveDuration(waInfoCooldownValue)
	if err != nil {
		logger.LogFatal("[CONFIG] invalid %s: %v", config_env.WA_INFO_COOLDOWN, err)
	}

	waOutboundRateValue := os.Getenv(config_env.WA_OUTBOUND_RATE)
	if waOutboundRateValue == "" {
		waOutboundRateValue = "30/min"
	}
	waOutboundRatePerSecond, err := parseRatePerSecond(waOutboundRateValue)
	if err != nil {
		logger.LogFatal("[CONFIG] invalid %s: %v", config_env.WA_OUTBOUND_RATE, err)
	}
	waOutboundBurstValue := os.Getenv(config_env.WA_OUTBOUND_BURST)
	if waOutboundBurstValue == "" {
		waOutboundBurstValue = "5"
	}
	waOutboundBurst, err := parsePositiveInt(waOutboundBurstValue)
	if err != nil {
		logger.LogFatal("[CONFIG] invalid %s: %v", config_env.WA_OUTBOUND_BURST, err)
	}
	waOutboundMaxWaitValue := os.Getenv(config_env.WA_OUTBOUND_MAX_WAIT)
	if waOutboundMaxWaitValue == "" {
		waOutboundMaxWaitValue = "5s"
	}
	waOutboundMaxWait, err := parseNonNegativeDuration(waOutboundMaxWaitValue)
	if err != nil {
		logger.LogFatal("[CONFIG] invalid %s: %v", config_env.WA_OUTBOUND_MAX_WAIT, err)
	}

	campaignBatchValue := os.Getenv(config_env.WA_CAMPAIGN_BATCH)
	if campaignBatchValue == "" {
		campaignBatchValue = "10"
	}
	campaignBatchSize, err := parsePositiveInt(campaignBatchValue)
	if err != nil || campaignBatchSize > 100 {
		logger.LogFatal("[CONFIG] invalid %s: must be between 1 and 100", config_env.WA_CAMPAIGN_BATCH)
	}
	campaignLeaseValue := os.Getenv(config_env.WA_CAMPAIGN_LEASE)
	if campaignLeaseValue == "" {
		campaignLeaseValue = "2m"
	}
	campaignLease, err := parsePositiveDuration(campaignLeaseValue)
	if err != nil {
		logger.LogFatal("[CONFIG] invalid %s: %v", config_env.WA_CAMPAIGN_LEASE, err)
	}
	campaignPollValue := os.Getenv(config_env.WA_CAMPAIGN_POLL_INTERVAL)
	if campaignPollValue == "" {
		campaignPollValue = "1s"
	}
	campaignPollInterval, err := parsePositiveDuration(campaignPollValue)
	if err != nil {
		logger.LogFatal("[CONFIG] invalid %s: %v", config_env.WA_CAMPAIGN_POLL_INTERVAL, err)
	}
	campaignMaxAttemptsValue := os.Getenv(config_env.WA_CAMPAIGN_MAX_ATTEMPTS)
	if campaignMaxAttemptsValue == "" {
		campaignMaxAttemptsValue = "3"
	}
	campaignMaxAttempts, err := parsePositiveInt(campaignMaxAttemptsValue)
	if err != nil {
		logger.LogFatal("[CONFIG] invalid %s: %v", config_env.WA_CAMPAIGN_MAX_ATTEMPTS, err)
	}
	campaignRetryBaseValue := os.Getenv(config_env.WA_CAMPAIGN_RETRY_BASE)
	if campaignRetryBaseValue == "" {
		campaignRetryBaseValue = "30s"
	}
	campaignRetryBase, err := parsePositiveDuration(campaignRetryBaseValue)
	if err != nil {
		logger.LogFatal("[CONFIG] invalid %s: %v", config_env.WA_CAMPAIGN_RETRY_BASE, err)
	}

	waGroupReconcileIntervalValue := os.Getenv(config_env.WA_GROUP_RECONCILE_INTERVAL)
	if waGroupReconcileIntervalValue == "" {
		waGroupReconcileIntervalValue = "6h"
	}
	waGroupReconcileInterval, err := parseNonNegativeDuration(waGroupReconcileIntervalValue)
	if err != nil {
		logger.LogFatal("[CONFIG] invalid %s: %v", config_env.WA_GROUP_RECONCILE_INTERVAL, err)
	}

	messageRetentionValue := os.Getenv(config_env.WA_MSG_RETENTION)
	if messageRetentionValue == "" {
		messageRetentionValue = "2160h"
	}
	messageRetention, err := parsePositiveDuration(messageRetentionValue)
	if err != nil {
		logger.LogFatal("[CONFIG] invalid %s: %v", config_env.WA_MSG_RETENTION, err)
	}

	eventRetentionValue := os.Getenv(config_env.WA_EVENT_RETENTION)
	if eventRetentionValue == "" {
		eventRetentionValue = "720h"
	}
	eventRetention, err := parsePositiveDuration(eventRetentionValue)
	if err != nil {
		logger.LogFatal("[CONFIG] invalid %s: %v", config_env.WA_EVENT_RETENTION, err)
	}

	remoteMediaFetchPolicy := os.Getenv(config_env.REMOTE_MEDIA_FETCH_POLICY)
	if remoteMediaFetchPolicy == "" {
		remoteMediaFetchPolicy = "public_only"
	}
	if remoteMediaFetchPolicy != "disabled" && remoteMediaFetchPolicy != "public_only" && remoteMediaFetchPolicy != "allowlist" {
		logger.LogFatal("[CONFIG] invalid %s: must be disabled, public_only, or allowlist", config_env.REMOTE_MEDIA_FETCH_POLICY)
	}
	remoteMediaAllowedHosts := splitNonEmptyCSV(os.Getenv(config_env.REMOTE_MEDIA_ALLOWED_HOSTS))
	if remoteMediaFetchPolicy == "allowlist" && len(remoteMediaAllowedHosts) == 0 {
		logger.LogFatal("[CONFIG] %s is required when %s=allowlist", config_env.REMOTE_MEDIA_ALLOWED_HOSTS, config_env.REMOTE_MEDIA_FETCH_POLICY)
	}
	remoteMediaFetchTimeoutValue := os.Getenv(config_env.REMOTE_MEDIA_FETCH_TIMEOUT)
	if remoteMediaFetchTimeoutValue == "" {
		remoteMediaFetchTimeoutValue = "15s"
	}
	remoteMediaFetchTimeout, err := parsePositiveDuration(remoteMediaFetchTimeoutValue)
	if err != nil {
		logger.LogFatal("[CONFIG] invalid %s: %v", config_env.REMOTE_MEDIA_FETCH_TIMEOUT, err)
	}
	remoteMediaMaxBytesValue := os.Getenv(config_env.REMOTE_MEDIA_MAX_BYTES)
	if remoteMediaMaxBytesValue == "" {
		remoteMediaMaxBytesValue = "33554432"
	}
	remoteMediaMaxBytes, err := strconv.ParseInt(remoteMediaMaxBytesValue, 10, 64)
	if err != nil || remoteMediaMaxBytes <= 0 {
		logger.LogFatal("[CONFIG] invalid %s: must be a positive integer", config_env.REMOTE_MEDIA_MAX_BYTES)
	}

	// Convertendo para int com valores padrão caso estejam vazios
	major := 0
	if whatsappVersionMajor != "" {
		major, _ = strconv.Atoi(whatsappVersionMajor)
	}
	minor := 0
	if whatsappVersionMinor != "" {
		minor, _ = strconv.Atoi(whatsappVersionMinor)
	}
	patch := 0
	if whatsappVersionPatch != "" {
		patch, _ = strconv.Atoi(whatsappVersionPatch)
	}

	qrMaxCount := 5 // Valor padrão
	if qrcodeMaxCount != "" {
		qrMaxCount, _ = strconv.Atoi(qrcodeMaxCount)
	}

	amqpGlobalEvents := strings.Split(os.Getenv(config_env.AMQP_GLOBAL_EVENTS), ",")
	if len(amqpGlobalEvents) == 1 && amqpGlobalEvents[0] == "" {
		amqpGlobalEvents = []string{}
	}

	amqpSpecificEvents := strings.Split(os.Getenv(config_env.AMQP_SPECIFIC_EVENTS), ",")
	if len(amqpSpecificEvents) == 1 && amqpSpecificEvents[0] == "" {
		amqpSpecificEvents = []string{}
	}

	natsUrl := os.Getenv(config_env.NATS_URL)
	natsGlobalEnabled := os.Getenv(config_env.NATS_GLOBAL_ENABLED)
	natsGlobalEvents := strings.Split(os.Getenv(config_env.NATS_GLOBAL_EVENTS), ",")
	if len(natsGlobalEvents) == 1 && natsGlobalEvents[0] == "" {
		natsGlobalEvents = []string{}
	}

	// Logger configurations
	logMaxSize, _ := strconv.Atoi(os.Getenv(config_env.LOG_MAX_SIZE))
	if logMaxSize == 0 {
		logMaxSize = 100 // Default 100MB
	}

	logMaxBackups, _ := strconv.Atoi(os.Getenv(config_env.LOG_MAX_BACKUPS))
	if logMaxBackups == 0 {
		logMaxBackups = 5 // Default 5 backups
	}

	logMaxAge, _ := strconv.Atoi(os.Getenv(config_env.LOG_MAX_AGE))
	if logMaxAge == 0 {
		logMaxAge = 30 // Default 30 days
	}

	logDirectory := os.Getenv(config_env.LOG_DIRECTORY)
	if logDirectory == "" {
		logDirectory = "./logs" // Default logs directory
	}

	logCompress := os.Getenv(config_env.LOG_COMPRESS) == "true"
	if os.Getenv(config_env.LOG_COMPRESS) == "" {
		logCompress = true // Default compression enabled
	}

	config := &Config{
		PostgresAuthDB:       postgresAuthDB,
		postgresUsersDB:      postgresUsersDB,
		DatabaseSaveMessages: databaseSaveMessages == "true",
		GlobalApiKey:         globalApiKey,
		WaDebug:              waDebug,
		LogType:              logType,
		WebhookFiles:         webhookFiles == "true",
		ConnectOnStartup:     connectOnStartup == "true",
		OsName:               osName,
		AmqpUrl:              amqpUrl,
		AmqpGlobalEnabled:    amqpGlobalEnabled == "true",
		WebhookUrl:           webhookUrl,
		ClientName:           clientName,
		ApiAudioConverter:    apiAudioConverter,
		ApiAudioConverterKey: apiAudioConverterKey,
		PostgresHost:         postgresHost,
		PostgresPort:         postgresPort,
		PostgresUser:         postgresUser,
		PostgresPassword:     postgresPassword,
		PostgresDB:           postgresDB,
		WhatsappVersionMajor: major,
		WhatsappVersionMinor: minor,
		WhatsappVersionPatch: patch,
		ProxyProtocol:        proxyProtocol,
		ProxyHost:            proxyHost,
		ProxyPort:            proxyPort,
		ProxyUsername:        proxyUsername,
		ProxyPassword:        proxyPassword,
		EventIgnoreGroup:     eventIgnoreGroup == "true",
		EventIgnoreStatus:    eventIgnoreStatus == "true",
		QrcodeMaxCount:       qrMaxCount,
		CheckUserExists:      checkUserExists != "false", // Default true, set to false to disable
		LicenseGateEnabled:   licenseGateEnabled,
		WAInfoRatePerSecond:  waInfoRatePerSecond,
		WAInfoBurst:          waInfoBurst,
		WAInfoMaxWait:        waInfoMaxWait,
		WAInfoCooldown:       waInfoCooldown,
		GroupSyncInterval:    waGroupReconcileInterval,
		MessageRetention:     messageRetention,
		EventRetention:       eventRetention,
		RemoteMedia: RemoteMediaConfig{
			Policy: remoteMediaFetchPolicy, AllowedHosts: remoteMediaAllowedHosts,
			Timeout: remoteMediaFetchTimeout, MaxBytes: remoteMediaMaxBytes,
		},
		AmqpGlobalEvents:   amqpGlobalEvents,
		AmqpSpecificEvents: amqpSpecificEvents,
		NatsUrl:            natsUrl,
		NatsGlobalEnabled:  natsGlobalEnabled == "true",
		NatsGlobalEvents:   natsGlobalEvents,
		LogMaxSize:         logMaxSize,
		LogMaxBackups:      logMaxBackups,
		LogMaxAge:          logMaxAge,
		LogDirectory:       logDirectory,
		LogCompress:        logCompress,

		WAOutboundRatePerSecond: waOutboundRatePerSecond,
		WAOutboundBurst:         waOutboundBurst,
		WAOutboundMaxWait:       waOutboundMaxWait,
		CampaignBatchSize:       campaignBatchSize,
		CampaignLease:           campaignLease,
		CampaignPollInterval:    campaignPollInterval,
		CampaignMaxAttempts:     campaignMaxAttempts,
		CampaignRetryBase:       campaignRetryBase,
	}

	minioEnabled := os.Getenv(config_env.MINIO_ENABLED) == "true"
	if minioEnabled {
		config.MinioEnabled = true
		loadMinioConfig(config)
	}

	return config
}

func parseRatePerSecond(value string) (float64, error) {
	parts := strings.Split(strings.TrimSpace(value), "/")
	if len(parts) != 2 {
		return 0, fmt.Errorf("rate must use <count>/<unit>, for example 5/min")
	}

	count, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil || count <= 0 || math.IsNaN(count) || math.IsInf(count, 0) {
		return 0, fmt.Errorf("rate count must be a finite positive number")
	}

	var unitSeconds float64
	switch strings.ToLower(strings.TrimSpace(parts[1])) {
	case "s", "sec", "second", "seconds":
		unitSeconds = float64(time.Second / time.Second)
	case "m", "min", "minute", "minutes":
		unitSeconds = float64(time.Minute / time.Second)
	case "h", "hr", "hour", "hours":
		unitSeconds = float64(time.Hour / time.Second)
	default:
		return 0, fmt.Errorf("unsupported rate unit %q", strings.TrimSpace(parts[1]))
	}

	return count / unitSeconds, nil
}

func parsePositiveInt(value string) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("value must be a positive integer")
	}
	return parsed, nil
}

func splitNonEmptyCSV(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

func parseNonNegativeDuration(value string) (time.Duration, error) {
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("value must be a non-negative Go duration")
	}
	return parsed, nil
}

func parsePositiveDuration(value string) (time.Duration, error) {
	parsed, err := parseNonNegativeDuration(value)
	if err != nil || parsed == 0 {
		return 0, fmt.Errorf("value must be a positive Go duration")
	}
	return parsed, nil
}

func loadMinioConfig(config *Config) {
	minioEndpoint := os.Getenv(config_env.MINIO_ENDPOINT)
	panicIfEmpty(config_env.MINIO_ENDPOINT, minioEndpoint)

	minioAccessKey := os.Getenv(config_env.MINIO_ACCESS_KEY)
	panicIfEmpty(config_env.MINIO_ACCESS_KEY, minioAccessKey)

	minioSecretKey := os.Getenv(config_env.MINIO_SECRET_KEY)
	panicIfEmpty(config_env.MINIO_SECRET_KEY, minioSecretKey)

	minioBucket := os.Getenv(config_env.MINIO_BUCKET)
	panicIfEmpty(config_env.MINIO_BUCKET, minioBucket)

	minioUseSSL := os.Getenv(config_env.MINIO_USE_SSL) == "true"

	minioRegion := os.Getenv(config_env.MINIO_REGION)

	config.MinioEndpoint = minioEndpoint
	config.MinioAccessKey = minioAccessKey
	config.MinioSecretKey = minioSecretKey
	config.MinioBucket = minioBucket
	config.MinioUseSSL = minioUseSSL
	config.MinioRegion = minioRegion
}

func panicIfEmpty(key, value string) {
	if value == "" {
		if os.Getenv("DEBUG_ENABLED") != "1" {
			logger.LogInfo("You are NOT on development mode")
		}
		logger.LogFatal("[CONFIG] required configuration variable is missing. Please check your environment configuration.")
	}
}

// validateAMQPURL validates if the AMQP URL has the correct scheme and format
func validateAMQPURL(amqpURL string) error {
	if amqpURL == "" {
		return nil // Empty URL is allowed (RabbitMQ disabled)
	}

	// Parse the URL
	parsedURL, err := url.Parse(amqpURL)
	if err != nil {
		return fmt.Errorf("invalid AMQP URL format: %v", err)
	}

	// Check if scheme is valid
	if parsedURL.Scheme != "amqp" && parsedURL.Scheme != "amqps" {
		return fmt.Errorf("AMQP scheme must be either 'amqp://' or 'amqps://', got: '%s://'", parsedURL.Scheme)
	}

	// Check if host is present
	if parsedURL.Host == "" {
		return fmt.Errorf("AMQP URL must include a host")
	}

	logger.LogInfo("[CONFIG] AMQP URL validation successful: %s://%s", parsedURL.Scheme, parsedURL.Host)
	return nil
}
