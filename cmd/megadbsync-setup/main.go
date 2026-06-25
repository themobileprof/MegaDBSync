//go:build windows

package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	installDir := flag.String("install-dir", defaultInstallDir, "Installation directory")
	version := flag.String("version", "latest", "Release tag (e.g. v1.0.0) or latest")
	repo := flag.String("repo", defaultRepo, "GitHub owner/repo for releases")
	asset := flag.String("asset", defaultAsset, "Release asset file name")
	addr := flag.String("addr", "127.0.0.1:8080", "Listen address for MegaDBSync")
	start := flag.Bool("start", false, "Start MegaDBSync after install without prompting")
	uninstall := flag.Bool("uninstall", false, "Uninstall MegaDBSync")
	upgrade := flag.Bool("upgrade", false, "Download latest version and upgrade")
	install := flag.Bool("install", false, "Force install/upgrade flow (skip menu)")
	autostart := flag.Bool("autostart", false, "Create Windows logon scheduled task")
	noAutostart := flag.Bool("no-autostart", false, "Do not create logon scheduled task")
	autoEngine := flag.Bool("auto-start-engine", false, "Auto-start migration engine on first app launch")
	noAutoEngine := flag.Bool("no-auto-start-engine", false, "Do not auto-start migration engine")
	flag.Parse()

	opts := setupOptions{
		installDir: *installDir,
		version:    *version,
		repo:       *repo,
		asset:      *asset,
		addr:       *addr,
		forceStart: *start,
	}
	if *autostart {
		v := true
		opts.autostartLogon = &v
	}
	if *noAutostart {
		v := false
		opts.autostartLogon = &v
	}
	if *autoEngine {
		v := true
		opts.autoStartEngine = &v
	}
	if *noAutoEngine {
		v := false
		opts.autoStartEngine = &v
	}

	fmt.Printf("%s Setup %s\n", productName, setupVersion)

	existing, installed := detectInstall(*installDir)

	if *uninstall {
		runUninstall(existing)
		return
	}

	if *upgrade {
		if !installed {
			fatal("MegaDBSync is not installed — run setup without -upgrade to install.")
		}
		opts.installDir = existing.Dir
		opts.version = "latest"
		runInstall(opts, existing, true)
		return
	}

	if installed && !*install {
		if handleExistingInstall(existing, opts) {
			return
		}
	}

	runInstall(opts, existing, installed)
}

func handleExistingInstall(existing installInfo, opts setupOptions) bool {
	latestVer := ""
	if _, tag, err := resolveAssetURL(opts.repo, "latest", opts.asset); err == nil {
		latestVer = normalizeVersion(tag)
	}

	if latestVer != "" && existing.Version != "" && versionLess(existing.Version, latestVer) {
		fmt.Printf("\nUpdate available: %s -> %s\n", existing.Version, latestVer)
		switch promptMenu("What would you like to do?", []string{
			fmt.Sprintf("Upgrade to %s", latestVer),
			"Run MegaDBSync",
			"Uninstall",
			"Exit",
		}) {
		case 1:
			o := opts
			o.installDir = existing.Dir
			o.version = "latest"
			runInstall(o, existing, true)
		case 2:
			mustLaunch(existing, opts.addr)
		case 3:
			runUninstall(existing)
		default:
		}
		return true
	}

	if existing.Version != "" {
		fmt.Printf("\nInstalled version: %s (up to date)\n", existing.Version)
	} else {
		fmt.Printf("\nMegaDBSync is installed at %s\n", existing.Dir)
	}

	switch promptMenu("What would you like to do?", []string{
		"Run MegaDBSync",
		"Reinstall / upgrade (download latest)",
		"Uninstall",
		"Exit",
	}) {
	case 1:
		mustLaunch(existing, opts.addr)
	case 2:
		o := opts
		o.installDir = existing.Dir
		o.version = "latest"
		runInstall(o, existing, true)
	case 3:
		runUninstall(existing)
	case 0, 4:
		os.Exit(0)
	}
	return true
}

func mustLaunch(existing installInfo, addr string) {
	dataDir := filepath.Join(existing.Dir, "data")
	if err := launchApp(existing.ExePath, existing.Dir, addr, dataDir); err != nil {
		fatal("start app: %v", err)
	}
}
