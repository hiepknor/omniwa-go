package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gomessguii/logger"
	"github.com/joho/godotenv"
	"go.mau.fi/whatsmeow"
	"gorm.io/gorm"
	_ "modernc.org/sqlite"

	call_handler "github.com/evolution-foundation/evolution-go/pkg/call/handler"
	call_service "github.com/evolution-foundation/evolution-go/pkg/call/service"
	campaign_handler "github.com/evolution-foundation/evolution-go/pkg/campaign/handler"
	campaign_repository "github.com/evolution-foundation/evolution-go/pkg/campaign/repository"
	campaign_service "github.com/evolution-foundation/evolution-go/pkg/campaign/service"
	chat_handler "github.com/evolution-foundation/evolution-go/pkg/chat/handler"
	chat_service "github.com/evolution-foundation/evolution-go/pkg/chat/service"
	community_handler "github.com/evolution-foundation/evolution-go/pkg/community/handler"
	community_service "github.com/evolution-foundation/evolution-go/pkg/community/service"
	config "github.com/evolution-foundation/evolution-go/pkg/config"
	"github.com/evolution-foundation/evolution-go/pkg/core"
	producer_interfaces "github.com/evolution-foundation/evolution-go/pkg/events/interfaces"
	nats_producer "github.com/evolution-foundation/evolution-go/pkg/events/nats"
	rabbitmq_producer "github.com/evolution-foundation/evolution-go/pkg/events/rabbitmq"
	webhook_producer "github.com/evolution-foundation/evolution-go/pkg/events/webhook"
	websocket_producer "github.com/evolution-foundation/evolution-go/pkg/events/websocket"
	group_handler "github.com/evolution-foundation/evolution-go/pkg/group/handler"
	group_service "github.com/evolution-foundation/evolution-go/pkg/group/service"
	instance_handler "github.com/evolution-foundation/evolution-go/pkg/instance/handler"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	instance_repository "github.com/evolution-foundation/evolution-go/pkg/instance/repository"
	instance_service "github.com/evolution-foundation/evolution-go/pkg/instance/service"
	label_handler "github.com/evolution-foundation/evolution-go/pkg/label/handler"
	label_model "github.com/evolution-foundation/evolution-go/pkg/label/model"
	label_repository "github.com/evolution-foundation/evolution-go/pkg/label/repository"
	label_service "github.com/evolution-foundation/evolution-go/pkg/label/service"
	logger_wrapper "github.com/evolution-foundation/evolution-go/pkg/logger"
	message_handler "github.com/evolution-foundation/evolution-go/pkg/message/handler"
	message_model "github.com/evolution-foundation/evolution-go/pkg/message/model"
	message_repository "github.com/evolution-foundation/evolution-go/pkg/message/repository"
	message_service "github.com/evolution-foundation/evolution-go/pkg/message/service"
	auth_middleware "github.com/evolution-foundation/evolution-go/pkg/middleware"
	"github.com/evolution-foundation/evolution-go/pkg/migrations"
	newsletter_handler "github.com/evolution-foundation/evolution-go/pkg/newsletter/handler"
	newsletter_service "github.com/evolution-foundation/evolution-go/pkg/newsletter/service"
	"github.com/evolution-foundation/evolution-go/pkg/outbound"
	passkey_handler "github.com/evolution-foundation/evolution-go/pkg/passkey/handler"
	poll_handler "github.com/evolution-foundation/evolution-go/pkg/poll/handler"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	routes "github.com/evolution-foundation/evolution-go/pkg/routes"
	send_handler "github.com/evolution-foundation/evolution-go/pkg/sendMessage/handler"
	send_service "github.com/evolution-foundation/evolution-go/pkg/sendMessage/service"
	server_handler "github.com/evolution-foundation/evolution-go/pkg/server/handler"
	storage_interfaces "github.com/evolution-foundation/evolution-go/pkg/storage/interfaces"
	minio_storage "github.com/evolution-foundation/evolution-go/pkg/storage/minio"
	user_handler "github.com/evolution-foundation/evolution-go/pkg/user/handler"
	user_service "github.com/evolution-foundation/evolution-go/pkg/user/service"
	"github.com/evolution-foundation/evolution-go/pkg/waquery"
	whatsmeow_service "github.com/evolution-foundation/evolution-go/pkg/whatsmeow/service"
	amqp "github.com/rabbitmq/amqp091-go"
)

var devMode = flag.Bool("dev", false, "Enable development mode")

var version = "0.0.0"

func init() {
	// ldflags -X main.version= sets this at compile time.
	// If not set (or still default), try reading from VERSION file.
	if version == "0.0.0" {
		if v, err := os.ReadFile("VERSION"); err == nil {
			if trimmed := strings.TrimSpace(string(v)); trimmed != "" {
				version = trimmed
			}
		}
	}
}

func setupRouter(db *gorm.DB, authDB *sql.DB, sqliteDB *sql.DB, config *config.Config, conn *amqp.Connection, exPath string, runtimeCtx *core.RuntimeContext, appCtx context.Context, backgroundWorkers *sync.WaitGroup) *gin.Engine {
	killChannel := make(map[string](chan bool))
	clientPointer := make(map[string]*whatsmeow.Client)

	loggerWrapper := logger_wrapper.NewLoggerManager(config)
	queryGuard, err := waquery.New(waquery.Settings{
		RatePerSecond: config.WAInfoRatePerSecond,
		Burst:         config.WAInfoBurst,
		MaxWait:       config.WAInfoMaxWait,
		Cooldown:      config.WAInfoCooldown,
	})
	if err != nil {
		log.Fatal(err)
	}
	outboundGuard, err := outbound.New(outbound.Settings{
		RatePerSecond: config.WAOutboundRatePerSecond,
		Burst:         config.WAOutboundBurst,
		MaxWait:       config.WAOutboundMaxWait,
	})
	if err != nil {
		log.Fatal(err)
	}
	identityResolver, err := waquery.NewIdentityResolver(queryGuard, waquery.DefaultIdentityCacheSettings())
	if err != nil {
		log.Fatal(err)
	}

	var rabbitmqProducer producer_interfaces.Producer
	if conn != nil {
		logger.LogInfo("RabbitMQ enabled")
		rabbitmqProducer = rabbitmq_producer.NewRabbitMQProducer(
			conn,
			config.AmqpGlobalEnabled,
			config.AmqpGlobalEvents,
			config.AmqpSpecificEvents,
			config.AmqpUrl,
			loggerWrapper,
		)
	} else {
		// Even if initial connection failed, pass the URL so reconnection can work
		rabbitmqProducer = rabbitmq_producer.NewRabbitMQProducer(
			nil,
			config.AmqpGlobalEnabled,
			config.AmqpGlobalEvents,
			config.AmqpSpecificEvents,
			config.AmqpUrl, // Keep the URL for reconnection attempts
			loggerWrapper,
		)
	}

	var natsProducer producer_interfaces.Producer
	if config.NatsUrl != "" {
		logger.LogInfo("NATS enabled")
		natsProducer = nats_producer.NewNatsProducer(
			config.NatsUrl,
			config.NatsGlobalEnabled,
			config.NatsGlobalEvents,
			loggerWrapper,
		)
	} else {
		natsProducer = nats_producer.NewNatsProducer(
			"",
			false,
			nil,
			loggerWrapper,
		)
	}

	webhookProducer := webhook_producer.NewWebhookProducer(config.WebhookUrl, loggerWrapper)
	websocketProducer := websocket_producer.NewWebsocketProducer(loggerWrapper)

	// Cria filas globais se o RabbitMQ global estiver habilitado
	if config.AmqpGlobalEnabled && conn != nil {
		logger.LogInfo("Creating global RabbitMQ queues...")
		if err := rabbitmqProducer.CreateGlobalQueues(); err != nil {
			logger.LogError("Failed to create global RabbitMQ queues: %v", err)
		} else {
			logger.LogInfo("Global RabbitMQ queues created successfully")
		}
	}

	var mediaStorage storage_interfaces.MediaStorage
	if config.MinioEnabled {
		mediaStorage, err = minio_storage.NewMinioMediaStorage(
			config.MinioEndpoint,
			config.MinioAccessKey,
			config.MinioSecretKey,
			config.MinioBucket,
			config.MinioRegion,
			config.MinioUseSSL,
		)
		if err != nil {
			log.Fatal(err)
		}
	}

	instanceRepository := instance_repository.NewInstanceRepository(db)
	messageRepository := message_repository.NewMessageRepository(db)
	labelRepository := label_repository.NewLabelRepository(db)
	projectionStateService := projection_service.NewStateService(projection_repository.NewStateRepository(db))
	projectionEventService := projection_service.NewEventService(projection_repository.NewEventRepository(db), 30*time.Second, 5*time.Second)
	groupProjectionRepository := projection_repository.NewGroupRepository(db)
	groupProjector := projection_service.NewGroupProjector(groupProjectionRepository, projectionStateService)
	labelProjectionRepository := projection_repository.NewLabelProjectionRepository(db)
	labelProjector := projection_service.NewLabelProjector(labelProjectionRepository, projectionStateService, projection_repository.NewReadinessRepository(db))
	contactProjectionRepository := projection_repository.NewContactRepository(db)
	projectionReadinessRepository := projection_repository.NewReadinessRepository(db)
	contactProjector := projection_service.NewContactProjector(contactProjectionRepository, projectionStateService, projectionReadinessRepository)
	chatMessageProjectionRepository := projection_repository.NewChatMessageRepository(db)
	chatMessageProjector := projection_service.NewChatMessageProjector(chatMessageProjectionRepository, projectionStateService, config.MessageRetention)
	chatMessageReader := projection_service.NewChatMessageReader(chatMessageProjectionRepository, projectionStateService, config.MessageRetention)
	historySyncer := projection_service.NewHistorySyncer(projectionEventService, projectionStateService)
	historyReadinessProjector := projection_service.NewHistoryReadinessProjector(projectionStateService, projectionReadinessRepository)
	durableEventRepository := projection_repository.NewDurableEventRepository(db)
	durableEventService := projection_service.NewDurableEventService(durableEventRepository, config.EventRetention)
	durableEventReader := projection_service.NewDurableEventReader(durableEventRepository, config.EventRetention)
	overviewService := projection_service.NewOverviewService(projection_repository.NewOverviewRepository(db))
	healthService := projection_service.NewServerHealthService(projection_repository.NewHealthRepository(db), projectionStateService, queryGuard)
	contactSyncer := projection_service.NewContactSyncer(contactProjectionRepository, projectionStateService, projectionEventService)
	contactReader := projection_service.NewContactReader(contactProjectionRepository, projectionStateService)
	labelSyncer := projection_service.NewLabelSyncer(queryGuard, projectionStateService)
	labelReader := projection_service.NewLabelReader(labelProjectionRepository, projectionStateService)
	labelWriter := projection_service.NewLabelWriter(labelProjectionRepository, projectionStateService)
	groupReconciler := projection_service.NewGroupReconciler(queryGuard, groupProjectionRepository, projectionStateService)
	groupReader := projection_service.NewGroupReader(groupProjectionRepository, projectionStateService)
	groupWriter := projection_service.NewGroupWriter(groupProjectionRepository, projectionStateService)
	groupWorker := projection_service.NewWorker(
		projectionEventService, "groups", []string{"joined_group", "group_info"}, 50, time.Second, groupProjector.Handle,
		func(result projection_service.EventBatchResult, err error) {
			if err != nil {
				logger.LogError("component=projection action=process resource=groups result=failed error_code=batch_failed")
			} else if result.Claimed > 0 {
				logger.LogInfo("component=projection action=process resource=groups claimed=%d processed=%d failed=%d", result.Claimed, result.Processed, result.Failed)
			}
		},
	)
	backgroundWorkers.Add(1)
	go func() {
		defer backgroundWorkers.Done()
		if err := groupWorker.Run(appCtx); err != nil {
			logger.LogError("component=projection action=worker resource=groups result=stopped error_code=invalid_worker_configuration")
		}
	}()
	labelWorker := projection_service.NewWorker(
		projectionEventService, "labels", []string{"label_edit", "label_chat_association", "label_message_association", "label_sync_complete"}, 50, time.Second, labelProjector.Handle,
		func(result projection_service.EventBatchResult, err error) {
			if err != nil {
				logger.LogError("component=projection action=process resource=labels result=failed error_code=batch_failed")
			} else if result.Claimed > 0 {
				logger.LogInfo("component=projection action=process resource=labels claimed=%d processed=%d failed=%d", result.Claimed, result.Processed, result.Failed)
			}
		},
	)
	backgroundWorkers.Add(1)
	go func() {
		defer backgroundWorkers.Done()
		if err := labelWorker.Run(appCtx); err != nil {
			logger.LogError("component=projection action=worker resource=labels result=stopped error_code=invalid_worker_configuration")
		}
	}()
	contactWorker := projection_service.NewWorker(
		projectionEventService, "contacts", []string{"contact", "push_name", "business_name", "picture", "user_about", "contact_sync_complete"}, 50, time.Second, contactProjector.Handle,
		func(result projection_service.EventBatchResult, err error) {
			if err != nil {
				logger.LogError("component=projection action=process resource=contacts result=failed error_code=batch_failed")
			} else if result.Claimed > 0 {
				logger.LogInfo("component=projection action=process resource=contacts claimed=%d processed=%d failed=%d", result.Claimed, result.Processed, result.Failed)
			}
		},
	)
	backgroundWorkers.Add(1)
	go func() {
		defer backgroundWorkers.Done()
		if err := contactWorker.Run(appCtx); err != nil {
			logger.LogError("component=projection action=worker resource=contacts result=stopped error_code=invalid_worker_configuration")
		}
	}()
	chatMessageWorker := projection_service.NewWorker(
		projectionEventService, "messages", []string{"message", "receipt", "history_chat", "history_message"}, 50, time.Second, chatMessageProjector.Handle,
		func(result projection_service.EventBatchResult, err error) {
			if err != nil {
				logger.LogError("component=projection action=process resource=messages result=failed error_code=batch_failed")
			} else if result.Claimed > 0 {
				logger.LogInfo("component=projection action=process resource=messages claimed=%d processed=%d failed=%d", result.Claimed, result.Processed, result.Failed)
			}
		},
	)
	backgroundWorkers.Add(1)
	go func() {
		defer backgroundWorkers.Done()
		if err := chatMessageWorker.Run(appCtx); err != nil {
			logger.LogError("component=projection action=worker resource=messages result=stopped error_code=invalid_worker_configuration")
		}
	}()
	historyReadinessWorker := projection_service.NewWorker(
		projectionEventService, "messages", []string{"history_sync_complete"}, 10, time.Second, historyReadinessProjector.Handle,
		func(result projection_service.EventBatchResult, err error) {
			if err != nil {
				logger.LogError("component=projection action=readiness resource=messages result=failed error_code=batch_failed")
			} else if result.Claimed > 0 {
				logger.LogInfo("component=projection action=readiness resource=messages claimed=%d processed=%d failed=%d", result.Claimed, result.Processed, result.Failed)
			}
		},
	)
	backgroundWorkers.Add(1)
	go func() {
		defer backgroundWorkers.Done()
		if err := historyReadinessWorker.Run(appCtx); err != nil {
			logger.LogError("component=projection action=worker resource=messages_readiness result=stopped error_code=invalid_worker_configuration")
		}
	}()
	messageRetentionWorker := projection_service.NewMessageRetentionWorker(
		projection_repository.NewMessageRetentionRepository(db), config.MessageRetention, 5_000, time.Minute,
		func(deleted int64, err error) {
			if err != nil {
				logger.LogError("component=projection action=retention resource=messages result=failed error_code=delete_failed")
			} else if deleted > 0 {
				logger.LogInfo("component=projection action=retention resource=messages result=deleted count=%d", deleted)
			}
		},
	)
	backgroundWorkers.Add(1)
	go func() {
		defer backgroundWorkers.Done()
		if err := messageRetentionWorker.Run(appCtx); err != nil {
			logger.LogError("component=projection action=worker resource=message_retention result=stopped error_code=invalid_worker_configuration")
		}
	}()
	durableEventRetentionWorker := projection_service.NewDurableEventRetentionWorker(
		durableEventRepository, 5_000, time.Minute,
		func(deleted int64, err error) {
			if err != nil {
				logger.LogError("component=events action=retention result=failed error_code=delete_failed")
			} else if deleted > 0 {
				logger.LogInfo("component=events action=retention result=deleted count=%d", deleted)
			}
		},
	)
	backgroundWorkers.Add(1)
	go func() {
		defer backgroundWorkers.Done()
		if err := durableEventRetentionWorker.Run(appCtx); err != nil {
			logger.LogError("component=events action=worker resource=event_retention result=stopped error_code=invalid_worker_configuration")
		}
	}()

	whatsmeowService := whatsmeow_service.NewWhatsmeowService(
		instanceRepository,
		authDB,
		message_repository.NewMessageRepository(db),
		labelRepository,
		config,
		killChannel,
		clientPointer,
		rabbitmqProducer,
		webhookProducer,
		websocketProducer,
		sqliteDB,
		exPath,
		mediaStorage,
		natsProducer,
		queryGuard,
		outboundGuard,
		projectionEventService,
		groupReconciler,
		labelSyncer,
		contactSyncer,
		historySyncer,
		durableEventService,
		appCtx,
		loggerWrapper,
	)
	instanceService := instance_service.NewInstanceService(
		instanceRepository,
		killChannel,
		clientPointer,
		whatsmeowService,
		config,
		queryGuard,
		identityResolver,
		loggerWrapper,
	)
	sendMessageService := send_service.NewSendService(clientPointer, whatsmeowService, config, queryGuard, identityResolver, projection_service.NewMessageWriteThrough(chatMessageProjector), loggerWrapper)
	campaignRepository := campaign_repository.NewCampaignRepository(db)
	campaignWorker := campaign_service.NewWorker(
		campaignRepository,
		campaign_service.NewTextSender(instanceRepository, sendMessageService),
		campaign_service.WorkerSettings{
			BatchSize: config.CampaignBatchSize, Lease: config.CampaignLease, PollInterval: config.CampaignPollInterval,
			MaxAttempts: config.CampaignMaxAttempts, RetryBase: config.CampaignRetryBase,
		},
		func(result campaign_service.BatchResult, err error) {
			if err != nil {
				logger.LogError("component=campaign action=process_batch result=failed error_code=batch_processing_failed claimed=%d sent=%d retried=%d deferred=%d failed=%d", result.Claimed, result.Sent, result.Retried, result.Deferred, result.Failed)
			} else if result.Claimed > 0 {
				logger.LogInfo("component=campaign action=process_batch result=success claimed=%d sent=%d retried=%d deferred=%d failed=%d", result.Claimed, result.Sent, result.Retried, result.Deferred, result.Failed)
			}
		},
	)
	backgroundWorkers.Add(1)
	go func() {
		defer backgroundWorkers.Done()
		if err := campaignWorker.Run(appCtx); err != nil {
			logger.LogError("component=campaign action=worker result=stopped error_code=invalid_worker_configuration")
		}
	}()
	userService := user_service.NewUserService(clientPointer, whatsmeowService, queryGuard, identityResolver, contactReader, loggerWrapper)
	messageService := message_service.NewMessageService(clientPointer, messageRepository, whatsmeowService, loggerWrapper)
	chatService := chat_service.NewChatService(clientPointer, whatsmeowService, loggerWrapper)
	groupService := group_service.NewGroupService(clientPointer, whatsmeowService, queryGuard, groupReader, groupWriter, loggerWrapper)
	callService := call_service.NewCallService(clientPointer, whatsmeowService, loggerWrapper)
	communityService := community_service.NewCommunityService(clientPointer, whatsmeowService, loggerWrapper)
	labelService := label_service.NewLabelService(clientPointer, whatsmeowService, labelRepository, labelReader, labelWriter, loggerWrapper)
	newsletterService := newsletter_service.NewNewsletterService(clientPointer, whatsmeowService, queryGuard, loggerWrapper)

	// NOVO: PollHandler usando PollService já inicializado no whatsmeowService (evita dupla inicialização)
	pollHandler := poll_handler.NewPollHandler(whatsmeowService.GetPollService(), loggerWrapper)

	r := gin.Default()

	// CORS middleware — must be before everything else
	r.Use(func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Origin, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, Accept, Cache-Control, X-Requested-With, apikey, ApiKey")
		c.Writer.Header().Set("Access-Control-Expose-Headers", "Content-Length, Retry-After")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(200)
			return
		}
		c.Next()
	})

	// License gate is opt-out via LICENSE_GATE_ENABLED=false (default: enabled).
	// When disabled, the API is served without the activation gate and the
	// runtime context is nil (no license routes, no remote heartbeat).
	if config.LicenseGateEnabled {
		r.Use(core.GateMiddleware(runtimeCtx))

		// License routes (always accessible, even without license)
		core.LicenseRoutes(r, runtimeCtx)
	}

	// Passkey ceremony routes — PUBLIC (called by the browser extension from the
	// web.whatsapp.com origin, gated only by an opaque ephemeral token).
	passkey_handler.RegisterRoutes(r, whatsmeowService)

	routes.NewRouter(
		auth_middleware.NewMiddleware(config, instanceService),
		instance_handler.NewInstanceHandler(instanceService, config),
		user_handler.NewUserHandler(userService),
		send_handler.NewSendHandler(sendMessageService),
		message_handler.NewMessageHandler(messageService, chatMessageReader),
		chat_handler.NewChatHandler(chatService, chatMessageReader),
		group_handler.NewGroupHandler(groupService),
		call_handler.NewCallHandler(callService),
		campaign_handler.NewCampaignHandler(campaign_service.NewManagementService(campaignRepository)),
		community_handler.NewCommunityHandler(communityService),
		label_handler.NewLabelHandler(labelService),
		newsletter_handler.NewNewsletterHandler(newsletterService),
		pollHandler,
		server_handler.NewServerHandler(version, projectionStateService, durableEventReader, overviewService, healthService),
	).AssignRoutes(r)

	if config.ConnectOnStartup {
		go whatsmeowService.ConnectOnStartup(config.ClientName)
	}

	r.GET("/ws", func(c *gin.Context) {
		// The token is sent via Sec-WebSocket-Protocol (["apikey", "<token>"])
		// rather than the query string, so it never lands in URLs/access logs.
		// Browsers can set this through the second arg of `new WebSocket(url, [...])`.
		token := websocket_producer.TokenFromProtocolHeader(c.GetHeader("Sec-WebSocket-Protocol"))
		instanceId := c.Query("instanceId")

		if token != config.GlobalApiKey {
			logger.LogError("WebSocket auth failed: invalid token")
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Token inválido"})
			return
		}

		websocket_producer.ServeWs(c.Writer, c.Request, instanceId, websocketProducer)
	})

	return r
}

func migrate(db *gorm.DB) {
	err := db.AutoMigrate(&instance_model.Instance{}, &message_model.Message{}, &label_model.Label{})

	if err != nil {
		log.Fatal(err)
	}
	if err := migrations.Run(db); err != nil {
		log.Fatal(err)
	}
}

func initAuthDB(config *config.Config) (*sql.DB, string, error) {
	if config.PostgresAuthDB != "" {
		return nil, "", nil
	}

	ex, err := os.Executable()
	if err != nil {
		panic(err)
	}
	exPath := filepath.Dir(ex)

	dbDirectory := exPath + "/dbdata"
	_, err = os.Stat(dbDirectory)
	if os.IsNotExist(err) {
		errDir := os.MkdirAll(dbDirectory, 0751)
		if errDir != nil {
			panic("Could not create dbdata directory")
		}
	}

	db, err := sql.Open("sqlite", exPath+"/dbdata/users.db?_pragma=foreign_keys(1)&_busy_timeout=3000")
	if err != nil {
		return nil, "", err
	}

	return db, exPath, nil
}

func initPostgresAuthDB(config *config.Config) (*sql.DB, error) {
	if config.PostgresAuthDB == "" {
		return nil, nil
	}

	if err := config.EnsureDBExists(config.PostgresAuthDB); err != nil {
		logger.LogWarn("Auto-setup auth DB failed (will try connecting anyway): %v", err)
	}

	db, err := sql.Open("postgres", config.PostgresAuthDB)
	if err != nil {
		return nil, fmt.Errorf("erro ao conectar ao banco AUTH PostgreSQL: %v", err)
	}

	// Configurar pool de conexões para evitar conexões ociosas não fechadas
	db.SetMaxOpenConns(25)                 // Máximo de 25 conexões abertas simultaneamente
	db.SetMaxIdleConns(5)                  // Máximo de 5 conexões ociosas no pool
	db.SetConnMaxLifetime(5 * time.Minute) // Reconectar após 5 minutos para evitar timeouts
	db.SetConnMaxIdleTime(1 * time.Minute) // Fechar conexões ociosas após 1 minuto

	err = db.Ping()
	if err != nil {
		return nil, fmt.Errorf("erro ao pingar banco AUTH PostgreSQL: %v", err)
	}

	logger.LogInfo("Conectado ao banco AUTH PostgreSQL com pool configurado")
	return db, nil
}

// @title OmniWA GO
// @version 1.0
// @description OmniWA GO - WhatsApp API (whatsmeow). All endpoints are authenticated with an `apikey` HTTP header. Admin routes under `/instance` (create/all/info/delete/proxy/forcereconnect/logs) require the global key from `GLOBAL_API_KEY`; every other route requires the target instance's own token as the `apikey`. See docs/wiki-en for the WebUI integration guide, including the realtime `/ws` event stream (not describable in Swagger 2.0).
// @contact.name OmniWA GO
// @license.name Apache-2.0
// @license.url https://www.apache.org/licenses/LICENSE-2.0
// @BasePath /
// @schemes http https
// @securityDefinitions.apikey ApiKeyAuth
// @in header
// @name apikey
// @security ApiKeyAuth
func main() {
	flag.Parse()
	if *devMode {
		err := godotenv.Load(".env")
		if err != nil {
			log.Fatal(err)
		}
	}

	cfg := config.Load()

	logger.LogInfo("Starting OmniWA GO version %s", version)

	startTime := time.Now()

	db, err := cfg.CreateUsersDB()
	if err != nil {
		log.Fatal(err)
	}

	// Inicializar PostgreSQL AUTH
	authDB, err := initPostgresAuthDB(cfg)
	if err != nil {
		log.Fatal(err)
	}
	if authDB != nil {
		defer authDB.Close()
	}

	// Manter inicialização do SQLite
	sqliteDB, exPath, err := initAuthDB(cfg)
	if err != nil {
		log.Fatal(err)
	}
	if sqliteDB != nil {
		defer sqliteDB.Close()
	}

	migrate(db)

	// Initialize core DB + license runtime only when the gate is enabled.
	// With LICENSE_GATE_ENABLED=false the runtime context stays nil and the
	// server never contacts the licensing server.
	var runtimeCtx *core.RuntimeContext
	if cfg.LicenseGateEnabled {
		core.SetDB(db)
		if err := core.MigrateDB(); err != nil {
			log.Fatal("Failed to migrate runtime_configs: ", err)
		}
		tier := "evolution-go"
		runtimeCtx = core.InitializeRuntime(tier, version, cfg.GlobalApiKey)
	} else {
		logger.LogInfo("License gate disabled (LICENSE_GATE_ENABLED=false) — running without activation")
	}

	var conn *amqp.Connection

	if cfg.AmqpUrl != "" {
		logger.LogInfo("Attempting to connect to RabbitMQ...")

		// Create connection with heartbeat to prevent timeouts
		amqpConfig := amqp.Config{
			Heartbeat: 30 * time.Second, // Send heartbeat every 30 seconds
			Locale:    "en_US",
		}

		conn, err = amqp.DialConfig(cfg.AmqpUrl, amqpConfig)
		if err != nil {
			logger.LogError("Failed to connect to RabbitMQ, err: %v", err)
			logger.LogInfo("RabbitMQ producer will be created with reconnection capability")
		} else {
			logger.LogInfo("Successfully connected to RabbitMQ with heartbeat enabled")
			defer func(conn *amqp.Connection) {
				err := conn.Close()
				if err != nil {
					logger.LogError("Failed to close RabbitMQ connection, err: %v", err)
				}
			}(conn)
		}
	} else {
		logger.LogInfo("RabbitMQ URL not configured, skipping RabbitMQ connection")
	}

	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()
	var backgroundWorkers sync.WaitGroup
	r := setupRouter(db, authDB, sqliteDB, cfg, conn, exPath, runtimeCtx, appCtx, &backgroundWorkers)

	// Graceful shutdown with heartbeat
	heartbeatCtx, heartbeatCancel := context.WithCancel(context.Background())
	defer heartbeatCancel()

	if cfg.LicenseGateEnabled {
		core.StartHeartbeat(heartbeatCtx, runtimeCtx, startTime)
	}

	srv := &http.Server{
		Addr:    ":" + os.Getenv("SERVER_PORT"),
		Handler: r,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		logger.LogInfo("Iniciando servidor na porta %s", os.Getenv("SERVER_PORT"))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-quit
	logger.LogInfo("[SHUTDOWN] Signal received, shutting down...")

	// Stop heartbeat loop
	heartbeatCancel()
	appCancel()

	if cfg.LicenseGateEnabled {
		core.Shutdown(runtimeCtx)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.LogError("[SHUTDOWN] Server forced to shutdown: %v", err)
	}
	workersStopped := make(chan struct{})
	go func() {
		backgroundWorkers.Wait()
		close(workersStopped)
	}()
	select {
	case <-workersStopped:
		logger.LogInfo("[SHUTDOWN] Background workers stopped")
	case <-shutdownCtx.Done():
		logger.LogError("[SHUTDOWN] Background worker shutdown timed out")
	}

	logger.LogInfo("[SHUTDOWN] Server exited")
}
