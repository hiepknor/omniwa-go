package waquery

// Stable operation names are shared by HTTP requests, internal callers,
// reconciliation jobs, logs, and metrics. Resource values provide the
// operation-specific identity used for single-flight coalescing.
const (
	OperationGroupsList           = "groups.list"
	OperationGroupInfo            = "groups.info"
	OperationGroupInviteLink      = "groups.invite_link"
	OperationGroupJoinRequests    = "groups.join_requests"
	OperationUserInfo             = "users.info"
	OperationUserExists           = "users.exists"
	OperationUserAvatar           = "users.avatar"
	OperationUserPrivacy          = "users.privacy"
	OperationUserBlocklist        = "users.blocklist"
	OperationNewslettersList      = "newsletters.list"
	OperationNewsletterInfo       = "newsletters.info"
	OperationNewsletterInviteInfo = "newsletters.invite_info"
	OperationNewsletterMessages   = "newsletters.messages"
)
