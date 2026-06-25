//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func runInstall(opts setupOptions, existing installInfo, isUpgrade bool) {
	installDir := opts.installDir
	if existing.Dir != "" {
		installDir = existing.Dir
	}
	exePath := filepath.Join(installDir, defaultAsset)
	dataDir := filepath.Join(installDir, "data")

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		fatal("create data directory: %v", err)
	}

	title := "Installing MegaDBSync"
	if isUpgrade {
		title = "Upgrading MegaDBSync"
	}
	printlnSection(title)
	fmt.Printf("  Install: %s\n", installDir)
	fmt.Printf("  Data:    %s\n", dataDir)
	fmt.Printf("  Release: %s (%s)\n", opts.version, opts.repo)

	url, tag, err := resolveAssetURL(opts.repo, opts.version, opts.asset)
	if err != nil {
		fatal("%v", err)
	}
	fmt.Printf("  Downloading %s from %s ...\n", opts.asset, tag)

	tmp := exePath + ".download"
	if err := downloadFile(url, tmp); err != nil {
		fatal("download: %v", err)
	}
	stopApp()
	if err := os.Rename(tmp, exePath); err != nil {
		fatal("finalize install: %v", err)
	}
	unblockFile(exePath)

	if err := copySetupToInstallDir(installDir); err != nil {
		fmt.Printf("  warning: could not copy setup to install folder: %v\n", err)
	}
	if err := writeVersionFile(installDir, tag); err != nil {
		fmt.Printf("  warning: could not write version file: %v\n", err)
	}

	info := installInfo{Dir: installDir, ExePath: exePath, Version: normalizeVersion(tag)}
	if err := registerInstall(info, tag, opts.addr); err != nil {
		fmt.Printf("  warning: could not register uninstall info: %v\n", err)
	}

	autostart := resolveAutostartLogon(opts.autostartLogon)
	autoEngine := resolveAutoStartEngine(opts.autoStartEngine)
	if autostart {
		if err := createLogonTask(exePath, opts.addr, dataDir); err != nil {
			fmt.Printf("  warning: could not create logon task: %v\n", err)
		} else {
			fmt.Println("  Scheduled task created: starts at Windows sign-in.")
		}
	}
	if autoEngine {
		if err := writeSetupPending(dataDir, true); err != nil {
			fmt.Printf("  warning: could not write setup flags: %v\n", err)
		}
	}

	if err := createStartMenuShortcuts(installDir, exePath, opts.addr, dataDir); err != nil {
		fmt.Printf("  warning: could not create Start Menu shortcuts: %v\n", err)
	}
	_ = writeInstallReadme(installDir, opts.addr)

	printlnSection("Setup complete")
	fmt.Printf("Installed: %s (%s)\n", exePath, normalizeVersion(tag))
	fmt.Printf("Data:      %s\n", dataDir)
	fmt.Printf("Dashboard: http://%s\n", opts.addr)
	fmt.Printf("\nUpgrade or uninstall later:\n")
	fmt.Printf("  %s\n", filepath.Join(installDir, setupCopyName))
	fmt.Printf("  Settings -> Apps -> %s\n", productName)

	startNow := opts.forceStart || promptYesNo("Start MegaDBSync now?", true)
	if startNow {
		fmt.Println("\nStarting MegaDBSync...")
		if err := launchApp(exePath, installDir, opts.addr, dataDir); err != nil {
			fatal("start app: %v", err)
		}
		fmt.Println("MegaDBSync is running in a new window.")
	}
}
