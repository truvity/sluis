package server

// The states a workspace's connection is shown in, as the module words them.
const (
	slackNotConnected = "not_connected"
	slackCreated      = "created"
	slackInstalled    = "installed"
)

// The states a catalogue Slack App is shown in.
const (
	slackAppDeclared      = "declared"
	slackAppCreated       = "created"
	slackAppInstalled     = "installed"
	slackAppScopesMissing = "scopes_missing"
)

// The states a channel and a Slack Connect channel are shown in.
const (
	sharedNotReported = "not_reported"
	sharedPending     = "pending"
	sharedWaiting     = "waiting"
	sharedActive      = "active"
	sharedHeld        = "held"
	sharedInvalid     = "invalid"
)
