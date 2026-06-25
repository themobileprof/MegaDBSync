//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func runUninstall(info installInfo) {
	if info.Dir == "" {
		if reg := readInstallFromRegistry(); reg.Dir != "" {
			info = reg
		}
	}
	if info.Dir == "" {
		info.Dir = defaultInstallDir
		info.ExePath = filepath.Join(info.Dir, defaultAsset)
	}

	if _, err := os.Stat(info.ExePath); err != nil {
		fatal("MegaDBSync does not appear to be installed at %s", info.Dir)
	}

	printlnSection("Uninstall MegaDBSync")
	fmt.Printf("Install location: %s\n", info.Dir)

	if !promptYesNo("Uninstall MegaDBSync?", false) {
		fmt.Println("Uninstall cancelled.")
		return
	}

	deleteData := promptYesNo("Delete data folder (connections, jobs, settings)?", false)

	stopApp()
	removeLogonTask()
	removeStartMenuShortcuts()
	unregisterInstall()

	files := []string{
		filepath.Join(info.Dir, defaultAsset),
		filepath.Join(info.Dir, setupCopyName),
		filepath.Join(info.Dir, versionFileName),
		filepath.Join(info.Dir, readmeFileName),
	}
	for _, f := range files {
		_ = os.Remove(f)
	}
	if deleteData {
		_ = os.RemoveAll(filepath.Join(info.Dir, "data"))
	}
	_ = os.Remove(info.Dir)

	printlnSection("Uninstall complete")
	if deleteData {
		fmt.Println("MegaDBSync and all data were removed.")
	} else {
		fmt.Printf("MegaDBSync was removed. Data kept at %s\\data\n", info.Dir)
	}
}
