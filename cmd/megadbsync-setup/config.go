//go:build windows

package main

// setupVersion is set at link time from CI (-X main.setupVersion=v1.0.0).
var setupVersion = "dev"

const (
	productName      = "MegaDBSync"
	defaultRepo      = "themobileprof/megadbsync"
	defaultAsset     = "megadbsync.exe"
	defaultInstallDir = `C:\MegaDBSync`
	setupCopyName    = "MegaDBSync-Setup.exe"
	versionFileName  = "version.txt"
	readmeFileName   = "README.txt"
	scheduledTaskName = "MegaDBSync"
)

type ghRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

type installInfo struct {
	Dir     string
	ExePath string
	Version string
}

type setupOptions struct {
	installDir      string
	version         string
	repo            string
	asset           string
	addr            string
	forceStart      bool
	forceUninstall  bool
	forceUpgrade    bool
	forceInstall    bool
	autostartLogon  *bool
	autoStartEngine *bool
}
