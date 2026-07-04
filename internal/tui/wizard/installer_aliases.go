package wizard

import (
	"github.com/tis24dev/proxsave/internal/installer"
)

// Bridge aliases while the remaining tview wizard screens (install,
// telegram, post-install audit) are still alive: the data layer moved to
// internal/installer (Phase 4) and these keep the wizard package and its
// external callers compiling unchanged until the Charm front replaces the
// screens.
type (
	InstallWizardPrefill       = installer.InstallWizardPrefill
	InstallWizardData          = installer.InstallWizardData
	PostInstallAuditSuggestion = installer.PostInstallAuditSuggestion
)

var (
	ApplyInstallData                     = installer.ApplyInstallData
	DeriveInstallWizardPrefill           = installer.DeriveInstallWizardPrefill
	CollectPostInstallDisableSuggestions = installer.CollectPostInstallDisableSuggestions
	ErrNilInstallData                    = installer.ErrNilInstallData

	setEnvValue                         = installer.SetEnvValueInTemplate
	unsetEnvValue                       = installer.UnsetEnvValueInTemplate
	parseEnvTemplate                    = installer.ParseEnvTemplate
	installEmailDeliveryMethodOrDefault = installer.EmailDeliveryMethodOrDefault
	validateSecondaryInstallData        = installer.ValidateSecondaryInstallData
	validateCloudInstallData            = installer.ValidateCloudInstallData
)
