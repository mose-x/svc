package sdk

import "svc/internal/config"

// baseLocalStatus fills the SdkStatus fields shared by every fetcher's
// GetLocalStatus: installed/active versions, the configured flag, the
// needsSwitch dangling-reference check and the PATH availability probe for
// verifyCmd (SVC shims excluded). Fetchers with extra PATH-copy logic
// (Python classification, Node.js external managers) start from this base
// and amend the Path* fields.
func baseLocalStatus(cfg *config.Config, sdkType SdkType, verifyCmd string) *SdkStatus {
	installed, _ := cfg.GetInstalledVersions(string(sdkType))
	active := cfg.GetActiveVersion(string(sdkType))
	configured := active != ""

	needsSwitch := false
	if active != "" {
		found := false
		for _, v := range installed {
			if v == active {
				found = true
				break
			}
		}
		needsSwitch = !found
	}

	return &SdkStatus{
		SdkType:           sdkType,
		DisplayName:       SdkDisplayName(sdkType),
		Configured:        configured,
		PathConfigured:    !configured && IsCommandAvailable(verifyCmd),
		CurrentVersion:    active,
		InstalledVersions: installed,
		InstallPath:       cfg.SdkDir(string(sdkType)),
		NeedsSwitch:       needsSwitch,
	}
}
