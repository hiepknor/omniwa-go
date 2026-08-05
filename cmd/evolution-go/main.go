package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gomessguii/logger"
	"github.com/joho/godotenv"
	"gorm.io/gorm"
	_ "modernc.org/sqlite"

	"github.com/evolution-foundation/evolution-go/pkg/bootstrap"
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
	event_emission "github.com/evolution-foundation/evolution-go/pkg/events/emission"
	websocket_producer "github.com/evolution-foundation/evolution-go/pkg/events/websocket"
	group_handler "github.com/evolution-foundation/evolution-go/pkg/group/handler"
	group_repository "github.com/evolution-foundation/evolution-go/pkg/group/repository"
	group_service "github.com/evolution-foundation/evolution-go/pkg/group/service"
	group_list_handler "github.com/evolution-foundation/evolution-go/pkg/groupList/handler"
	group_list_repository "github.com/evolution-foundation/evolution-go/pkg/groupList/repository"
	group_list_service "github.com/evolution-foundation/evolution-go/pkg/groupList/service"
	"github.com/evolution-foundation/evolution-go/pkg/httpapi"
	instance_credential "github.com/evolution-foundation/evolution-go/pkg/instance/credential"
	instance_handler "github.com/evolution-foundation/evolution-go/pkg/instance/handler"
	instance_ownership "github.com/evolution-foundation/evolution-go/pkg/instance/ownership"
	instance_repository "github.com/evolution-foundation/evolution-go/pkg/instance/repository"
	instance_runtime "github.com/evolution-foundation/evolution-go/pkg/instance/runtime"
	instance_service "github.com/evolution-foundation/evolution-go/pkg/instance/service"
	label_handler "github.com/evolution-foundation/evolution-go/pkg/label/handler"
	label_repository "github.com/evolution-foundation/evolution-go/pkg/label/repository"
	label_service "github.com/evolution-foundation/evolution-go/pkg/label/service"
	logger_wrapper "github.com/evolution-foundation/evolution-go/pkg/logger"
	media_handler "github.com/evolution-foundation/evolution-go/pkg/media/handler"
	media_model "github.com/evolution-foundation/evolution-go/pkg/media/model"
	media_repository "github.com/evolution-foundation/evolution-go/pkg/media/repository"
	media_service "github.com/evolution-foundation/evolution-go/pkg/media/service"
	message_handler "github.com/evolution-foundation/evolution-go/pkg/message/handler"
	message_repository "github.com/evolution-foundation/evolution-go/pkg/message/repository"
	message_service "github.com/evolution-foundation/evolution-go/pkg/message/service"
	auth_middleware "github.com/evolution-foundation/evolution-go/pkg/middleware"
	"github.com/evolution-foundation/evolution-go/pkg/netguard"
	newsletter_handler "github.com/evolution-foundation/evolution-go/pkg/newsletter/handler"
	newsletter_service "github.com/evolution-foundation/evolution-go/pkg/newsletter/service"
	"github.com/evolution-foundation/evolution-go/pkg/observability"
	"github.com/evolution-foundation/evolution-go/pkg/outbound"
	passkey_handler "github.com/evolution-foundation/evolution-go/pkg/passkey/handler"
	poll_handler "github.com/evolution-foundation/evolution-go/pkg/poll/handler"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	routes "github.com/evolution-foundation/evolution-go/pkg/routes"
	send_handler "github.com/evolution-foundation/evolution-go/pkg/sendMessage/handler"
	send_service "github.com/evolution-foundation/evolution-go/pkg/sendMessage/service"
	server_handler "github.com/evolution-foundation/evolution-go/pkg/server/handler"
	server_service "github.com/evolution-foundation/evolution-go/pkg/server/service"
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
var revision = "unknown"

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

func startBackground(supervisor *bootstrap.Supervisor, name string, work bootstrap.Work) {
	if err := supervisor.Start(name, work); err != nil {
		logger.LogFatal("component=bootstrap action=register_worker worker=%s result=failed", name)
	}
}

func runStandbyServer(ctx context.Context, address string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	processState := bootstrap.NewProcessState(nil)
	standbyRuntime, err := bootstrap.NewStandbyRuntime(processState)
	if err != nil {
		return err
	}
	handler, err := standbyRuntime.Start()
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return errors.Join(err, standbyRuntime.Stop())
	}

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.Serve(listener)
	}()
	logger.LogInfo("component=standby_runtime action=serve result=started address=%s", listener.Addr().String())

	var serveErr error
	select {
	case <-ctx.Done():
	case result := <-serveResult:
		if result != nil && !errors.Is(result, http.ErrServerClosed) {
			serveErr = result
		}
	}
	if err := standbyRuntime.BeginDrain(); err != nil {
		serveErr = errors.Join(serveErr, err)
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	shutdownErr := server.Shutdown(shutdownCtx)
	stopErr := standbyRuntime.Stop()
	return errors.Join(serveErr, shutdownErr, stopErr)
}

func setupRouter(db *gorm.DB, authDB *sql.DB, sqliteDB *sql.DB, config *config.Config, conn *amqp.Connection, exPath string, runtimeCtx *core.RuntimeContext, appCtx context.Context, backgroundWorkers *bootstrap.Supervisor, metricsRegistry *observability.Registry, processState *bootstrap.ProcessState, providerCommands instance_runtime.ProviderCommandExecutor) *gin.Engine {
	runtimeRegistry := bootstrap.NewInstanceRuntime(appCtx, providerCommands)

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

	if conn != nil {
		logger.LogInfo("RabbitMQ enabled")
	}
	if config.NatsUrl != "" {
		logger.LogInfo("NATS enabled")
	}
	externalEvents, err := bootstrap.NewExternalEvents(bootstrap.ExternalEventsDependencies{
		DB: db, Config: config, AMQPConnection: conn, Logger: loggerWrapper, Observer: metricsRegistry,
	})
	if err != nil {
		logger.LogFatal("component=external_events action=initialize result=failed error_code=%s", bootstrap.ExternalEventsErrorCode(err))
	}
	natsProducer := externalEvents.NATSProducer()
	outboxRepository := externalEvents.OutboxRepository()
	dependencyNames := []server_service.DependencyName{
		server_service.DependencyUsersDatabase,
		server_service.DependencyExternalEventOutbox,
	}
	if config.AmqpUrl != "" {
		dependencyNames = append(dependencyNames, server_service.DependencyRabbitMQ)
	}
	if config.MinioEnabled {
		dependencyNames = append(dependencyNames, server_service.DependencyLegacyMedia)
	}
	if config.MediaAssetsEnabled {
		dependencyNames = append(dependencyNames, server_service.DependencyMediaAssets)
	}
	if config.CampaignImageContentEnabled {
		dependencyNames = append(dependencyNames, server_service.DependencyCampaignMedia)
	}
	dependencyHealth, err := server_service.NewDependencyHealthRegistry(metricsRegistry, dependencyNames...)
	if err != nil {
		logger.LogFatal("component=dependency_health action=initialize result=failed error_code=invalid_configuration")
	}
	readinessHealth := server_service.NewReadinessHealth(processState, dependencyHealth, server_service.ReadinessRequirements{
		UsersDatabase: config.ReadinessRequireUsersDatabase,
		EventDelivery: config.ReadinessRequireEventDelivery,
		MinIO:         config.ReadinessRequireMinIO,
	})
	startDependencyProbe := func(name server_service.DependencyName, probe server_service.DependencyProbe) {
		worker, workerErr := server_service.NewDependencyProbeWorker(
			name, probe, dependencyHealth,
			server_service.DefaultDependencyProbeInterval, server_service.DefaultDependencyProbeTimeout,
		)
		if workerErr != nil {
			logger.LogFatal("component=dependency_health action=register dependency=%s result=failed error_code=invalid_configuration", name)
		}
		startBackground(backgroundWorkers, "dependency_health."+string(name), worker.Run)
	}
	usersDatabase, err := db.DB()
	if err != nil {
		logger.LogFatal("component=dependency_health action=register dependency=users_database result=failed error_code=database_pool_unavailable")
	}
	startDependencyProbe(server_service.DependencyUsersDatabase, usersDatabase.PingContext)
	startDependencyProbe(server_service.DependencyExternalEventOutbox, func(ctx context.Context) error {
		_, healthErr := outboxRepository.Health(ctx)
		return healthErr
	})
	if config.AmqpUrl != "" {
		startDependencyProbe(server_service.DependencyRabbitMQ, externalEvents.RabbitMQHealth)
	}
	startBackground(backgroundWorkers, "external_event_outbox.deliveries", externalEvents.OutboxWork())
	logger.LogInfo("component=external_event_outbox action=initialize result=success mode=durable transports=webhook,rabbitmq")
	originPolicy, err := httpapi.NewOriginPolicy(config.HTTPAllowedOrigins)
	if err != nil {
		logger.LogFatal("component=http action=configure_origin_policy result=failed error=%v", err)
	}
	websocketProducer := websocket_producer.NewWebsocketProducer(loggerWrapper, originPolicy)
	startBackground(backgroundWorkers, "websocket.shutdown", func(ctx context.Context) error {
		<-ctx.Done()
		websocketProducer.Close()
		return nil
	})

	// Cria filas globais se o RabbitMQ global estiver habilitado
	if config.AmqpGlobalEnabled && conn != nil {
		logger.LogInfo("Creating global RabbitMQ queues...")
		if err := externalEvents.CreateGlobalRabbitMQQueues(); err != nil {
			logger.LogError("Failed to create global RabbitMQ queues: %v", err)
		} else {
			logger.LogInfo("Global RabbitMQ queues created successfully")
		}
	}

	var mediaStorage storage_interfaces.MediaStorage
	var mediaAssetStore storage_interfaces.MediaAssetStore
	var mediaAssetErr error
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
		startDependencyProbe(server_service.DependencyLegacyMedia, mediaStorage.Health)
	}
	if config.MediaAssetsEnabled {
		if !config.MinioEnabled {
			logger.LogFatal("component=media_assets action=initialize result=failed error=minio_required")
		}
		if err := config.ValidateMediaAssetBucketIsolation(); err != nil {
			logger.LogFatal("component=media_assets action=initialize result=failed error=bucket_isolation_required detail=%v", err)
		}
		mediaAssetStore, mediaAssetErr = minio_storage.NewMediaAssetStorage(
			appCtx, config.MinioEndpoint, config.MinioAccessKey, config.MinioSecretKey,
			config.MediaAssetBucket, config.MinioRegion, config.MinioUseSSL,
		)
		if mediaAssetErr != nil {
			logger.LogFatal("component=media_assets action=initialize result=failed error=%v", mediaAssetErr)
		}
		if err := mediaAssetStore.Health(appCtx); err != nil {
			logger.LogFatal("component=media_assets action=health result=failed error=%v", err)
		}
		logger.LogInfo("component=media_assets action=initialize result=success")
		startDependencyProbe(server_service.DependencyMediaAssets, mediaAssetStore.Health)
	}
	if config.ChatImageContentEnabled && !config.MediaAssetsEnabled {
		logger.LogFatal("component=chat_image_content action=initialize result=failed error=media_assets_required")
	}
	if config.InboundImageContentEnabled && !config.MediaAssetsEnabled {
		logger.LogFatal("component=inbound_image_content action=initialize result=failed error=media_assets_required")
	}
	if config.GroupPhotoAssetsEnabled && (!config.GroupManagementEnabled || !config.MediaAssetsEnabled) {
		logger.LogFatal("component=group_photo_assets action=initialize result=failed error=group_management_and_media_assets_required")
	}
	if config.InboundImageContentEnabled && (len(config.MediaDescriptorKey) != 32 || config.MediaDescriptorKeyVersion < 1) {
		logger.LogFatal("component=inbound_image_content action=initialize result=failed error=descriptor_key_required")
	}

	var tokenDigester instance_repository.TokenDigester
	if len(config.InstanceTokenHMACKey) > 0 {
		digester, err := instance_credential.NewDigester(config.InstanceTokenHMACKey, config.InstanceTokenHMACKeyVersion)
		if err != nil {
			log.Fatal(err)
		}
		tokenDigester = digester
	}
	instanceRepository := instance_repository.NewInstanceRepositoryWithTokenDigester(db, tokenDigester)
	var tokenRotator instance_repository.TokenRotator
	var credentialHealthService *instance_credential.HealthService
	credentialCapabilities := []string{"instance_metadata_views"}
	if tokenDigester != nil {
		backfiller, ok := instanceRepository.(instance_repository.TokenBackfiller)
		if !ok {
			log.Fatal("instance repository does not support token digest backfill")
		}
		startBackground(backgroundWorkers, "instance_token.backfill", func(ctx context.Context) error {
			result, err := instance_credential.RunBoundedBackfill(ctx, backfiller, config.InstanceTokenBackfillBatch, config.InstanceTokenBackfillMaxBatches)
			if err != nil {
				logger.LogError("Instance token digest backfill failed: %v", err)
				return nil
			}
			logger.LogInfo("Instance token digest backfill finished: updated=%d batches=%d complete=%t", result.Updated, result.Batches, result.Complete)
			return nil
		})
		rotator, ok := instanceRepository.(instance_repository.TokenRotator)
		if !ok {
			log.Fatal("instance repository does not support token rotation")
		}
		tokenRotator = rotator
		healthReader, ok := instanceRepository.(instance_repository.CredentialHealthReader)
		if !ok {
			log.Fatal("instance repository does not support credential health")
		}
		credentialHealthService = instance_credential.NewHealthService(healthReader, config.InstanceTokenHMACKeyVersion)
		credentialCapabilities = append(credentialCapabilities, "instance_credential_health", "instance_token_rotation")
	}
	messageRepository := message_repository.NewMessageRepository(db)
	labelRepository := label_repository.NewLabelRepository(db)
	projectionHealthPolicy := projection_service.ProjectionHealthPolicy{}
	if config.GroupSyncInterval > 0 {
		projectionHealthPolicy.MaxReconcileAge = map[string]time.Duration{"groups": 2 * config.GroupSyncInterval}
	}
	var projectionStateOptions []projection_service.StateServiceOption
	contactIdentityBackfillRepository := projection_repository.NewContactIdentityBackfillRepository(db)
	var contactIdentityResolver projection_service.ContactLIDResolver
	if config.ContactIdentityReconciliationEnabled {
		mappingDB := authDB
		if mappingDB == nil {
			mappingDB = sqliteDB
		}
		if mappingDB == nil {
			logger.LogFatal("component=projection action=initialize resource=contact_identity result=failed error_code=mapping_store_required")
		}
		contactIdentityResolver = projection_repository.NewContactLIDMappingResolver(mappingDB)
	}
	conversationBackfillRepository := projection_repository.NewConversationBackfillRepository(db)
	canonicalConversationReadiness := projection_service.NewCanonicalConversationReadiness(contactIdentityBackfillRepository, conversationBackfillRepository)
	groupCampaignsEnabled := config.GroupListsEnabled && config.CampaignGroupTargetsEnabled
	if config.CampaignImageContentEnabled && !groupCampaignsEnabled {
		logger.LogFatal("component=campaign_media action=initialize result=failed error=group_campaign_targets_required")
	}
	if config.CampaignImageContentEnabled {
		if err := config.ValidatePrivateBucketIsolation(config.CampaignMediaBucket); err != nil {
			logger.LogFatal("component=campaign_media action=initialize result=failed error=bucket_isolation_required detail=%v", err)
		}
	}
	if config.GroupListsEnabled {
		projectionStateOptions = append(projectionStateOptions, projection_service.WithResourceCapability("groups", projection_service.CapabilityGroupLists))
	}
	if config.GroupManagementEnabled {
		projectionStateOptions = append(projectionStateOptions, projection_service.WithResourceCapability("groups", projection_service.CapabilityGroupManagementPermissions))
		projectionStateOptions = append(projectionStateOptions, projection_service.WithResourceCapability("groups", projection_service.CapabilityGroupMembersProjection))
		projectionStateOptions = append(projectionStateOptions, projection_service.WithResourceCapability("groups", projection_service.CapabilityGroupManagementCommands))
		projectionStateOptions = append(projectionStateOptions, projection_service.WithResourceCapability("groups", projection_service.CapabilityGroupManagementAudit))
		projectionStateOptions = append(projectionStateOptions, projection_service.WithResourceCapability("groups", projection_service.CapabilityGroupSummary))
	}
	if config.GroupPhotoAssetsEnabled {
		projectionStateOptions = append(projectionStateOptions, projection_service.WithResourceCapability("groups", projection_service.CapabilityGroupPhotoAssets))
	}
	if config.GroupListEligibilityEnabled {
		projectionStateOptions = append(projectionStateOptions, projection_service.WithStaticCapability(projection_service.CapabilityGroupListEligibility))
	}
	if groupCampaignsEnabled {
		projectionStateOptions = append(projectionStateOptions, projection_service.WithResourceCapability("groups", projection_service.CapabilityCampaignGroupTargets))
	}
	if config.CampaignImageContentEnabled {
		projectionStateOptions = append(projectionStateOptions, projection_service.WithResourceCapability("groups", projection_service.CapabilityCampaignImageContent))
	}
	if config.ChatImageContentEnabled {
		projectionStateOptions = append(projectionStateOptions, projection_service.WithResourceCapability("messages", projection_service.CapabilityChatImageContent))
	}
	if config.InboundImageContentEnabled {
		projectionStateOptions = append(projectionStateOptions, projection_service.WithResourceCapability("messages", projection_service.CapabilityInboundImageContent))
	}
	if config.ChatImageContentEnabled && config.InboundImageContentEnabled {
		projectionStateOptions = append(projectionStateOptions, projection_service.WithResourceCapability("messages", projection_service.CapabilityConversationMediaAssets))
	}
	if config.ContactIdentityReconciliationEnabled {
		projectionStateOptions = append(projectionStateOptions, projection_service.WithConditionalCapability(
			projection_service.CapabilityCanonicalContactIdentity,
			[]string{"contacts", "chats"},
			func(instanceID string) (bool, error) {
				state, err := contactIdentityBackfillRepository.GetState(context.Background(), instanceID)
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return false, nil
				}
				return err == nil && state.Version == projection_service.ContactIdentityBackfillVersion &&
					state.Status == projection_model.ContactIdentityBackfillComplete, err
			},
		))
	}
	if config.CanonicalConversationIdentityEnabled {
		projectionStateOptions = append(projectionStateOptions, projection_service.WithConditionalCapability(
			projection_service.CapabilityCanonicalConversationIdentity,
			[]string{"contacts", "chats", "messages"},
			canonicalConversationReadiness.Ready,
		))
		projectionStateOptions = append(projectionStateOptions, projection_service.WithConditionalCapability(
			projection_service.CapabilityAuthoritativeConversationUnread,
			[]string{"contacts", "chats", "messages"},
			canonicalConversationReadiness.UnreadReady,
		))
	}
	if config.ConversationAppStateCommandsEnabled {
		projectionStateOptions = append(projectionStateOptions, projection_service.WithConditionalCapability(
			projection_service.CapabilityConversationAppStateCommands,
			[]string{"contacts", "chats", "messages"},
			canonicalConversationReadiness.Ready,
		))
	}
	if config.ConversationHistorySyncEnabled {
		projectionStateOptions = append(projectionStateOptions, projection_service.WithConditionalCapability(
			projection_service.CapabilityConversationHistorySync,
			[]string{"contacts", "chats", "messages"},
			canonicalConversationReadiness.Ready,
		))
	}
	projectionStateService := projection_service.NewStateServiceWithHealth(
		projection_repository.NewStateRepository(db),
		projection_repository.NewWorkHealthRepository(db),
		projectionHealthPolicy,
		projectionStateOptions...,
	)
	projectionEventService := projection_service.NewEventService(projection_repository.NewEventRepository(db), 30*time.Second, 5*time.Second)
	groupProjectionRepository := projection_repository.NewGroupRepository(db)
	var groupListHandler group_list_handler.Handler
	if config.GroupListsEnabled {
		groupListRepository := group_list_repository.New(db, group_list_repository.WithMutationEligibilityEvaluator(
			func(ctx context.Context, tx *gorm.DB, instanceID, instanceJID string, groupJIDs []string) ([]group_list_repository.EntryInput, error) {
				state := projection_service.NewStateServiceWithHealth(
					projection_repository.NewStateRepository(tx), projection_repository.NewWorkHealthRepository(tx), projectionHealthPolicy,
				)
				assessment, err := group_list_service.NewEligibilityService(projection_repository.NewGroupRepository(tx), state).
					Assess(ctx, instanceID, instanceJID, groupJIDs)
				if err != nil {
					return nil, err
				}
				return group_list_service.MutationEntries(assessment.Results)
			},
		))
		groupListHandler = group_list_handler.New(group_list_service.NewManagementService(
			groupListRepository,
			group_list_service.NewEligibilityService(groupProjectionRepository, projectionStateService),
			group_list_service.WithEligibilityObserver(metricsRegistry.GroupListEligibility()),
		), group_list_handler.WithEligibilityEndpoints(config.GroupListEligibilityEnabled))
	}
	groupProjector := projection_service.NewGroupProjector(groupProjectionRepository, projectionStateService)
	labelProjectionRepository := projection_repository.NewLabelProjectionRepository(db)
	labelProjector := projection_service.NewLabelProjector(labelProjectionRepository, projectionStateService, projection_repository.NewReadinessRepository(db))
	contactProjectionRepository := projection_repository.NewContactRepository(db)
	phoneEvidenceRepository := projection_repository.NewPhoneIdentityEvidenceRepository(db)
	phoneNumberResolver := projection_service.NewPhoneNumberResolver(phoneEvidenceRepository, config.PhoneNumberExposureEnabled, metricsRegistry)
	var phoneIdentityEvidence *projection_service.PhoneIdentityEvidenceRecorder
	if config.PhoneIdentityEvidenceEnabled {
		phoneIdentityEvidence = projection_service.NewPhoneIdentityEvidenceRecorder(
			phoneEvidenceRepository, metricsRegistry,
		)
	}
	projectionReadinessRepository := projection_repository.NewReadinessRepository(db)
	contactProjector := projection_service.NewContactProjector(contactProjectionRepository, projectionStateService, projectionReadinessRepository)
	chatMessageProjectionRepository := projection_repository.NewChatMessageRepository(db)
	chatMessageProjector := projection_service.NewChatMessageProjector(chatMessageProjectionRepository, projectionStateService, config.MessageRetention)
	chatMessageReader := projection_service.NewChatMessageReader(chatMessageProjectionRepository, projectionStateService, config.MessageRetention).
		WithPhoneNumberResolver(phoneNumberResolver)
	canonicalConversationServing := func(instanceID string) (bool, error) {
		capabilities, err := projectionStateService.Capabilities(instanceID)
		if err != nil {
			return false, err
		}
		for _, capability := range capabilities {
			if capability == projection_service.CapabilityCanonicalConversationIdentity {
				return true, nil
			}
		}
		return false, nil
	}
	if config.CanonicalConversationIdentityEnabled {
		chatMessageReader.EnableCanonicalConversations(canonicalConversationServing)
	}
	historySyncer := projection_service.NewHistorySyncer(projectionEventService, projectionStateService)
	historyReadinessProjector := projection_service.NewHistoryReadinessProjector(projectionStateService, projectionReadinessRepository)
	if config.CanonicalConversationIdentityEnabled {
		historyReadinessProjector.WithCanonicalUnread(chatMessageProjectionRepository)
	}
	durableEventRepository := projection_repository.NewDurableEventRepository(db)
	durableEventService := projection_service.NewDurableEventService(durableEventRepository, config.EventRetention)
	externalEventEmitter, err := event_emission.NewEmitter(durableEventService, outboxRepository, event_emission.Settings{
		GlobalWebhookEnabled: config.WebhookUrl != "",
		GlobalRabbitEnabled:  config.AmqpGlobalEnabled, AMQPGlobalEvents: config.AmqpGlobalEvents,
		AMQPSpecificEvents: config.AmqpSpecificEvents,
	}, metricsRegistry.ExternalEventEmitter())
	if err != nil {
		logger.LogFatal("component=external_event_emitter action=initialize result=failed error_code=invalid_configuration")
	}
	logger.LogInfo("component=external_event_emitter action=initialize result=success mode=durable transports=webhook,rabbitmq")
	durableEventReader := projection_service.NewDurableEventReader(durableEventRepository, config.EventRetention)
	projectionFailureService := projection_service.NewFailureService(projection_repository.NewFailureRepository(db))
	overviewService := projection_service.NewOverviewService(projection_repository.NewOverviewRepository(db))
	healthService := projection_service.NewServerHealthService(projection_repository.NewHealthRepository(db), projectionStateService, queryGuard)
	contactSyncer := projection_service.NewContactSyncer(contactProjectionRepository, projectionStateService, projectionEventService).
		WithPhoneIdentityEvidence(phoneIdentityEvidence)
	var contactIdentityReconciler *projection_service.ContactIdentityReconciler
	if config.ContactIdentityReconciliationEnabled {
		contactIdentityReconciler = projection_service.NewContactIdentityReconciler(
			contactIdentityBackfillRepository, contactProjectionRepository,
		)
	}
	var conversationReconciler *projection_service.ConversationReconciler
	if config.CanonicalConversationIdentityEnabled {
		conversationReconciler = projection_service.NewConversationReconciler(conversationBackfillRepository).WithUnreadSnapshots(chatMessageProjectionRepository)
	}
	contactReader := projection_service.NewContactReader(contactProjectionRepository, projectionStateService)
	labelSyncer := projection_service.NewLabelSyncer(queryGuard, projectionStateService)
	labelReader := projection_service.NewLabelReader(labelProjectionRepository, projectionStateService)
	labelWriter := projection_service.NewLabelWriter(labelProjectionRepository, projectionStateService)
	groupReconciler := projection_service.NewGroupReconciler(queryGuard, groupProjectionRepository, projectionStateService)
	groupReader := projection_service.NewGroupReader(groupProjectionRepository, projectionStateService)
	groupManagementReader := group_service.NewManagementReader(groupProjectionRepository, projectionStateService)
	groupWriter := projection_service.NewGroupWriter(groupProjectionRepository, projectionStateService)
	groupWorker := projection_service.NewWorker(
		projectionEventService, "groups", []string{"joined_group", "group_info"}, 50, time.Second, groupProjector.Handle,
		func(result projection_service.EventBatchResult, err error) {
			if err != nil {
				logger.LogError("component=projection action=process resource=groups result=failed error_code=batch_failed")
			} else if result.Claimed > 0 {
				logger.LogInfo("component=projection action=process resource=groups claimed=%d processed=%d failed=%d retried=%d dead_lettered=%d", result.Claimed, result.Processed, result.Failed, result.Retried, result.DeadLettered)
			}
		},
	)
	startBackground(backgroundWorkers, "projection.groups", groupWorker.Run)
	labelWorker := projection_service.NewWorker(
		projectionEventService, "labels", []string{"label_edit", "label_chat_association", "label_message_association", "label_sync_complete"}, 50, time.Second, labelProjector.Handle,
		func(result projection_service.EventBatchResult, err error) {
			if err != nil {
				logger.LogError("component=projection action=process resource=labels result=failed error_code=batch_failed")
			} else if result.Claimed > 0 {
				logger.LogInfo("component=projection action=process resource=labels claimed=%d processed=%d failed=%d retried=%d dead_lettered=%d", result.Claimed, result.Processed, result.Failed, result.Retried, result.DeadLettered)
			}
		},
	)
	startBackground(backgroundWorkers, "projection.labels", labelWorker.Run)
	contactProjectionHandler := projection_service.EventHandler(contactProjector.Handle)
	if phoneIdentityEvidence != nil {
		contactProjectionHandler = phoneIdentityEvidence.HandleContact(contactProjectionHandler)
	}
	contactWorker := projection_service.NewWorker(
		projectionEventService, "contacts", []string{"contact", "push_name", "business_name", "picture", "user_about", "contact_sync_complete"}, 50, time.Second, contactProjectionHandler,
		func(result projection_service.EventBatchResult, err error) {
			if err != nil {
				logger.LogError("component=projection action=process resource=contacts result=failed error_code=batch_failed")
			} else if result.Claimed > 0 {
				logger.LogInfo("component=projection action=process resource=contacts claimed=%d processed=%d failed=%d retried=%d dead_lettered=%d", result.Claimed, result.Processed, result.Failed, result.Retried, result.DeadLettered)
			}
		},
	)
	startBackground(backgroundWorkers, "projection.contacts", contactWorker.Run)
	chatMessageProjectionHandler := projection_service.EventHandler(chatMessageProjector.Handle)
	if phoneIdentityEvidence != nil {
		chatMessageProjectionHandler = phoneIdentityEvidence.HandleMessage(chatMessageProjectionHandler)
	}
	chatMessageWorker := projection_service.NewWorker(
		projectionEventService, "messages", []string{"message", "receipt", "history_chat", "history_message", "chat_archived", "chat_pinned", "chat_muted"}, 50, time.Second, chatMessageProjectionHandler,
		func(result projection_service.EventBatchResult, err error) {
			if err != nil {
				logger.LogError("component=projection action=process resource=messages result=failed error_code=batch_failed")
			} else if result.Claimed > 0 {
				logger.LogInfo("component=projection action=process resource=messages claimed=%d processed=%d failed=%d retried=%d dead_lettered=%d", result.Claimed, result.Processed, result.Failed, result.Retried, result.DeadLettered)
			}
		},
	)
	startBackground(backgroundWorkers, "projection.messages", chatMessageWorker.Run)
	historyReadinessWorker := projection_service.NewWorker(
		projectionEventService, "messages", []string{"history_sync_complete"}, 10, time.Second, historyReadinessProjector.Handle,
		func(result projection_service.EventBatchResult, err error) {
			if err != nil {
				logger.LogError("component=projection action=readiness resource=messages result=failed error_code=batch_failed")
			} else if result.Claimed > 0 {
				logger.LogInfo("component=projection action=readiness resource=messages claimed=%d processed=%d failed=%d retried=%d dead_lettered=%d", result.Claimed, result.Processed, result.Failed, result.Retried, result.DeadLettered)
			}
		},
	)
	startBackground(backgroundWorkers, "projection.messages_readiness", historyReadinessWorker.Run)
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
	startBackground(backgroundWorkers, "projection.message_retention", messageRetentionWorker.Run)
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
	startBackground(backgroundWorkers, "events.retention", durableEventRetentionWorker.Run)

	mediaAssetRepository := media_repository.New(db)
	var inboundMediaRepository media_repository.InboundRepository
	var mediaDescriptorCipher *media_service.DescriptorCipher
	var inboundMediaCapture *media_service.InboundCaptureService
	if config.InboundImageContentEnabled {
		inboundMediaRepository = media_repository.NewInbound(db)
		mediaDescriptorCipher, err = media_service.NewDescriptorCipher(
			map[int][]byte{config.MediaDescriptorKeyVersion: config.MediaDescriptorKey}, config.MediaDescriptorKeyVersion,
		)
		if err != nil {
			logger.LogFatal("component=inbound_image_content action=initialize result=failed error=invalid_descriptor_key")
		}
		inboundMediaCapture = media_service.NewInboundCaptureService(inboundMediaRepository, mediaDescriptorCipher, media_service.InboundCaptureSettings{
			MaxBytes: config.MediaAssetMaxBytes, MaxPixels: config.MediaAssetMaxPixels,
			MaxAttempts: config.MediaDownloadMaxAttempts, Retention: config.MessageRetention,
		})
	}
	whatsmeowService := whatsmeow_service.NewWhatsmeowService(
		instanceRepository,
		authDB,
		message_repository.NewMessageRepository(db),
		labelRepository,
		config,
		runtimeRegistry,
		websocketProducer,
		sqliteDB,
		exPath,
		mediaStorage,
		natsProducer,
		queryGuard,
		outboundGuard,
		projectionEventService,
		inboundMediaCapture,
		groupReconciler,
		labelSyncer,
		contactSyncer,
		contactIdentityReconciler,
		contactIdentityResolver,
		conversationReconciler,
		historySyncer,
		externalEventEmitter,
		appCtx,
		loggerWrapper,
	)
	instanceMediaPurger := media_service.NewInstancePurger(media_repository.New(db), func(ctx context.Context, variants []media_model.AssetVariant) (storage_interfaces.MediaAssetStore, error) {
		if !config.MinioEnabled {
			return nil, errors.New("MinIO is required to purge instance media")
		}
		needShared, needLegacy := false, false
		for _, variant := range variants {
			switch {
			case strings.HasPrefix(variant.ObjectKey, "media-assets/"):
				needShared = true
			case strings.HasPrefix(variant.ObjectKey, "campaign-media/"):
				needLegacy = true
			default:
				return nil, errors.New("unsupported media object namespace in instance purge")
			}
		}
		var sharedStore storage_interfaces.MediaAssetStore
		if needShared {
			sharedStore = mediaAssetStore
		}
		if needShared && sharedStore == nil {
			createdStore, storeErr := minio_storage.NewMediaAssetStorage(
				ctx, config.MinioEndpoint, config.MinioAccessKey, config.MinioSecretKey,
				config.MediaAssetBucket, config.MinioRegion, config.MinioUseSSL,
			)
			if storeErr != nil {
				return nil, storeErr
			}
			sharedStore = createdStore
		}
		var legacyStore storage_interfaces.CampaignMediaStore
		if needLegacy {
			var storeErr error
			legacyStore, storeErr = minio_storage.NewCampaignMediaStorage(
				ctx, config.MinioEndpoint, config.MinioAccessKey, config.MinioSecretKey,
				config.CampaignMediaBucket, config.MinioRegion, config.MinioUseSSL,
			)
			if storeErr != nil {
				return nil, storeErr
			}
		}
		return minio_storage.NewRoutedMediaAssetPurgeStorage(sharedStore, legacyStore)
	})
	instanceService := instance_service.NewInstanceService(
		instanceRepository,
		runtimeRegistry,
		whatsmeowService,
		config,
		queryGuard,
		identityResolver,
		instanceMediaPurger,
		loggerWrapper,
	)
	var tokenRotationService *instance_credential.RotationService
	if tokenRotator != nil {
		tokenRotationService = instance_credential.NewRotationService(tokenRotator, instance_credential.WithRuntimeTokenUpdater(whatsmeowService))
	}
	remoteMediaFetcher, err := netguard.NewFetcher(netguard.Settings{
		Policy:       netguard.Policy(config.RemoteMedia.Policy),
		AllowedHosts: config.RemoteMedia.AllowedHosts,
		Timeout:      config.RemoteMedia.Timeout,
		MaxBytes:     config.RemoteMedia.MaxBytes,
	})
	if err != nil {
		logger.LogFatal("component=remote_media action=initialize result=failed error=%v", err)
	}
	var audioConverterRequester netguard.Requester
	if config.ApiAudioConverter != "" {
		converterURL, parseErr := url.Parse(config.ApiAudioConverter)
		if parseErr != nil || converterURL.Hostname() == "" {
			logger.LogFatal("component=audio_converter action=initialize result=failed error=invalid_url")
		}
		converterPorts := []string{"80", "443"}
		if converterURL.Port() != "" {
			converterPorts = append(converterPorts, converterURL.Port())
		}
		audioConverterRequester, err = netguard.NewRequester(netguard.RequestSettings{
			AllowedHosts: []string{converterURL.Hostname()}, AllowedPorts: converterPorts, AllowedContentTypes: []string{"application/json"}, AllowPrivate: true, Timeout: 60 * time.Second,
			MaxRequestBytes: 64 * 1024 * 1024, MaxResponseBytes: 64 * 1024 * 1024,
		})
		if err != nil {
			logger.LogFatal("component=audio_converter action=initialize result=failed error=%v", err)
		}
	}
	sendMessageService := send_service.NewSendService(runtimeRegistry, whatsmeowService, config, queryGuard, identityResolver, projection_service.NewMessageWriteThrough(chatMessageProjector), remoteMediaFetcher, audioConverterRequester, loggerWrapper)
	var mediaAssetHandler media_handler.Handler
	var outboundImageService *media_service.OutboundImageService
	var assetService *media_service.AssetService
	if config.ChatImageContentEnabled || config.InboundImageContentEnabled || config.GroupPhotoAssetsEnabled {
		assetSettings := media_service.AssetSettings{
			MaxBytes: config.MediaAssetMaxBytes, MaxPixels: config.MediaAssetMaxPixels,
			UnboundTTL: config.MediaAssetUnboundTTL, DeleteLease: 5 * time.Minute,
		}
		assetService = media_service.NewAssetService(mediaAssetRepository, mediaAssetStore, assetSettings)
		mediaAssetHandler = media_handler.New(assetService, config.MediaAssetMaxBytes,
			media_handler.WithDeviceUploads(config.ChatImageContentEnabled || config.GroupPhotoAssetsEnabled),
			media_handler.WithContent(config.InboundImageContentEnabled || config.GroupPhotoAssetsEnabled),
		)
		if config.ChatImageContentEnabled {
			outboundImageService = media_service.NewOutboundImageService(
				mediaAssetRepository, mediaAssetStore, sendMessageService, config.MediaAssetMaxBytes, config.MessageRetention,
			)
		}
		cleanupWorker := media_service.NewCleanupWorker(mediaAssetRepository, mediaAssetStore, 100, 5*time.Minute, 15*time.Minute,
			func(cleaned int, err error) {
				if err != nil {
					logger.LogError("component=media_assets action=cleanup result=failed error_code=cleanup_failed cleaned=%d", cleaned)
				} else if cleaned > 0 {
					logger.LogInfo("component=media_assets action=cleanup result=success cleaned=%d", cleaned)
				}
			},
		)
		startBackground(backgroundWorkers, "media_assets.cleanup", cleanupWorker.Run)
		if config.InboundImageContentEnabled {
			inboundWorker := media_service.NewInboundWorker(
				inboundMediaRepository, mediaDescriptorCipher, media_service.NewRuntimeInboundDownloader(runtimeRegistry), mediaAssetStore,
				media_service.InboundWorkerSettings{
					BatchSize: config.MediaDownloadBatch, Lease: config.MediaDownloadLease,
					PollInterval: config.MediaDownloadPollInterval, Timeout: config.MediaDownloadTimeout,
					RetryBase: config.MediaDownloadRetryBase, MaxBytes: config.MediaAssetMaxBytes, MaxPixels: config.MediaAssetMaxPixels,
				},
				func(result media_service.InboundWorkerResult, err error) {
					if err != nil {
						logger.LogError("component=media_assets action=download_inbound result=failed claimed=%d completed=%d retried=%d failed=%d", result.Claimed, result.Completed, result.Retried, result.Failed)
					} else if result.Claimed > 0 {
						logger.LogInfo("component=media_assets action=download_inbound result=success claimed=%d completed=%d retried=%d failed=%d", result.Claimed, result.Completed, result.Retried, result.Failed)
					}
				},
			)
			startBackground(backgroundWorkers, "media_assets.inbound_download", inboundWorker.Run)
		}
	}
	campaignRepository := campaign_repository.NewCampaignRepository(db, campaign_repository.WithGroupEligibilityEvaluator(
		func(ctx context.Context, tx *gorm.DB, instanceID, instanceJID string, groupJIDs []string) ([]campaign_repository.GroupEligibilityResult, error) {
			if instanceJID == "" {
				instance, instanceErr := instanceRepository.GetInstanceByID(instanceID)
				if instanceErr != nil {
					return nil, instanceErr
				}
				instanceJID = instance.Jid
			}
			state := projection_service.NewStateServiceWithHealth(
				projection_repository.NewStateRepository(tx),
				projection_repository.NewWorkHealthRepository(tx),
				projectionHealthPolicy,
			)
			results, err := group_list_service.NewEligibilityService(projection_repository.NewGroupRepository(tx), state).
				Evaluate(ctx, instanceID, instanceJID, groupJIDs)
			if err != nil {
				return nil, err
			}
			targets := make([]campaign_repository.GroupEligibilityResult, len(results))
			for index, result := range results {
				reason := ""
				if result.EligibilityReason != nil {
					reason = *result.EligibilityReason
				}
				targets[index] = campaign_repository.GroupEligibilityResult{
					GroupJID: result.GroupJID, TargetLabel: result.CurrentName, Eligibility: result.Eligibility, Reason: reason, CheckedAt: result.CheckedAt,
				}
			}
			return targets, nil
		},
	), campaign_repository.WithGroupSafety(campaign_repository.GroupSafetySettings{
		Enabled: groupCampaignsEnabled, Cooldown: config.CampaignGroupCooldown, CircuitDuration: config.CampaignCircuitDuration,
		RatePauseThreshold: config.CampaignRatePauseThreshold, FailurePauseThreshold: config.CampaignFailurePauseThreshold,
	}))
	var campaignMediaHandler campaign_handler.MediaHandler
	var campaignMediaRepository campaign_repository.MediaAssetRepository
	var campaignMediaStore storage_interfaces.CampaignMediaStore
	if config.CampaignImageContentEnabled {
		if !config.MinioEnabled {
			logger.LogFatal("component=campaign_media action=initialize result=failed error=minio_required")
		}
		if config.MediaAssetsEnabled {
			legacyCampaignStore, mediaStoreErr := minio_storage.NewCampaignMediaStorage(
				appCtx, config.MinioEndpoint, config.MinioAccessKey, config.MinioSecretKey,
				config.CampaignMediaBucket, config.MinioRegion, config.MinioUseSSL,
			)
			if mediaStoreErr != nil {
				logger.LogFatal("component=campaign_media action=initialize_legacy_store result=failed error=%v", mediaStoreErr)
			}
			campaignMediaStore, mediaStoreErr = minio_storage.NewRoutedMediaAssetStorage(mediaAssetStore, legacyCampaignStore)
			if mediaStoreErr != nil {
				logger.LogFatal("component=campaign_media action=initialize_routed_store result=failed error=%v", mediaStoreErr)
			}
			campaignMediaRepository = campaign_repository.NewSharedMediaAssetRepository(db)
		} else {
			var mediaStoreErr error
			campaignMediaStore, mediaStoreErr = minio_storage.NewCampaignMediaStorage(
				appCtx, config.MinioEndpoint, config.MinioAccessKey, config.MinioSecretKey,
				config.CampaignMediaBucket, config.MinioRegion, config.MinioUseSSL,
			)
			if mediaStoreErr != nil {
				logger.LogFatal("component=campaign_media action=initialize result=failed error=%v", mediaStoreErr)
			}
			campaignMediaRepository = campaign_repository.NewMediaAssetRepository(db)
		}
		campaignMediaService := campaign_service.NewMediaAssetService(
			campaignMediaRepository,
			campaignMediaStore,
			campaign_service.MediaSettings{
				MaxBytes: config.CampaignMediaMaxBytes, MaxPixels: config.CampaignMediaMaxPixels,
				UnboundTTL: config.CampaignMediaUnboundTTL, CleanupBatch: 100,
				CleanupLease: 5 * time.Minute, CleanupInterval: 15 * time.Minute,
			},
		)
		campaignMediaHandler = campaign_handler.NewMediaHandler(campaignMediaService, config.CampaignMediaMaxBytes)
		startBackground(backgroundWorkers, "campaign_media.cleanup", campaignMediaService.RunCleanup)
		startDependencyProbe(server_service.DependencyCampaignMedia, campaignMediaStore.Health)
	}
	var imageCampaignSender campaign_service.Sender
	if config.CampaignImageContentEnabled {
		imageCampaignSender = campaign_service.NewImageSender(
			instanceRepository, campaignMediaRepository, campaignMediaStore, sendMessageService, config.CampaignMediaMaxBytes,
		)
	}
	campaignSender := campaign_service.NewContentSender(
		campaign_service.NewTextSender(instanceRepository, sendMessageService),
		imageCampaignSender,
	)
	campaignWorker := campaign_service.NewWorker(
		campaignRepository,
		campaignSender,
		campaign_service.WorkerSettings{
			BatchSize: config.CampaignBatchSize, Lease: config.CampaignLease, PollInterval: config.CampaignPollInterval,
			MaxAttempts: config.CampaignMaxAttempts, RetryBase: config.CampaignRetryBase,
		},
		func(result campaign_service.BatchResult, err error) {
			if err != nil {
				logger.LogError("component=campaign action=process_batch result=failed error_code=batch_processing_failed claimed=%d sent=%d retried=%d deferred=%d failed=%d skipped=%d", result.Claimed, result.Sent, result.Retried, result.Deferred, result.Failed, result.Skipped)
			} else if result.Claimed > 0 {
				logger.LogInfo("component=campaign action=process_batch result=success claimed=%d sent=%d retried=%d deferred=%d failed=%d skipped=%d", result.Claimed, result.Sent, result.Retried, result.Deferred, result.Failed, result.Skipped)
			}
		},
	)
	startBackground(backgroundWorkers, "campaign.delivery", campaignWorker.Run)
	userService := user_service.NewUserService(runtimeRegistry, whatsmeowService, queryGuard, identityResolver, contactReader, remoteMediaFetcher, loggerWrapper, phoneNumberResolver)
	messageServiceOptions := []message_service.MessageServiceOption{}
	if config.CanonicalConversationIdentityEnabled {
		messageServiceOptions = append(messageServiceOptions, message_service.WithProjectedUnread(chatMessageProjectionRepository, projectionStateService))
	}
	messageService := message_service.NewMessageService(runtimeRegistry, messageRepository, whatsmeowService, message_service.LegacyMediaSettings{
		MaxBytes: config.RemoteMedia.MaxBytes,
		Timeout:  config.MediaDownloadTimeout,
	}, loggerWrapper, messageServiceOptions...)
	chatService := chat_service.NewChatService(runtimeRegistry, whatsmeowService, loggerWrapper)
	conversationCommandService := chat_service.NewConversationCommandService(chatMessageProjectionRepository, chatService, canonicalConversationServing)
	groupManagementCommandRepository := group_repository.NewManagementCommandRepository(db)
	var groupPhotoAssets *group_service.GroupPhotoAssetService
	if config.GroupPhotoAssetsEnabled {
		groupPhotoAssets = group_service.NewGroupPhotoAssetService(assetService, mediaAssetRepository, groupWriter, config.MediaAssetMaxBytes, config.MediaAssetMaxPixels)
	}
	groupService := group_service.NewGroupService(runtimeRegistry, whatsmeowService, queryGuard, outboundGuard, groupReader, groupManagementReader, groupWriter, groupManagementCommandRepository, groupPhotoAssets, remoteMediaFetcher, loggerWrapper)
	if config.GroupManagementEnabled {
		groupManagementRecovery := group_service.NewManagementRecoveryWorker(groupManagementCommandRepository, 5*time.Minute, time.Minute, 100,
			func(recovered int64, err error) {
				if err != nil {
					logger.LogError("component=group_management action=recover_commands result=failed error_code=recovery_failed")
				} else if recovered > 0 {
					logger.LogInfo("component=group_management action=recover_commands result=recovered count=%d", recovered)
				}
			})
		startBackground(backgroundWorkers, "group_management.recovery", groupManagementRecovery.Run)
	}
	callService := call_service.NewCallService(runtimeRegistry, whatsmeowService, loggerWrapper)
	communityService := community_service.NewCommunityService(runtimeRegistry, whatsmeowService, loggerWrapper)
	labelService := label_service.NewLabelService(runtimeRegistry, whatsmeowService, labelRepository, labelReader, labelWriter, loggerWrapper)
	newsletterService := newsletter_service.NewNewsletterService(runtimeRegistry, whatsmeowService, queryGuard, loggerWrapper)

	// NOVO: PollHandler usando PollService já inicializado no whatsmeowService (evita dupla inicialização)
	pollHandler := poll_handler.NewPollHandler(whatsmeowService.GetPollService(), loggerWrapper)
	var sendHandlerOptions []send_handler.Option
	if outboundImageService != nil {
		sendHandlerOptions = append(sendHandlerOptions, send_handler.WithAssetImageSender(outboundImageService))
	}

	r := gin.Default()
	if err := r.SetTrustedProxies(config.HTTPTrustedProxies); err != nil {
		logger.LogFatal("component=http action=configure_trusted_proxies result=failed detail=%v", err)
	}
	r.Use(httpapi.RequestIdentity())
	r.Use(originPolicy.Middleware())
	r.Use(auth_middleware.BodyLimit())

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
		metricsRegistry.Handler(),
		instance_handler.NewInstanceHandler(
			instanceService, config,
			instance_handler.WithTokenRotation(tokenRotationService),
			instance_handler.WithCredentialHealth(credentialHealthService),
		),
		user_handler.NewUserHandler(userService),
		send_handler.NewSendHandler(sendMessageService, sendHandlerOptions...),
		message_handler.NewMessageHandler(messageService, chatMessageReader),
		chat_handler.NewChatHandler(
			chatMessageReader,
			chat_handler.WithConversationCommands(
				conversationCommandService,
				config.ConversationAppStateCommandsEnabled,
				config.ConversationHistorySyncEnabled,
			),
		),
		group_handler.NewGroupHandler(groupService, group_handler.WithManagementContract(config.GroupManagementEnabled), group_handler.WithPhotoAssets(config.GroupPhotoAssetsEnabled)),
		groupListHandler,
		call_handler.NewCallHandler(callService),
		campaign_handler.NewCampaignHandler(campaign_service.NewManagementService(
			campaignRepository,
			campaign_service.WithDirectCreateEnabled(config.CampaignDirectCreateEnabled),
			campaign_service.WithGroupTargetsEnabled(groupCampaignsEnabled),
			campaign_service.WithImageContentEnabled(config.CampaignImageContentEnabled),
			campaign_service.WithEligibilityObserver(metricsRegistry.GroupListEligibility()),
		)),
		campaignMediaHandler,
		mediaAssetHandler,
		community_handler.NewCommunityHandler(communityService),
		label_handler.NewLabelHandler(labelService),
		newsletter_handler.NewNewsletterHandler(newsletterService),
		pollHandler,
		server_handler.NewServerHandler(
			version, revision, projectionStateService, durableEventReader, overviewService,
			server_handler.WithHealthService(healthService),
			server_handler.WithFailureService(projectionFailureService),
			server_handler.WithAdminCapabilities(credentialCapabilities...),
			server_handler.WithCapabilityInstanceReader(instanceRepository),
			server_handler.WithRuntimeHealth(readinessHealth),
			server_handler.WithDependencyHealth(dependencyHealth),
		),
		routes.WithConversationAPIObserver(metricsRegistry.ConversationAPI()),
	).AssignRoutes(r)

	if config.ConnectOnStartup {
		startBackground(backgroundWorkers, "whatsmeow.connect_on_startup", func(ctx context.Context) error {
			if ctx.Err() != nil {
				return nil
			}
			whatsmeowService.ConnectOnStartup(config.ClientName)
			return nil
		})
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

func runMigrations(ctx context.Context, db *gorm.DB, config *config.Config, exPath string) error {
	authDialect := "postgres"
	authAddress := config.PostgresAuthDB
	if authAddress == "" {
		authDialect = "sqlite"
		authAddress = fmt.Sprintf("file:%s/dbdata/main.db?_pragma=foreign_keys(1)&_busy_timeout=5000&cache=shared&mode=rwc&_journal_mode=WAL", exPath)
	}
	var migrateCore bootstrap.CoreMigration
	if config.LicenseGateEnabled {
		migrateCore = func(usersDB *gorm.DB) error {
			core.SetDB(usersDB)
			return core.MigrateDB()
		}
	}
	return bootstrap.RunMigrations(ctx, bootstrap.MigrationDependencies{
		UsersDB: db, AuthDialect: authDialect, AuthAddress: authAddress, MigrateCore: migrateCore,
	})
}

func runMigrationCommand(ctx context.Context, config *config.Config) (runErr error) {
	db, err := config.CreateUsersDB()
	if err != nil {
		return fmt.Errorf("connect users database: %w", err)
	}
	usersDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("access users database pool: %w", err)
	}
	defer func() { runErr = errors.Join(runErr, usersDB.Close()) }()

	ownershipCtx, ownershipCancel := context.WithTimeout(ctx, 10*time.Second)
	ownershipGuard, err := instance_ownership.Acquire(ownershipCtx, usersDB)
	ownershipCancel()
	if err != nil {
		return fmt.Errorf("acquire migration ownership: %w", err)
	}
	defer func() {
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer releaseCancel()
		runErr = errors.Join(runErr, ownershipGuard.Close(releaseCtx))
	}()

	if config.PostgresAuthDB != "" {
		if err := config.EnsureDBExists(config.PostgresAuthDB); err != nil {
			logger.LogWarn("Auto-setup auth DB failed (will try connecting anyway): %v", err)
		}
	}
	sqliteDB, exPath, err := initAuthDB(config)
	if err != nil {
		return err
	}
	if sqliteDB != nil {
		defer func() { runErr = errors.Join(runErr, sqliteDB.Close()) }()
	}

	if err := runMigrations(ctx, db, config, exPath); err != nil {
		return err
	}
	return nil
}

func initAuthDB(config *config.Config) (*sql.DB, string, error) {
	if config.PostgresAuthDB != "" {
		return nil, "", nil
	}

	ex, err := os.Executable()
	if err != nil {
		return nil, "", fmt.Errorf("resolve executable path: %w", err)
	}
	exPath := filepath.Dir(ex)

	dbDirectory := exPath + "/dbdata"
	_, err = os.Stat(dbDirectory)
	if os.IsNotExist(err) {
		errDir := os.MkdirAll(dbDirectory, 0751)
		if errDir != nil {
			return nil, "", fmt.Errorf("create auth database directory: %w", errDir)
		}
	} else if err != nil {
		return nil, "", fmt.Errorf("inspect auth database directory: %w", err)
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
		_ = db.Close()
		return nil, fmt.Errorf("erro ao pingar banco AUTH PostgreSQL: %v", err)
	}

	logger.LogInfo("Conectado ao banco AUTH PostgreSQL com pool configurado")
	return db, nil
}

// @title OmniWA GO
// @version 1.0
// @description OmniWA GO - WhatsApp API (whatsmeow). All endpoints are authenticated with an `apikey` HTTP header. Admin routes under `/instance` (create/all/info/delete/proxy/forcereconnect/logs) require the global key from `GLOBAL_API_KEY`; every other route requires the target instance's own token as the `apikey`. Repeated authentication failures may return HTTP 429 with `Retry-After`. See docs/wiki-en for the WebUI integration guide, including the realtime `/ws` event stream (not describable in Swagger 2.0).
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
	command, err := bootstrap.ParseCommand(flag.Args())
	if err != nil {
		log.Fatal(err)
	}
	if command == bootstrap.CommandMigrate {
		if *devMode {
			if err := godotenv.Load(".env"); err != nil {
				log.Fatal(err)
			}
		}
		migrationConfig, err := config.LoadMigration()
		if err != nil {
			log.Fatal(err)
		}
		migrationCtx, stopMigration := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stopMigration()
		logger.LogInfo("component=migrations action=run result=started")
		if err := runMigrationCommand(migrationCtx, migrationConfig); err != nil {
			logger.LogFatal("component=migrations action=run result=failed detail=%v", err)
		}
		logger.LogInfo("component=migrations action=run result=success")
		return
	}
	runtimeMode, err := bootstrap.ParseRuntimeMode(os.Getenv("RUNTIME_MODE"))
	if err != nil {
		log.Fatal(err)
	}
	if runtimeMode == bootstrap.RuntimeModeStandby {
		serverPort := strings.TrimSpace(os.Getenv("SERVER_PORT"))
		if serverPort == "" {
			log.Fatal("SERVER_PORT is required in standby mode")
		}
		standbyCtx, stopStandby := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stopStandby()
		if err := runStandbyServer(standbyCtx, net.JoinHostPort("", serverPort)); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *devMode {
		if err := godotenv.Load(".env"); err != nil {
			log.Fatal(err)
		}
	}

	cfg := config.Load()
	metricsRegistry, err := observability.NewRegistry()
	if err != nil {
		logger.LogFatal("component=metrics action=initialize result=failed detail=%v", err)
	}
	processState := bootstrap.NewProcessState(metricsRegistry)

	logger.LogInfo("Starting OmniWA GO version %s revision %s", version, revision)

	startTime := time.Now()

	db, err := cfg.CreateUsersDB()
	if err != nil {
		log.Fatal(err)
	}
	usersDB, err := db.DB()
	if err != nil {
		log.Fatal("Failed to access users database pool: ", err)
	}
	defer usersDB.Close()
	ownershipCtx, ownershipCancel := context.WithTimeout(context.Background(), 10*time.Second)
	ownershipGuard, err := instance_ownership.Acquire(ownershipCtx, usersDB)
	ownershipCancel()
	if err != nil {
		log.Fatal("Failed to acquire single-replica ownership: ", err)
	}
	defer func() {
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer releaseCancel()
		if err := ownershipGuard.Close(releaseCtx); err != nil {
			logger.LogWarn("component=ownership action=release result=failed error=%v", err)
		}
	}()
	logger.LogInfo("component=ownership action=acquire result=success topology=single_replica")

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

	if err := runMigrations(context.Background(), db, cfg, exPath); err != nil {
		log.Fatal(err)
	}
	epochCtx, epochCancel := context.WithTimeout(context.Background(), 10*time.Second)
	ownershipEpoch, err := ownershipGuard.Activate(epochCtx)
	epochCancel()
	if err != nil {
		log.Fatal("Failed to activate durable ownership epoch: ", err)
	}
	logger.LogInfo("component=ownership action=activate_epoch result=success epoch=%d", ownershipEpoch)
	providerCommands, err := instance_ownership.NewSideEffectFencer(usersDB, ownershipEpoch)
	if err != nil {
		log.Fatal("Failed to initialize fenced provider commands: ", err)
	}

	// Initialize core DB + license runtime only when the gate is enabled.
	// With LICENSE_GATE_ENABLED=false the runtime context stays nil and the
	// server never contacts the licensing server.
	var runtimeCtx *core.RuntimeContext
	if cfg.LicenseGateEnabled {
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

	activeRuntime, err := bootstrap.NewActiveRuntime(context.Background(), processState, func(name string, _ error) {
		logger.LogError("component=bootstrap action=worker_exit worker=%s result=failed error_code=worker_failed", name)
	})
	if err != nil {
		logger.LogFatal("component=active_runtime action=initialize result=failed error_code=invalid_configuration")
	}
	ownershipLost := make(chan error, 1)
	r, err := activeRuntime.Start(func(appCtx context.Context, backgroundWorkers *bootstrap.Supervisor) (http.Handler, error) {
		startBackground(backgroundWorkers, "instance_ownership.monitor", func(ctx context.Context) error {
			if monitorErr := ownershipGuard.Monitor(ctx, 5*time.Second); monitorErr != nil {
				if drainErr := activeRuntime.BeginDrain(); drainErr != nil {
					logger.LogError("component=active_runtime action=drain trigger=ownership_lost result=failed error_code=invalid_transition")
				}
				ownershipLost <- monitorErr
			}
			return nil
		})
		return setupRouter(db, authDB, sqliteDB, cfg, conn, exPath, runtimeCtx, appCtx, backgroundWorkers, metricsRegistry, processState, providerCommands), nil
	})
	if err != nil {
		logger.LogFatal("component=active_runtime action=start result=failed error_code=start_failed detail=%v", err)
	}

	// Graceful shutdown with heartbeat
	heartbeatCtx, heartbeatCancel := context.WithCancel(context.Background())
	defer heartbeatCancel()

	if cfg.LicenseGateEnabled {
		core.StartHeartbeat(heartbeatCtx, runtimeCtx, startTime)
	}

	srv := &http.Server{
		Addr:              ":" + os.Getenv("SERVER_PORT"),
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		logger.LogInfo("Iniciando servidor na porta %s", os.Getenv("SERVER_PORT"))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	select {
	case receivedSignal := <-quit:
		logger.LogInfo("[SHUTDOWN] Signal %s received, shutting down...", receivedSignal)
	case ownershipErr := <-ownershipLost:
		logger.LogError("[SHUTDOWN] Ownership lost, shutting down: %v", ownershipErr)
	}
	if err := activeRuntime.BeginDrain(); err != nil {
		logger.LogError("component=active_runtime action=drain result=failed error_code=invalid_transition")
	}

	// Stop heartbeat loop
	heartbeatCancel()

	if cfg.LicenseGateEnabled {
		core.Shutdown(runtimeCtx)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.LogError("[SHUTDOWN] Server forced to shutdown: %v", err)
	}
	if err := activeRuntime.Stop(shutdownCtx); err != nil {
		logger.LogError("[SHUTDOWN] Background worker shutdown failed: %v", err)
	} else {
		logger.LogInfo("[SHUTDOWN] Background workers stopped")
	}

	logger.LogInfo("[SHUTDOWN] Server exited")
}
