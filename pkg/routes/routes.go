package routes

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"

	_ "github.com/evolution-foundation/evolution-go/docs"
	call_handler "github.com/evolution-foundation/evolution-go/pkg/call/handler"
	campaign_handler "github.com/evolution-foundation/evolution-go/pkg/campaign/handler"
	chat_handler "github.com/evolution-foundation/evolution-go/pkg/chat/handler"
	community_handler "github.com/evolution-foundation/evolution-go/pkg/community/handler"
	group_handler "github.com/evolution-foundation/evolution-go/pkg/group/handler"
	group_list_handler "github.com/evolution-foundation/evolution-go/pkg/groupList/handler"
	instance_handler "github.com/evolution-foundation/evolution-go/pkg/instance/handler"
	label_handler "github.com/evolution-foundation/evolution-go/pkg/label/handler"
	media_handler "github.com/evolution-foundation/evolution-go/pkg/media/handler"
	message_handler "github.com/evolution-foundation/evolution-go/pkg/message/handler"
	auth_middleware "github.com/evolution-foundation/evolution-go/pkg/middleware"
	newsletter_handler "github.com/evolution-foundation/evolution-go/pkg/newsletter/handler"
	"github.com/evolution-foundation/evolution-go/pkg/observability"
	poll_handler "github.com/evolution-foundation/evolution-go/pkg/poll/handler"
	send_handler "github.com/evolution-foundation/evolution-go/pkg/sendMessage/handler"
	server_handler "github.com/evolution-foundation/evolution-go/pkg/server/handler"
	user_handler "github.com/evolution-foundation/evolution-go/pkg/user/handler"
)

type Routes struct {
	authMiddleware          auth_middleware.Middleware
	metricsHandler          http.Handler
	jidValidationMiddleware *auth_middleware.JIDValidationMiddleware
	instanceHandler         instance_handler.InstanceHandler
	userHandler             user_handler.UserHandler
	sendHandler             send_handler.SendHandler
	messageHandler          message_handler.MessageHandler
	chatHandler             chat_handler.ChatHandler
	groupHandler            group_handler.GroupHandler
	groupListHandler        group_list_handler.Handler
	callHandler             call_handler.CallHandler
	campaignHandler         campaign_handler.CampaignHandler
	campaignMediaHandler    campaign_handler.MediaHandler
	mediaHandler            media_handler.Handler
	communityHandler        community_handler.CommunityHandler
	labelHandler            label_handler.LabelHandler
	newsletterHandler       newsletter_handler.NewsletterHandler
	pollHandler             *poll_handler.PollHandler
	serverHandler           server_handler.ServerHandler
	conversationObserver    observability.ConversationAPIObserver
}

type Option func(*Routes)

func WithConversationAPIObserver(observer observability.ConversationAPIObserver) Option {
	return func(routes *Routes) { routes.conversationObserver = observer }
}

func (r *Routes) AssignRoutes(eng *gin.Engine) {
	// CORS is configured once in setupRouter (cmd/evolution-go/main.go),
	// applied before every route including the license gate.

	eng.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	eng.GET("/favicon.ico", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	// Rotas para o gerenciador React (sem autenticação)
	eng.Static("/assets", "./manager/dist/assets")

	// Ajuste nas rotas do manager para suportar client-side routing do React
	eng.GET("/manager/*any", func(c *gin.Context) {
		c.File("manager/dist/index.html")
	})

	eng.GET("/manager", func(c *gin.Context) {
		c.File("manager/dist/index.html")
	})

	r.assignRuntimeHealthRoutes(eng)
	r.assignMetricsRoute(eng)
	eng.GET("/server/capabilities", r.authMiddleware.AuthAdminOrInstance, r.serverHandler.Capabilities)
	eng.GET("/server/projection-health", r.authMiddleware.AuthAdminOrInstance, r.serverHandler.ProjectionHealth)
	eng.GET("/server/overview", r.authMiddleware.AuthAdminOrInstance, r.serverHandler.Overview)
	eng.GET("/server/health", r.authMiddleware.AuthAdminOrInstance, r.serverHandler.Health)
	eng.GET("/server/projection-failures", r.authMiddleware.AuthAdmin, r.serverHandler.ProjectionFailures)
	eng.POST("/server/projection-failures/replay", r.authMiddleware.AuthAdmin, r.serverHandler.ReplayProjectionFailure)
	eng.POST("/server/projection-failures/discard", r.authMiddleware.AuthAdmin, r.serverHandler.DiscardProjectionFailure)
	eng.GET("/server/external-event-failures", r.authMiddleware.AuthAdmin, r.serverHandler.ExternalEventFailures)
	eng.POST("/server/external-event-failures/replay", r.authMiddleware.AuthAdmin, r.serverHandler.ReplayExternalEventFailure)
	eng.GET("/events", r.authMiddleware.Auth, r.serverHandler.EventHistory)

	if r.campaignMediaHandler != nil {
		routes := eng.Group("/campaign-media")
		routes.Use(r.authMiddleware.Auth)
		routes.POST("", r.campaignMediaHandler.Upload)
		routes.GET("/:mediaId", r.campaignMediaHandler.Get)
		routes.DELETE("/:mediaId", r.campaignMediaHandler.Delete)
	}
	if r.mediaHandler != nil {
		routes := eng.Group("/media-assets")
		routes.Use(r.authMiddleware.Auth)
		routes.GET("/:mediaId", r.mediaHandler.Get)
		if r.mediaHandler.DeviceUploadsEnabled() {
			routes.POST("", r.mediaHandler.Upload)
			routes.DELETE("/:mediaId", r.mediaHandler.Delete)
		}
		if r.mediaHandler.ContentEnabled() {
			routes.GET("/:mediaId/content", r.mediaHandler.Content)
		}
	}

	if r.groupListHandler != nil {
		routes := eng.Group("/group-lists")
		routes.Use(r.authMiddleware.Auth)
		routes.GET("", r.groupListHandler.List)
		routes.POST("", r.groupListHandler.Create)
		if r.groupListHandler.EligibilityEnabled() {
			routes.POST("/eligibility", r.groupListHandler.Eligibility)
			routes.GET("/:groupListId/eligibility", r.groupListHandler.AggregateEligibility)
		}
		routes.GET("/:groupListId", r.groupListHandler.Get)
		routes.GET("/:groupListId/groups", r.groupListHandler.Groups)
		routes.PUT("/:groupListId", r.groupListHandler.Update)
		routes.DELETE("/:groupListId", r.groupListHandler.Delete)
		routes.GET("/:groupListId/audit", r.groupListHandler.Audit)
	}

	routes := eng.Group("/instance")
	{
		routes.Use(r.authMiddleware.AuthAdmin)
		{
			routes.POST("/create", r.instanceHandler.Create)
			routes.POST("/rotate-token/:instanceId", r.instanceHandler.RotateToken)
			routes.GET("/credential-health", r.instanceHandler.CredentialHealth)
			routes.GET("/metadata", r.instanceHandler.AllMetadata)
			routes.GET("/metadata/:instanceId", r.instanceHandler.Metadata)
			routes.GET("/all", r.instanceHandler.All)
			routes.GET("/info/:instanceId", r.instanceHandler.Info)
			routes.DELETE("/delete/:instanceId", r.instanceHandler.Delete)
			routes.POST("/proxy/:instanceId", r.instanceHandler.SetProxy)
			routes.DELETE("/proxy/:instanceId", r.instanceHandler.DeleteProxy)
			routes.POST("/forcereconnect/:instanceId", r.instanceHandler.ForceReconnect)
			routes.GET("/logs/:instanceId", r.instanceHandler.GetLogs)
		}
	}

	routes = eng.Group("/instance")
	{
		routes.Use(r.authMiddleware.Auth)
		{
			routes.POST("/connect", r.instanceHandler.Connect)
			routes.GET("/status", r.instanceHandler.Status)
			routes.GET("/qr", r.instanceHandler.Qr)
			routes.POST("/pair", r.jidValidationMiddleware.ValidateNumberField(), r.instanceHandler.Pair)
			routes.POST("/disconnect", r.instanceHandler.Disconnect)
			routes.POST("/reconnect", r.instanceHandler.Reconnect)
			routes.DELETE("/logout", r.instanceHandler.Logout)
			routes.GET("/:instanceId/advanced-settings", r.instanceHandler.GetAdvancedSettings)
			routes.PUT("/:instanceId/advanced-settings", r.instanceHandler.UpdateAdvancedSettings)
		}
	}

	routes = eng.Group("/send")
	{
		routes.Use(r.authMiddleware.Auth)
		{
			routes.POST("/text", r.jidValidationMiddleware.ValidateNumberFieldWithFormatJid(), r.sendHandler.SendText)
			routes.POST("/link", r.jidValidationMiddleware.ValidateNumberFieldWithFormatJid(), r.sendHandler.SendLink)
			routes.POST("/media", r.jidValidationMiddleware.ValidateNumberFieldWithFormatJid(), r.sendHandler.SendMedia)
			routes.POST("/poll", r.jidValidationMiddleware.ValidateNumberFieldWithFormatJid(), r.sendHandler.SendPoll)
			routes.POST("/sticker", r.jidValidationMiddleware.ValidateNumberFieldWithFormatJid(), r.sendHandler.SendSticker)
			routes.POST("/location", r.jidValidationMiddleware.ValidateNumberFieldWithFormatJid(), r.sendHandler.SendLocation)
			routes.POST("/contact", r.jidValidationMiddleware.ValidateContactFields(), r.sendHandler.SendContact) // TODO: send multiple contacts
			routes.POST("/button", r.jidValidationMiddleware.ValidateNumberFieldWithFormatJid(), r.sendHandler.SendButton)
			routes.POST("/list", r.jidValidationMiddleware.ValidateNumberFieldWithFormatJid(), r.sendHandler.SendList)
			routes.POST("/carousel", r.jidValidationMiddleware.ValidateNumberFieldWithFormatJid(), r.sendHandler.SendCarousel)
			routes.POST("/status/text", r.sendHandler.SendStatusText)
			routes.POST("/status/media", r.sendHandler.SendStatusMedia)
		}
	}
	routes = eng.Group("/campaigns")
	{
		routes.Use(r.authMiddleware.Auth)
		{
			routes.POST("", r.campaignHandler.Create)
			routes.GET("", r.campaignHandler.List)
			routes.GET("/:campaignId", r.campaignHandler.Get)
			routes.GET("/:campaignId/recipients", r.campaignHandler.Recipients)
			routes.GET("/:campaignId/audit", r.campaignHandler.Audit)
			routes.POST("/:campaignId/schedule", r.campaignHandler.Schedule)
			routes.POST("/:campaignId/start", r.campaignHandler.Start)
			routes.POST("/:campaignId/pause", r.campaignHandler.Pause)
			routes.POST("/:campaignId/resume", r.campaignHandler.Resume)
			routes.POST("/:campaignId/abort", r.campaignHandler.Abort)
		}
	}
	routes = eng.Group("/user")
	{
		routes.Use(r.authMiddleware.Auth)
		{
			routes.POST("/info", r.jidValidationMiddleware.ValidateNumberField(), r.userHandler.GetUser)
			routes.POST("/check", r.jidValidationMiddleware.ValidateNumberFieldWithFormatJid(), r.userHandler.CheckUser)
			routes.POST("/avatar", r.jidValidationMiddleware.ValidateNumberField(), r.userHandler.GetAvatar)
			routes.GET("/contacts", r.userHandler.GetContacts)
			routes.GET("/contacts/search", r.userHandler.SearchContacts)
			routes.GET("/contact/:contactId", r.userHandler.GetContact)
			routes.GET("/privacy", r.userHandler.GetPrivacy)
			routes.POST("/privacy", r.userHandler.SetPrivacy)
			routes.POST("/block", r.jidValidationMiddleware.ValidateNumberField(), r.userHandler.BlockContact)
			routes.POST("/unblock", r.jidValidationMiddleware.ValidateNumberField(), r.userHandler.UnblockContact)
			routes.GET("/blocklist", r.userHandler.GetBlockList)
			routes.POST("/profilePicture", r.userHandler.SetProfilePicture)
			routes.POST("/profileName", r.userHandler.SetProfileName)
			routes.POST("/profileStatus", r.userHandler.SetProfileStatus)
		}
	}
	routes = eng.Group("/message")
	{
		routes.Use(r.authMiddleware.Auth)
		{
			routes.GET("/:messageId/delivery", r.messageHandler.Receipts)
			routes.POST("/react", r.jidValidationMiddleware.ValidateJIDFields("number"), r.messageHandler.React)
			routes.POST("/presence", r.jidValidationMiddleware.ValidateNumberField(), r.messageHandler.ChatPresence)
			routes.POST("/markread", r.jidValidationMiddleware.ValidateNumberField(), r.messageHandler.MarkRead)
			routes.POST("/markplayed", r.jidValidationMiddleware.ValidateNumberField(), r.messageHandler.MarkPlayed)
			routes.POST("/downloadmedia", r.messageHandler.DownloadMedia)
			routes.POST("/status", r.messageHandler.GetMessageStatus)
			routes.POST("/delete", r.jidValidationMiddleware.ValidateNumberField(), r.messageHandler.DeleteMessageEveryone)
			routes.POST("/edit", r.jidValidationMiddleware.ValidateNumberField(), r.messageHandler.EditMessage) // TODO: edit MediaMessage too
		}
	}
	r.assignConversationRoutes(eng)
	routes = eng.Group("/group")
	{
		routes.Use(r.authMiddleware.Auth)
		{
			routes.GET("/list", r.groupHandler.ListGroups)
			routes.GET("/search", r.groupHandler.SearchGroups)
			routes.GET("/summary", r.groupHandler.GroupSummary)
			routes.GET("/:groupJid/members", r.groupHandler.ListGroupMembers)
			routes.GET("/:groupJid/audit", r.groupHandler.GroupAudit)
			if r.groupHandler.PhotoAssetsEnabled() {
				routes.POST("/photo", r.groupHandler.SetGroupPhoto)
			} else {
				routes.POST("/photo", r.jidValidationMiddleware.ValidateNumberField(), r.groupHandler.SetGroupPhoto)
			}
			if r.groupHandler.ManagementContractEnabled() {
				routes.POST("/info", r.groupHandler.GetGroupInfo)
				routes.POST("/invitelink", r.groupHandler.GetGroupInviteLink)
				routes.POST("/name", r.groupHandler.SetGroupName)
				routes.POST("/description", r.groupHandler.SetGroupDescription)
				routes.POST("/create", r.groupHandler.CreateGroup)
				routes.POST("/participant", r.groupHandler.UpdateParticipant)
				routes.POST("/leave", r.groupHandler.LeaveGroup)
				routes.POST("/settings", r.groupHandler.UpdateGroupSettings)
			} else {
				routes.POST("/info", r.jidValidationMiddleware.ValidateNumberField(), r.groupHandler.GetGroupInfo)
				routes.POST("/invitelink", r.jidValidationMiddleware.ValidateNumberField(), r.groupHandler.GetGroupInviteLink)
				routes.POST("/name", r.jidValidationMiddleware.ValidateNumberField(), r.groupHandler.SetGroupName)
				routes.POST("/description", r.jidValidationMiddleware.ValidateNumberField(), r.groupHandler.SetGroupDescription)
				routes.POST("/create", r.jidValidationMiddleware.ValidateMultipleNumbers("participants"), r.groupHandler.CreateGroup)
				routes.POST("/participant", r.jidValidationMiddleware.ValidateJIDFields("number", "participants"), r.groupHandler.UpdateParticipant)
				routes.POST("/leave", r.jidValidationMiddleware.ValidateNumberField(), r.groupHandler.LeaveGroup)
				routes.POST("/settings", r.jidValidationMiddleware.ValidateNumberField(), r.groupHandler.UpdateGroupSettings)
			}
			routes.GET("/myall", r.groupHandler.GetMyGroups) // TODO: not working
			routes.POST("/join", r.groupHandler.JoinGroupLink)
		}
	}
	routes = eng.Group("/call")
	{
		routes.Use(r.authMiddleware.Auth)
		{
			routes.POST("/reject", r.jidValidationMiddleware.ValidateNumberField(), r.callHandler.RejectCall)
		}
	}
	routes = eng.Group("/community")
	{
		routes.Use(r.authMiddleware.Auth)
		{
			routes.POST("/create", r.communityHandler.CreateCommunity)
			routes.POST("/add", r.jidValidationMiddleware.ValidateJIDFields("number", "communityId"), r.communityHandler.CommunityAdd)
			routes.POST("/remove", r.jidValidationMiddleware.ValidateJIDFields("number", "communityId"), r.communityHandler.CommunityRemove)
		}
	}
	routes = eng.Group("/label")
	{
		routes.Use(r.authMiddleware.Auth)
		{
			routes.POST("/chat", r.jidValidationMiddleware.ValidateNumberField(), r.labelHandler.ChatLabel)
			routes.POST("/message", r.labelHandler.MessageLabel)
			routes.POST("/edit", r.labelHandler.EditLabel)
			routes.GET("/list", r.labelHandler.GetLabels)
			routes.GET("/info/:labelId", r.labelHandler.GetLabel)
		}
	}
	routes = eng.Group("/unlabel")
	{
		routes.Use(r.authMiddleware.Auth)
		{
			routes.POST("/chat", r.jidValidationMiddleware.ValidateNumberField(), r.labelHandler.ChatUnlabel)
			routes.POST("/message", r.labelHandler.MessageUnlabel)
		}
	}
	routes = eng.Group("/newsletter")
	{
		routes.Use(r.authMiddleware.Auth)
		{
			routes.POST("/create", r.newsletterHandler.CreateNewsletter)
			routes.GET("/list", r.newsletterHandler.ListNewsletter)
			routes.POST("/info", r.jidValidationMiddleware.ValidateJIDFields("newsletterId"), r.newsletterHandler.GetNewsletter)
			routes.POST("/link", r.jidValidationMiddleware.ValidateJIDFields("newsletterId"), r.newsletterHandler.GetNewsletterInvite)
			routes.POST("/subscribe", r.jidValidationMiddleware.ValidateJIDFields("newsletterId"), r.newsletterHandler.SubscribeNewsletter)
			routes.POST("/messages", r.jidValidationMiddleware.ValidateJIDFields("newsletterId"), r.newsletterHandler.GetNewsletterMessages)
		}
	}

	// NOVO: Rotas de Enquetes (Polls)
	routes = eng.Group("/polls")
	{
		routes.Use(r.authMiddleware.Auth)
		{
			routes.GET("/:pollMessageId/results", r.pollHandler.GetPollResults)
		}
	}

}

func (r *Routes) assignRuntimeHealthRoutes(eng *gin.Engine) {
	if r == nil || r.serverHandler == nil {
		return
	}
	eng.GET("/server/ok", r.serverHandler.ServerOk)
	eng.GET("/server/live", r.serverHandler.RuntimeLive)
	eng.GET("/server/ready", r.serverHandler.RuntimeReady)
}

func (r *Routes) assignConversationRoutes(eng *gin.Engine) {
	routes := eng.Group("/conversations")
	routes.Use(r.authMiddleware.Auth)
	routes.GET("", r.observeConversationAPI(observability.ConversationContractCanonical, observability.ConversationOperationList), r.chatHandler.ListConversations)
	routes.GET("/:conversationRef", r.observeConversationAPI(observability.ConversationContractCanonical, observability.ConversationOperationGet), r.chatHandler.GetConversation)
	routes.GET("/:conversationRef/messages", r.observeConversationAPI(observability.ConversationContractCanonical, observability.ConversationOperationMessages), r.chatHandler.ConversationMessages)
	routes.GET("/:conversationRef/messages/:messageId", r.observeConversationAPI(observability.ConversationContractCanonical, observability.ConversationOperationMessage), r.chatHandler.ConversationMessage)
	if r.chatHandler.ConversationAppStateCommandsEnabled() {
		routes.POST("/:conversationRef/archive", r.observeConversationAPI(observability.ConversationContractCanonical, observability.ConversationOperationArchive), r.chatHandler.ArchiveConversation)
		routes.DELETE("/:conversationRef/archive", r.observeConversationAPI(observability.ConversationContractCanonical, observability.ConversationOperationUnarchive), r.chatHandler.UnarchiveConversation)
		routes.POST("/:conversationRef/pin", r.observeConversationAPI(observability.ConversationContractCanonical, observability.ConversationOperationPin), r.chatHandler.PinConversation)
		routes.DELETE("/:conversationRef/pin", r.observeConversationAPI(observability.ConversationContractCanonical, observability.ConversationOperationUnpin), r.chatHandler.UnpinConversation)
		routes.PUT("/:conversationRef/mute", r.observeConversationAPI(observability.ConversationContractCanonical, observability.ConversationOperationMute), r.chatHandler.MuteConversation)
		routes.DELETE("/:conversationRef/mute", r.observeConversationAPI(observability.ConversationContractCanonical, observability.ConversationOperationUnmute), r.chatHandler.UnmuteConversation)
	}
	if r.chatHandler.ConversationHistorySyncEnabled() {
		routes.POST("/:conversationRef/history-sync", r.observeConversationAPI(observability.ConversationContractCanonical, observability.ConversationOperationHistorySync), r.chatHandler.ConversationHistorySync)
	}
}

func (r *Routes) assignMetricsRoute(eng *gin.Engine) {
	if r.metricsHandler == nil {
		return
	}
	eng.GET("/metrics", r.authMiddleware.AuthAdmin, gin.WrapH(r.metricsHandler))
}

func (r *Routes) observeConversationAPI(contract, operation string) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		startedAt := time.Now()
		ctx.Next()
		if r != nil && r.conversationObserver != nil {
			r.conversationObserver.ObserveConversationRequest(contract, operation, ctx.Writer.Status(), time.Since(startedAt))
		}
	}
}

func NewRouter(
	authMiddleware auth_middleware.Middleware,
	metricsHandler http.Handler,
	instanceHandler instance_handler.InstanceHandler,
	userHandler user_handler.UserHandler,
	sendHandler send_handler.SendHandler,
	messageHandler message_handler.MessageHandler,
	chatHandler chat_handler.ChatHandler,
	groupHandler group_handler.GroupHandler,
	groupListHandler group_list_handler.Handler,
	callHandler call_handler.CallHandler,
	campaignHandler campaign_handler.CampaignHandler,
	campaignMediaHandler campaign_handler.MediaHandler,
	mediaHandler media_handler.Handler,
	communityHandler community_handler.CommunityHandler,
	labelHandler label_handler.LabelHandler,
	newsletterHandler newsletter_handler.NewsletterHandler,
	pollHandler *poll_handler.PollHandler,
	serverHandler server_handler.ServerHandler,
	options ...Option,
) *Routes {
	result := &Routes{
		authMiddleware:          authMiddleware,
		metricsHandler:          metricsHandler,
		jidValidationMiddleware: auth_middleware.NewJIDValidationMiddleware(),
		instanceHandler:         instanceHandler,
		userHandler:             userHandler,
		sendHandler:             sendHandler,
		messageHandler:          messageHandler,
		chatHandler:             chatHandler,
		groupHandler:            groupHandler,
		groupListHandler:        groupListHandler,
		callHandler:             callHandler,
		campaignHandler:         campaignHandler,
		campaignMediaHandler:    campaignMediaHandler,
		mediaHandler:            mediaHandler,
		communityHandler:        communityHandler,
		labelHandler:            labelHandler,
		newsletterHandler:       newsletterHandler,
		pollHandler:             pollHandler,
		serverHandler:           serverHandler,
	}
	for _, option := range options {
		if option != nil {
			option(result)
		}
	}
	return result
}
