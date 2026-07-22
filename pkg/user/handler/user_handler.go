package user_handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/evolution-foundation/evolution-go/pkg/httpapi"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	user_service "github.com/evolution-foundation/evolution-go/pkg/user/service"
	"github.com/gin-gonic/gin"
	"go.mau.fi/whatsmeow/types"
	"gorm.io/gorm"
)

type UserHandler interface {
	GetUser(ctx *gin.Context)
	CheckUser(ctx *gin.Context)
	GetAvatar(ctx *gin.Context)
	GetContacts(ctx *gin.Context)
	SearchContacts(ctx *gin.Context)
	GetContact(ctx *gin.Context)
	GetPrivacy(ctx *gin.Context)
	SetPrivacy(ctx *gin.Context)
	BlockContact(ctx *gin.Context)
	UnblockContact(ctx *gin.Context)
	GetBlockList(ctx *gin.Context)
	SetProfilePicture(ctx *gin.Context)
	SetProfileName(ctx *gin.Context)
	SetProfileStatus(ctx *gin.Context)
}

type userHandler struct {
	userService user_service.UserService
}

const defaultContactSearchLimit = 50

// Get a user
// @Summary Get a user
// @Description Get a user
// @Tags User
// @Accept json
// @Produce json
// @Param message body user_service.CheckUserStruct true "User data"
// @Success 200 {object} apidocs.SuccessResponse{data=user_service.UserCollection} "success"
// @Failure 400 {object} apidocs.ErrorResponse "Error on validation"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Failure 429 {object} apidocs.RateLimitResponse "Information query rate limited; see Retry-After header"
// @Security ApiKeyAuth
// @Router /user/info [post]
func (u *userHandler) GetUser(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "instance not found"})
		return
	}

	var data *user_service.CheckUserStruct
	err := ctx.ShouldBindBodyWithJSON(&data)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if len(data.Number) < 1 {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "phone number is required"})
		return
	}

	uc, err := u.userService.GetUser(ctx.Request.Context(), data, instance)
	if err != nil {
		if httpapi.WriteRateLimit(ctx, err) {
			return
		}
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": uc})
}

// Check a user
// @Summary Check a user
// @Description Check a user
// @Tags User
// @Accept json
// @Produce json
// @Param message body user_service.CheckUserStruct true "User data"
// @Success 200 {object} apidocs.SuccessResponse{data=user_service.CheckUserCollection} "success"
// @Failure 400 {object} apidocs.ErrorResponse "Error on validation"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Failure 429 {object} apidocs.RateLimitResponse "Information query rate limited; see Retry-After header"
// @Security ApiKeyAuth
// @Router /user/check [post]
func (u *userHandler) CheckUser(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "instance not found"})
		return
	}

	var data *user_service.CheckUserStruct
	err := ctx.ShouldBindBodyWithJSON(&data)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if len(data.Number) < 1 {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "phone number is required"})
		return
	}

	uc, err := u.userService.CheckUser(ctx.Request.Context(), data, instance)
	if err != nil {
		if httpapi.WriteRateLimit(ctx, err) {
			return
		}
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	response := gin.H{"message": "success", "data": uc}
	if uc.Stale {
		response["meta"] = gin.H{"source": "cache", "stale": true}
	}
	ctx.JSON(http.StatusOK, response)
}

// Get a user's avatar
// @Summary Get a user's avatar
// @Description Get a user's avatar
// @Tags User
// @Accept json
// @Produce json
// @Param message body user_service.GetAvatarStruct true "Avatar data"
// @Success 200 {object} apidocs.SuccessResponse{data=types.ProfilePictureInfo} "success"
// @Failure 400 {object} apidocs.ErrorResponse "Error on validation"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Failure 429 {object} apidocs.RateLimitResponse "Information query rate limited; see Retry-After header"
// @Security ApiKeyAuth
// @Router /user/avatar [post]
func (u *userHandler) GetAvatar(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "instance not found"})
		return
	}

	var data *user_service.GetAvatarStruct
	err := ctx.ShouldBindBodyWithJSON(&data)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if len(data.Number) < 1 {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "phone number is required"})
		return
	}

	if data.Number == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "phone number is required"})
		return
	}

	pic, err := u.userService.GetAvatar(ctx.Request.Context(), data, instance)
	if err != nil {
		if httpapi.WriteRateLimit(ctx, err) {
			return
		}
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": pic})
}

// Get a user's contacts
// @Summary Get a user's contacts
// @Description Get a user's contacts
// @Tags User
// @Accept json
// @Produce json
// @Success 200 {object} apidocs.SuccessResponse{data=[]user_service.ContactInfo} "success"
// @Failure 503 {object} apidocs.ErrorResponse "Projection not ready"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /user/contacts [get]
func (u *userHandler) GetContacts(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "instance not found"})
		return
	}

	contacts, meta, err := u.userService.GetContacts(ctx.Request.Context(), instance)
	if err != nil {
		writeContactReadError(ctx, err)
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": contacts, "meta": meta})
}

// Search projected contacts
// @Summary Search projected contacts
// @Description Prefix-search normalized contacts from the persisted instance projection without querying WhatsApp
// @Tags User
// @Produce json
// @Param q query string false "Case-insensitive prefix matched against normalized contact fields" maxlength(128)
// @Param limit query int false "Page size (1-200)" minimum(1) maximum(200) default(50)
// @Param cursor query string false "Opaque cursor bound to the normalized search query"
// @Success 200 {object} apidocs.SuccessResponse{data=[]user_service.ContactInfo} "success"
// @Failure 400 {object} apidocs.ErrorResponse "Invalid search or cursor"
// @Failure 503 {object} apidocs.ErrorResponse "Projection not ready"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /user/contacts/search [get]
func (u *userHandler) SearchContacts(ctx *gin.Context) {
	instance, ok := ctx.MustGet("instance").(*instance_model.Instance)
	if !ok {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "instance not found"})
		return
	}
	limit := defaultContactSearchLimit
	if value := ctx.Query("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 200 {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": "limit must be between 1 and 200", "code": "invalid_pagination"})
			return
		}
		limit = parsed
	}
	contacts, meta, err := u.userService.SearchContacts(ctx.Request.Context(), instance, ctx.Query("q"), limit, ctx.Query("cursor"))
	if err != nil {
		writeContactReadError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": contacts, "meta": meta})
}

// Get a projected contact
// @Summary Get a projected contact
// @Description Get one normalized contact from the persisted instance projection
// @Tags User
// @Produce json
// @Param contactId path string true "Contact JID"
// @Success 200 {object} apidocs.SuccessResponse{data=user_service.ContactInfo} "success"
// @Failure 400 {object} apidocs.ErrorResponse "Invalid contact JID"
// @Failure 404 {object} apidocs.ErrorResponse "Contact not found"
// @Failure 503 {object} apidocs.ErrorResponse "Projection not ready"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /user/contact/{contactId} [get]
func (u *userHandler) GetContact(ctx *gin.Context) {
	instance, ok := ctx.MustGet("instance").(*instance_model.Instance)
	if !ok {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "instance not found"})
		return
	}
	contactID := ctx.Param("contactId")
	jid, err := types.ParseJID(contactID)
	if err != nil || jid.IsEmpty() || !isContactJID(jid) {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid contact JID"})
		return
	}
	contact, meta, err := u.userService.GetContact(ctx.Request.Context(), instance, jid.ToNonAD().String())
	if err != nil {
		writeContactReadError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": contact, "meta": meta})
}

func isContactJID(jid types.JID) bool {
	switch jid.ToNonAD().Server {
	case types.DefaultUserServer, types.LegacyUserServer, types.HiddenUserServer, types.HostedLIDServer:
		return true
	default:
		return false
	}
}

func writeContactReadError(ctx *gin.Context, err error) {
	switch {
	case errors.Is(err, projection_service.ErrInvalidContactCursor):
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid contact search cursor", "code": "invalid_cursor"})
	case errors.Is(err, projection_service.ErrInvalidContactSearch):
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid contact search query", "code": "invalid_search"})
	case errors.Is(err, projection_service.ErrContactsProjectionNotReady):
		ctx.JSON(http.StatusServiceUnavailable, gin.H{"error": "contacts projection is not ready", "code": "projection_not_ready"})
	case errors.Is(err, gorm.ErrRecordNotFound):
		ctx.JSON(http.StatusNotFound, gin.H{"error": "contact not found", "code": "not_found"})
	default:
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
	}
}

// Get a user's privacy settings
// @Summary Get a user's privacy settings
// @Description Get a user's privacy settings
// @Tags User
// @Accept json
// @Produce json
// @Success 200 {object} apidocs.SuccessResponse{data=types.PrivacySettings} "success"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Failure 429 {object} apidocs.RateLimitResponse "Information query rate limited; see Retry-After header"
// @Security ApiKeyAuth
// @Router /user/privacy [get]
func (u *userHandler) GetPrivacy(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "instance not found"})
		return
	}

	privacy, err := u.userService.GetPrivacy(ctx.Request.Context(), instance)
	if err != nil {
		if httpapi.WriteRateLimit(ctx, err) {
			return
		}
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": privacy})
}

// Set a user's privacy settings
// @Summary Set a user's privacy settings
// @Description Set a user's privacy settings
// @Tags User
// @Accept json
// @Produce json
// @Param message body user_service.PrivacyStruct true "Privacy data"
// @Success 200 {object} apidocs.SuccessResponse{data=types.PrivacySettings} "success"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Failure 429 {object} apidocs.RateLimitResponse "Information query rate limited; see Retry-After header"
// @Security ApiKeyAuth
// @Router /user/privacy [post]
func (u *userHandler) SetPrivacy(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "instance not found"})
		return
	}

	var data *user_service.PrivacyStruct
	err := ctx.ShouldBindBodyWithJSON(&data)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if data.CallAdd == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "call add is required"})
		return
	}

	if data.GroupAdd == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "group add is required"})
		return
	}

	if data.LastSeen == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "last seen is required"})
		return
	}

	if data.Online == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "online is required"})
		return
	}

	if data.Profile == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "profile is required"})
		return
	}

	if data.ReadReceipts == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "read receipts is required"})
		return
	}

	if data.Status == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "status is required"})
		return
	}

	privacy, err := u.userService.SetPrivacy(ctx.Request.Context(), data, instance)
	if err != nil {
		if httpapi.WriteRateLimit(ctx, err) {
			return
		}
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": privacy})
}

// Block a contact
// @Summary Block a contact
// @Description Block a contact
// @Tags User
// @Accept json
// @Produce json
// @Param message body user_service.BlockStruct true "Block data"
// @Success 200 {object} apidocs.SuccessResponse{data=types.Blocklist} "success"
// @Failure 400 {object} apidocs.ErrorResponse "Error on validation"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /user/block [post]
func (u *userHandler) BlockContact(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "instance not found"})
		return
	}

	var data *user_service.BlockStruct
	err := ctx.ShouldBindBodyWithJSON(&data)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if len(data.Number) < 1 {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "phone number is required"})
		return
	}

	if data.Number == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "phone number is required"})
		return
	}

	resp, err := u.userService.BlockContact(data, instance)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": resp})
}

// Unblock a contact
// @Summary Unblock a contact
// @Description Unblock a contact
// @Tags User
// @Accept json
// @Produce json
// @Param message body user_service.BlockStruct true "Block data"
// @Success 200 {object} apidocs.SuccessResponse{data=types.Blocklist} "success"
// @Failure 400 {object} apidocs.ErrorResponse "Error on validation"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /user/unblock [post]
func (u *userHandler) UnblockContact(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "instance not found"})
		return
	}

	var data *user_service.BlockStruct
	err := ctx.ShouldBindBodyWithJSON(&data)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if len(data.Number) < 1 {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "phone number is required"})
		return
	}

	if data.Number == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "phone number is required"})
		return
	}

	resp, err := u.userService.UnlockContact(data, instance)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": resp})
}

// Get a user's block list
// @Summary Get a user's block list
// @Description Get a user's block list
// @Tags User
// @Accept json
// @Produce json
// @Success 200 {object} apidocs.SuccessResponse{data=types.Blocklist} "success"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Failure 429 {object} apidocs.RateLimitResponse "Information query rate limited; see Retry-After header"
// @Security ApiKeyAuth
// @Router /user/blocklist [get]
func (u *userHandler) GetBlockList(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "instance not found"})
		return
	}

	resp, err := u.userService.GetBlockList(ctx.Request.Context(), instance)
	if err != nil {
		if httpapi.WriteRateLimit(ctx, err) {
			return
		}
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": resp})
}

// Set a user's profile picture
// @Summary Set a user's profile picture
// @Description Set a user's profile picture
// @Tags User
// @Accept json
// @Produce json
// @Param message body user_service.SetProfilePictureStruct true "Profile picture data"
// @Success 200 {object} apidocs.SuccessResponse "success"
// @Failure 400 {object} apidocs.ErrorResponse "Error on validation"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /user/profilePicture [post]
func (u *userHandler) SetProfilePicture(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "instance not found"})
		return
	}

	var data *user_service.SetProfilePictureStruct
	err := ctx.ShouldBindBodyWithJSON(&data)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if data.Image == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "image is required"})
		return
	}

	resp, err := u.userService.SetProfilePicture(data, instance)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if !resp {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "failed to set profile picture"})
		return
	}

	responseData := gin.H{"image": data.Image}

	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": responseData})
}

// Set a user's profile name
// @Summary Set a user's profile name
// @Description Set a user's profile name
// @Tags User
// @Accept json
// @Produce json
// @Param message body user_service.SetProfilePictureStruct true "Profile name data"
// @Success 200 {object} apidocs.SuccessResponse "success"
// @Failure 400 {object} apidocs.ErrorResponse "Error on validation"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /user/profileName [post]
func (u *userHandler) SetProfileName(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "instance not found"})
		return
	}

	var data *user_service.SetProfileNameStruct
	err := ctx.ShouldBindBodyWithJSON(&data)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if data.Name == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}

	resp, err := u.userService.SetProfileName(data, instance)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if !resp {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "failed to set profile picture"})
		return
	}

	responseData := gin.H{"name": data.Name}

	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": responseData})
}

// Set a user's profile status
// @Summary Set a user's profile status
// @Description Set a user's profile status
// @Tags User
// @Accept json
// @Produce json
// @Param message body user_service.SetProfilePictureStruct true "Profile status data"
// @Success 200 {object} apidocs.SuccessResponse "success"
// @Failure 400 {object} apidocs.ErrorResponse "Error on validation"
// @Failure 500 {object} apidocs.ErrorResponse "Internal server error"
// @Security ApiKeyAuth
// @Router /user/profileStatus [post]
func (u *userHandler) SetProfileStatus(ctx *gin.Context) {
	getInstance := ctx.MustGet("instance")

	instance, ok := getInstance.(*instance_model.Instance)
	if !ok {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "instance not found"})
		return
	}

	var data *user_service.SetProfileStatusStruct
	err := ctx.ShouldBindBodyWithJSON(&data)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if data.Status == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}

	resp, err := u.userService.SetProfileStatus(data, instance)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if !resp {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "failed to set profile picture"})
		return
	}

	responseData := gin.H{"status": data.Status}

	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": responseData})
}

func NewUserHandler(
	userService user_service.UserService,
) UserHandler {
	return &userHandler{
		userService: userService,
	}
}
