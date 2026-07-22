package user_service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	logger_wrapper "github.com/evolution-foundation/evolution-go/pkg/logger"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	"github.com/evolution-foundation/evolution-go/pkg/utils"
	"github.com/evolution-foundation/evolution-go/pkg/waquery"
	whatsmeow_service "github.com/evolution-foundation/evolution-go/pkg/whatsmeow/service"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type UserService interface {
	GetUser(ctx context.Context, data *CheckUserStruct, instance *instance_model.Instance) (*UserCollection, error)
	CheckUser(ctx context.Context, data *CheckUserStruct, instance *instance_model.Instance) (*CheckUserCollection, error)
	GetAvatar(ctx context.Context, data *GetAvatarStruct, instance *instance_model.Instance) (*types.ProfilePictureInfo, error)
	GetContacts(context.Context, *instance_model.Instance) ([]ContactInfo, *projection_service.ProjectionReadMeta, error)
	SearchContacts(context.Context, *instance_model.Instance, string, int, string) ([]ContactInfo, *projection_service.ProjectionReadMeta, error)
	GetContact(context.Context, *instance_model.Instance, string) (*ContactInfo, *projection_service.ProjectionReadMeta, error)
	GetPrivacy(ctx context.Context, instance *instance_model.Instance) (types.PrivacySettings, error)
	SetPrivacy(ctx context.Context, data *PrivacyStruct, instance *instance_model.Instance) (*types.PrivacySettings, error)
	BlockContact(data *BlockStruct, instance *instance_model.Instance) (*types.Blocklist, error)
	UnlockContact(data *BlockStruct, instance *instance_model.Instance) (*types.Blocklist, error)
	GetBlockList(ctx context.Context, instance *instance_model.Instance) (*types.Blocklist, error)
	SetProfilePicture(data *SetProfilePictureStruct, instance *instance_model.Instance) (bool, error)
	SetProfileName(data *SetProfileNameStruct, instance *instance_model.Instance) (bool, error)
	SetProfileStatus(data *SetProfileStatusStruct, instance *instance_model.Instance) (bool, error)
}

type userService struct {
	clientPointer    map[string]*whatsmeow.Client
	whatsmeowService whatsmeow_service.WhatsmeowService
	loggerWrapper    *logger_wrapper.LoggerManager
	queryGuard       waquery.Guard
	identityResolver waquery.IdentityResolver
	contactReader    *projection_service.ContactReader
}

type ContactInfo struct {
	Jid              string     `json:"Jid"`
	Found            bool       `json:"Found"`
	FirstName        string     `json:"FirstName"`
	FullName         string     `json:"FullName"`
	PushName         string     `json:"PushName"`
	BusinessName     string     `json:"BusinessName"`
	PhoneJID         string     `json:"PhoneJID,omitempty"`
	LID              string     `json:"LID,omitempty"`
	Username         string     `json:"Username,omitempty"`
	RedactedPhone    string     `json:"RedactedPhone,omitempty"`
	PictureID        string     `json:"PictureID,omitempty"`
	PictureRemoved   *bool      `json:"PictureRemoved,omitempty"`
	PictureUpdatedAt *time.Time `json:"PictureUpdatedAt,omitempty"`
	About            string     `json:"About,omitempty"`
	AboutUpdatedAt   *time.Time `json:"AboutUpdatedAt,omitempty"`
}

type UserInfo struct {
	VerifiedName *types.VerifiedName
	Status       string
	PictureID    string
	Devices      []types.JID
	LID          *string // The local ID (if available)
}

type UserCollection struct {
	Users map[types.JID]UserInfo
}

type User struct {
	Query        string
	IsInWhatsapp bool
	JID          string
	RemoteJID    string
	LID          *string
	VerifiedName string
}

type CheckUserCollection struct {
	Users []User
	Stale bool `json:"-"`
}

type CheckUserStruct struct {
	Number    []string `json:"number"`
	FormatJid *bool    `json:"formatJid,omitempty"`
}

type GetAvatarStruct struct {
	Number  string `json:"number"`
	Preview bool   `json:"preview"`
}

type BlockStruct struct {
	Number string `json:"number"`
}

type SetProfilePictureStruct struct {
	Image string `json:"image"`
}

type SetProfileNameStruct struct {
	Name string `json:"name"`
}

type SetProfileStatusStruct struct {
	Status string `json:"status"`
}

type PrivacyStruct struct {
	GroupAdd     types.PrivacySetting `json:"groupAdd"`
	LastSeen     types.PrivacySetting `json:"lastSeen"`
	Status       types.PrivacySetting `json:"status"`
	Profile      types.PrivacySetting `json:"profile"`
	ReadReceipts types.PrivacySetting `json:"readReceipts"`
	CallAdd      types.PrivacySetting `json:"callAdd"`
	Online       types.PrivacySetting `json:"online"`
}

func (u *userService) ensureClientConnected(instanceId string) (*whatsmeow.Client, error) {
	client := u.clientPointer[instanceId]
	u.loggerWrapper.GetLogger(instanceId).LogInfo("[%s] Checking client connection status - Client exists: %v", instanceId, client != nil)

	if client == nil {
		u.loggerWrapper.GetLogger(instanceId).LogInfo("[%s] No client found, attempting to start new instance", instanceId)
		err := u.whatsmeowService.StartInstance(instanceId)
		if err != nil {
			u.loggerWrapper.GetLogger(instanceId).LogError("[%s] Failed to start instance: %v", instanceId, err)
			return nil, errors.New("no active session found")
		}

		u.loggerWrapper.GetLogger(instanceId).LogInfo("[%s] Instance started, waiting 2 seconds...", instanceId)
		time.Sleep(2 * time.Second)

		client = u.clientPointer[instanceId]
		u.loggerWrapper.GetLogger(instanceId).LogInfo("[%s] Checking new client - Exists: %v, Connected: %v",
			instanceId,
			client != nil,
			client != nil && client.IsConnected())

		if client == nil || !client.IsConnected() {
			u.loggerWrapper.GetLogger(instanceId).LogError("[%s] New client validation failed - Exists: %v, Connected: %v",
				instanceId,
				client != nil,
				client != nil && client.IsConnected())
			return nil, errors.New("no active session found")
		}
	} else if !client.IsConnected() {
		u.loggerWrapper.GetLogger(instanceId).LogError("[%s] Existing client is disconnected - Connected status: %v",
			instanceId,
			client.IsConnected())
		return nil, errors.New("client disconnected")
	}

	u.loggerWrapper.GetLogger(instanceId).LogInfo("[%s] Client successfully validated - Connected: %v", instanceId, client.IsConnected())
	return client, nil
}

func (u *userService) GetUser(ctx context.Context, data *CheckUserStruct, instance *instance_model.Instance) (*UserCollection, error) {
	client, err := u.ensureClientConnected(instance.Id)
	if err != nil {
		return nil, err
	}

	var jids []types.JID
	for _, arg := range data.Number {
		jid, ok := utils.ParseJID(arg)
		if !ok {
			return nil, errors.New("invalid phone number")
		}
		jids = append(jids, jid)
	}
	resources := make([]string, len(jids))
	for i, jid := range jids {
		resources[i] = jid.String()
	}
	resp, err := waquery.Do(ctx, u.queryGuard, instance.Id, waquery.OperationUserInfo, waquery.ResourceKey(resources...), func(queryCtx context.Context) (map[types.JID]types.UserInfo, error) {
		return client.GetUserInfo(queryCtx, jids)
	})
	if err != nil {
		return nil, err
	}

	uc := new(UserCollection)
	uc.Users = make(map[types.JID]UserInfo)

	for jid, whatsmeowInfo := range resp {
		// Consultar LID Store para obter LID associado ao JID
		var lidStr *string
		if client.Store.LIDs != nil {
			if lid, err := client.Store.LIDs.GetLIDForPN(context.TODO(), jid); err == nil && !lid.IsEmpty() {
				lidString := fmt.Sprintf("%v", lid)
				lidStr = &lidString
			}
		}

		// Converter para nossa estrutura UserInfo que inclui LID
		info := UserInfo{
			VerifiedName: whatsmeowInfo.VerifiedName,
			Status:       whatsmeowInfo.Status,
			PictureID:    whatsmeowInfo.PictureID,
			Devices:      whatsmeowInfo.Devices,
			LID:          lidStr,
		}
		uc.Users[jid] = info
	}

	return uc, nil
}

func (u *userService) CheckUser(ctx context.Context, data *CheckUserStruct, instance *instance_model.Instance) (*CheckUserCollection, error) {
	client, err := u.ensureClientConnected(instance.Id)
	if err != nil {
		return nil, err
	}

	// Set formatJid to false by default for CheckUser
	formatJid := false
	if data.FormatJid != nil {
		formatJid = *data.FormatJid
	}

	// First attempt with the requested formatJid setting
	uc, shouldRetry, err := u.performCheckUser(ctx, client, data.Number, formatJid, instance.Id)
	if err != nil {
		return nil, err
	}
	if !shouldRetry {
		return uc, nil
	}

	// If formatJid was true and we got false results, retry with formatJid=false
	if formatJid {
		u.loggerWrapper.GetLogger(instance.Id).LogInfo("[%s] Some users not found with formatJid=true, retrying with formatJid=false", instance.Id)
		ucRetry, _, err := u.performCheckUser(ctx, client, data.Number, false, instance.Id)
		if err != nil {
			return nil, err
		}

		// Merge results: use retry results for users that weren't found in first attempt
		return u.mergeCheckUserResults(uc, ucRetry), nil
	}

	return uc, nil
}

// performCheckUser executes the actual user check with specified formatJid
func (u *userService) performCheckUser(ctx context.Context, client *whatsmeow.Client, numbers []string, formatJid bool, instanceId string) (*CheckUserCollection, bool, error) {
	// Use centralized function to prepare numbers for WhatsApp check
	phoneNumbers, err := utils.PrepareNumbersForWhatsAppCheck(numbers, &formatJid)
	if err != nil {
		u.loggerWrapper.GetLogger(instanceId).LogWarn("[%s] Failed to prepare numbers for WhatsApp check: %v", instanceId, err)
		return nil, false, nil
	}

	query := func(queryCtx context.Context, missing []string) ([]types.IsOnWhatsAppResponse, error) {
		return client.IsOnWhatsApp(queryCtx, missing)
	}
	var resp []types.IsOnWhatsAppResponse
	stale := false
	if reader, ok := u.identityResolver.(waquery.IdentityReadResolver); ok {
		result, resolveErr := reader.ResolveRead(ctx, instanceId, phoneNumbers, query)
		resp, stale, err = result.Responses, result.Stale, resolveErr
	} else {
		resp, err = u.identityResolver.Resolve(ctx, instanceId, phoneNumbers, query)
	}
	if err != nil {
		u.loggerWrapper.GetLogger(instanceId).LogError("[%s] Failed to check users on WhatsApp: %v", instanceId, err)
		var rateLimitErr *waquery.RateLimitError
		if errors.As(err, &rateLimitErr) {
			return nil, false, err
		}
		return nil, false, nil
	}

	uc := &CheckUserCollection{Stale: stale}
	shouldRetry := false

	for _, item := range resp {
		// Consultar LID Store para obter LID associado ao JID
		var lidStr *string
		if client.Store.LIDs != nil {
			if lid, err := client.Store.LIDs.GetLIDForPN(context.TODO(), item.JID); err == nil && !lid.IsEmpty() {
				lidString := fmt.Sprintf("%v", lid)
				lidStr = &lidString
			}
		}

		// Determine the RemoteJID to use for messaging
		remoteJID := item.Query // Default to original query
		if item.IsIn {
			// When user exists on WhatsApp, use the JID returned by WhatsApp
			remoteJID = fmt.Sprintf("%v", item.JID)
		} else if formatJid && !stale {
			// If user not found and we used formatJid=true, we should retry with formatJid=false
			shouldRetry = true
		}

		if item.VerifiedName != nil {
			var msg = User{
				Query:        item.Query,
				IsInWhatsapp: item.IsIn,
				JID:          fmt.Sprintf("%v", item.JID),
				RemoteJID:    remoteJID,
				LID:          lidStr,
				VerifiedName: item.VerifiedName.Details.GetVerifiedName(),
			}
			uc.Users = append(uc.Users, msg)
		} else {
			var msg = User{
				Query:        item.Query,
				IsInWhatsapp: item.IsIn,
				JID:          fmt.Sprintf("%v", item.JID),
				RemoteJID:    remoteJID,
				LID:          lidStr,
				VerifiedName: "",
			}
			uc.Users = append(uc.Users, msg)
		}
	}

	return uc, shouldRetry, nil
}

// mergeCheckUserResults merges results from two CheckUser attempts
// Priority: if a user is found in retry (formatJid=false), use that result
func (u *userService) mergeCheckUserResults(original, retry *CheckUserCollection) *CheckUserCollection {
	if retry == nil {
		return original
	}

	// Create a map of retry results by original query for quick lookup
	retryMap := make(map[string]User)
	for _, user := range retry.Users {
		retryMap[user.Query] = user
	}

	// Merge results
	merged := &CheckUserCollection{Stale: original.Stale || retry.Stale}
	for _, originalUser := range original.Users {
		if retryUser, exists := retryMap[originalUser.Query]; exists && retryUser.IsInWhatsapp && !originalUser.IsInWhatsapp {
			// Use retry result if it found the user and original didn't
			merged.Users = append(merged.Users, retryUser)
		} else {
			// Use original result
			merged.Users = append(merged.Users, originalUser)
		}
	}

	return merged
}

func (u *userService) GetAvatar(ctx context.Context, data *GetAvatarStruct, instance *instance_model.Instance) (*types.ProfilePictureInfo, error) {
	client, err := u.ensureClientConnected(instance.Id)
	if err != nil {
		return nil, err
	}

	// 🔒 FIX: Verificar se o cliente está conectado antes de fazer a requisição
	if !client.IsConnected() {
		return nil, errors.New("client is not connected to WhatsApp")
	}

	// 🔒 FIX: Verificar se o cliente está autenticado
	if !client.IsLoggedIn() {
		return nil, errors.New("client is not logged in to WhatsApp")
	}

	jid, ok := utils.ParseJID(data.Number)
	if !ok {
		return nil, errors.New("invalid phone number")
	}

	u.loggerWrapper.GetLogger(instance.Id).LogInfo("[%s] Requesting avatar for JID: %s, Preview: %v", instance.Id, jid, data.Preview)

	var pic *types.ProfilePictureInfo

	// 🔒 FIX: Adicionar timeout ao contexto para evitar que a requisição trave indefinidamente
	// Usar timeout maior que o padrão do sendIQ (75s) para dar tempo suficiente
	ctx, cancel := context.WithTimeout(ctx, 80*time.Second)
	defer cancel()

	u.loggerWrapper.GetLogger(instance.Id).LogInfo("[%s] Starting GetProfilePictureInfo request...", instance.Id)
	resource := fmt.Sprintf("%s:preview=%t", jid.String(), data.Preview)
	pic, err = waquery.Do(ctx, u.queryGuard, instance.Id, waquery.OperationUserAvatar, resource, func(queryCtx context.Context) (*types.ProfilePictureInfo, error) {
		return client.GetProfilePictureInfo(queryCtx, jid, &whatsmeow.GetProfilePictureParams{Preview: data.Preview})
	})
	if err != nil {
		u.loggerWrapper.GetLogger(instance.Id).LogError("[%s] GetProfilePictureInfo failed: %v", instance.Id, err)
		return nil, err
	}

	if pic == nil {
		return nil, errors.New("no profile picture found")
	}

	u.loggerWrapper.GetLogger(instance.Id).LogInfo("[%s] Avatar lookup succeeded", instance.Id)

	return pic, nil
}

func (u *userService) GetContacts(ctx context.Context, instance *instance_model.Instance) ([]ContactInfo, *projection_service.ProjectionReadMeta, error) {
	if instance == nil || u.contactReader == nil {
		return nil, nil, errors.New("contact projection reader and instance are required")
	}
	contacts, meta, err := u.contactReader.List(ctx, instance.Id)
	if err != nil {
		return nil, nil, err
	}
	result := make([]ContactInfo, len(contacts))
	for index := range contacts {
		result[index] = contactInfoFromProjection(&contacts[index])
	}
	return result, meta, nil
}

func (u *userService) SearchContacts(ctx context.Context, instance *instance_model.Instance, term string, limit int, cursor string) ([]ContactInfo, *projection_service.ProjectionReadMeta, error) {
	if instance == nil || u.contactReader == nil {
		return nil, nil, errors.New("contact projection reader and instance are required")
	}
	contacts, meta, err := u.contactReader.Search(ctx, instance.Id, term, limit, cursor)
	if err != nil {
		return nil, nil, err
	}
	result := make([]ContactInfo, len(contacts))
	for index := range contacts {
		result[index] = contactInfoFromProjection(&contacts[index])
	}
	return result, meta, nil
}

func (u *userService) GetContact(ctx context.Context, instance *instance_model.Instance, jid string) (*ContactInfo, *projection_service.ProjectionReadMeta, error) {
	if instance == nil || u.contactReader == nil || jid == "" {
		return nil, nil, errors.New("contact projection reader, instance, and JID are required")
	}
	contact, meta, err := u.contactReader.GetByJID(ctx, instance.Id, jid)
	if err != nil {
		return nil, nil, err
	}
	if contact == nil {
		return nil, nil, errors.New("projected contact is missing")
	}
	result := contactInfoFromProjection(contact)
	return &result, meta, nil
}

func contactInfoFromProjection(contact *projection_model.Contact) ContactInfo {
	result := ContactInfo{Jid: contact.PreferredJID, Found: contact.Found}
	if contact.FirstName != nil {
		result.FirstName = *contact.FirstName
	}
	if contact.FullName != nil {
		result.FullName = *contact.FullName
	}
	if contact.PushName != nil {
		result.PushName = *contact.PushName
	}
	if contact.BusinessName != nil {
		result.BusinessName = *contact.BusinessName
	}
	if contact.PhoneJID != nil {
		result.PhoneJID = *contact.PhoneJID
	}
	if contact.LID != nil {
		result.LID = *contact.LID
	}
	if contact.Username != nil {
		result.Username = *contact.Username
	}
	if contact.RedactedPhone != nil {
		result.RedactedPhone = *contact.RedactedPhone
	}
	if contact.PictureID != nil {
		result.PictureID = *contact.PictureID
	}
	result.PictureRemoved = contact.PictureRemoved
	if contact.PictureUpdatedAt != nil {
		value := contact.PictureUpdatedAt.UTC()
		result.PictureUpdatedAt = &value
	}
	if contact.About != nil {
		result.About = *contact.About
	}
	if contact.AboutUpdatedAt != nil {
		value := contact.AboutUpdatedAt.UTC()
		result.AboutUpdatedAt = &value
	}
	return result
}

func (u *userService) GetPrivacy(ctx context.Context, instance *instance_model.Instance) (types.PrivacySettings, error) {
	client, err := u.ensureClientConnected(instance.Id)
	if err != nil {
		var rateLimitErr *waquery.RateLimitError
		if errors.As(err, &rateLimitErr) {
			return types.PrivacySettings{}, err
		}
		// Preserve GetPrivacySettings' historical behavior for non-rate-limit
		// provider errors while making known throttling machine-readable.
		return types.PrivacySettings{}, nil
	}

	privacy, err := waquery.Do(ctx, u.queryGuard, instance.Id, waquery.OperationUserPrivacy, "", func(queryCtx context.Context) (*types.PrivacySettings, error) {
		return client.TryFetchPrivacySettings(queryCtx, false)
	})
	if err != nil {
		return types.PrivacySettings{}, err
	}
	return *privacy, nil
}

func (u *userService) SetPrivacy(ctx context.Context, data *PrivacyStruct, instance *instance_model.Instance) (*types.PrivacySettings, error) {
	client, err := u.ensureClientConnected(instance.Id)
	if err != nil {
		return nil, err
	}

	privacySettings := []struct {
		name  types.PrivacySettingType
		value types.PrivacySetting
	}{
		{types.PrivacySettingTypeGroupAdd, data.GroupAdd},
		{types.PrivacySettingTypeLastSeen, data.LastSeen},
		{types.PrivacySettingTypeStatus, data.Status},
		{types.PrivacySettingTypeProfile, data.Profile},
		{types.PrivacySettingTypeReadReceipts, data.ReadReceipts},
		{types.PrivacySettingTypeCallAdd, data.CallAdd},
		{types.PrivacySettingTypeOnline, data.Online},
	}

	// Populate the provider cache through the query guard before mutations. This
	// prevents SetPrivacySetting from issuing an unguarded prerequisite query.
	if _, err := waquery.Do(ctx, u.queryGuard, instance.Id, waquery.OperationUserPrivacy, "", func(queryCtx context.Context) (*types.PrivacySettings, error) {
		return client.TryFetchPrivacySettings(queryCtx, false)
	}); err != nil {
		return nil, err
	}

	var privacy types.PrivacySettings
	for _, setting := range privacySettings {
		privacy, err = client.SetPrivacySetting(ctx, setting.name, setting.value)
		if err != nil {
			return nil, u.queryGuard.ObserveError(instance.Id, err)
		}
	}
	return &privacy, nil
}

func (u *userService) BlockContact(data *BlockStruct, instance *instance_model.Instance) (*types.Blocklist, error) {
	client, err := u.ensureClientConnected(instance.Id)
	if err != nil {
		return nil, err
	}

	jid, ok := utils.ParseJID(data.Number)
	if !ok {
		return nil, errors.New("invalid phone number")
	}

	resp, err := client.UpdateBlocklist(context.Background(), jid, events.BlocklistChangeActionBlock)
	if err != nil {
		return nil, err
	}

	return resp, nil
}

func (u *userService) UnlockContact(data *BlockStruct, instance *instance_model.Instance) (*types.Blocklist, error) {
	client, err := u.ensureClientConnected(instance.Id)
	if err != nil {
		return nil, err
	}

	jid, ok := utils.ParseJID(data.Number)
	if !ok {
		return nil, errors.New("invalid phone number")
	}

	resp, err := client.UpdateBlocklist(context.Background(), jid, events.BlocklistChangeActionUnblock)
	if err != nil {
		return nil, err
	}

	return resp, nil
}

func (u *userService) GetBlockList(ctx context.Context, instance *instance_model.Instance) (*types.Blocklist, error) {
	client, err := u.ensureClientConnected(instance.Id)
	if err != nil {
		return nil, err
	}

	resp, err := waquery.Do(ctx, u.queryGuard, instance.Id, waquery.OperationUserBlocklist, "", client.GetBlocklist)
	if err != nil {
		return nil, err
	}

	return resp, nil
}

func (u *userService) SetProfilePicture(data *SetProfilePictureStruct, instance *instance_model.Instance) (bool, error) {
	client, err := u.ensureClientConnected(instance.Id)
	if err != nil {
		return false, err
	}

	var filedata []byte

	resp, err := http.Get(data.Image)
	if err != nil {
		return false, fmt.Errorf("failed to fetch image from URL: %v", err)
	}
	defer resp.Body.Close()

	filedata, err = io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("failed to read image data: %v", err)
	}

	_, err = client.SetGroupPhoto(context.Background(), types.EmptyJID, filedata)
	if err != nil {
		return false, err
	}

	return true, nil
}

func (u *userService) SetProfileName(data *SetProfileNameStruct, instance *instance_model.Instance) (bool, error) {
	client, err := u.ensureClientConnected(instance.Id)
	if err != nil {
		return false, err
	}

	err = client.SetGroupName(context.Background(), types.EmptyJID, data.Name)
	if err != nil {
		return false, err
	}

	return true, nil
}

func (u *userService) SetProfileStatus(data *SetProfileStatusStruct, instance *instance_model.Instance) (bool, error) {
	client, err := u.ensureClientConnected(instance.Id)
	if err != nil {
		return false, err
	}

	err = client.SetStatusMessage(context.Background(), data.Status)
	if err != nil {
		return false, err
	}

	return true, nil
}

func NewUserService(
	clientPointer map[string]*whatsmeow.Client,
	whatsmeowService whatsmeow_service.WhatsmeowService,
	queryGuard waquery.Guard,
	identityResolver waquery.IdentityResolver,
	contactReader *projection_service.ContactReader,
	loggerWrapper *logger_wrapper.LoggerManager,
) UserService {
	return &userService{
		clientPointer:    clientPointer,
		whatsmeowService: whatsmeowService,
		queryGuard:       queryGuard,
		identityResolver: identityResolver,
		contactReader:    contactReader,
		loggerWrapper:    loggerWrapper,
	}
}
