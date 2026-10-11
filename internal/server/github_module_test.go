package server

import (
	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"

	ghconsole "github.com/truvity/sluis/internal/githubapp/console"
	"github.com/truvity/sluis/internal/githubroster/consolewire"
)

// githubModuleOf is the GitHub module in the process of a console under test,
// built over the console's stores as they are at its first call.
func githubModuleOf(c *Console) *ghconsole.Service {
	c.githubModule()
	return c.github
}

// refreshGitHubModule gives the module the console's stores as they are now,
// for a test that replaces one after the first call.
func refreshGitHubModule(c *Console) { c.githubModule(); c.github.SetDeps(c.githubDeps()) }

// The words of the Apps contract the tests read.
const (
	appTokens     = directoryrosterv1.AppPurpose_APP_PURPOSE_TOKENS
	appPreset     = directoryrosterv1.AppOrigin_APP_ORIGIN_PRESET
	appCatalogue  = directoryrosterv1.AppOrigin_APP_ORIGIN_CATALOGUE
	appNotCreated = directoryrosterv1.AppState_APP_STATE_NOT_CREATED
	appInstalled  = directoryrosterv1.AppState_APP_STATE_INSTALLED
	appDone       = directoryrosterv1.AppAttention_APP_ATTENTION_DONE
	appNeedsYou   = directoryrosterv1.AppAttention_APP_ATTENTION_NEEDS_YOU
)

// The states a catalogue App is shown in.
const (
	catalogueNotCreated = "not_created"
	catalogueCreated    = "created"
	catalogueInstalled  = "installed"
	catalogueDrifted    = "drifted"
	appDrifted          = directoryrosterv1.AppState_APP_STATE_DRIFTED
)

const (
	githubCatalogueCallbackPath = consolewire.CatalogueCallbackPath
	githubCatalogueSetupPath    = consolewire.CatalogueSetupPath
	githubRunnerCallbackPath    = consolewire.RunnerCallbackPath
	githubRunnerSetupPath       = consolewire.RunnerSetupPath
	githubCallbackPath          = consolewire.CallbackPath
	githubSetupPath             = consolewire.SetupPath
	githubBind                  = consolewire.BindOrg
	appLink                     = directoryrosterv1.AppPurpose_APP_PURPOSE_LINK
	appController               = directoryrosterv1.AppPurpose_APP_PURPOSE_CONTROLLER
	appRunners                  = directoryrosterv1.AppPurpose_APP_PURPOSE_RUNNERS

	githubLinkPath            = consolewire.LinkPath
	githubLinkCallbackPath    = consolewire.LinkCallbackPath
	githubLinkAppCallbackPath = consolewire.LinkAppCallbackPath
	githubLinkAppBind         = consolewire.BindLinkApp
	githubLinkBind            = consolewire.BindLink
)
