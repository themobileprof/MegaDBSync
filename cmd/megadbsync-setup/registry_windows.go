//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const uninstallRegKey = `Software\Microsoft\Windows\CurrentVersion\Uninstall\MegaDBSync`

func detectInstall(installDir string) (installInfo, bool) {
	if reg := readInstallFromRegistry(); reg.Dir != "" {
		if _, err := os.Stat(reg.ExePath); err == nil {
			return reg, true
		}
	}
	exePath := filepath.Join(installDir, defaultAsset)
	if _, err := os.Stat(exePath); err != nil {
		return installInfo{}, false
	}
	ver, _ := os.ReadFile(filepath.Join(installDir, versionFileName))
	return installInfo{
		Dir:     installDir,
		ExePath: exePath,
		Version: strings.TrimSpace(string(ver)),
	}, true
}

func readInstallFromRegistry() installInfo {
	for _, hive := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		k, err := registry.OpenKey(hive, uninstallRegKey, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		loc, _, err := k.GetStringValue("InstallLocation")
		_ = k.Close()
		if err != nil || strings.TrimSpace(loc) == "" {
			continue
		}
		dir := strings.TrimSpace(loc)
		ver, _ := os.ReadFile(filepath.Join(dir, versionFileName))
		return installInfo{
			Dir:     dir,
			ExePath: filepath.Join(dir, defaultAsset),
			Version: strings.TrimSpace(string(ver)),
		}
	}
	return installInfo{}
}

func registerInstall(info installInfo, version, addr string) error {
	setupPath := filepath.Join(info.Dir, setupCopyName)
	uninstall := fmt.Sprintf(`"%s" -uninstall`, setupPath)
	displayVersion := normalizeVersion(version)

	k, created, err := registry.CreateKey(registry.LOCAL_MACHINE, uninstallRegKey, registry.SET_VALUE)
	if err != nil {
		k, created, err = registry.CreateKey(registry.CURRENT_USER, uninstallRegKey, registry.SET_VALUE)
		if err != nil {
			return err
		}
	}
	defer k.Close()
	_ = created

	_ = k.SetStringValue("DisplayName", productName)
	_ = k.SetStringValue("DisplayVersion", displayVersion)
	_ = k.SetStringValue("Publisher", "MegaDBSync")
	_ = k.SetStringValue("InstallLocation", info.Dir)
	_ = k.SetStringValue("UninstallString", uninstall)
	_ = k.SetStringValue("DisplayIcon", info.ExePath)
	_ = k.SetStringValue("HelpLink", "https://github.com/themobileprof/mdas")
	_ = k.SetStringValue("URLInfoAbout", "https://github.com/themobileprof/mdas")
	_ = k.SetDWordValue("NoModify", 1)
	_ = k.SetDWordValue("NoRepair", 1)
	return nil
}

func unregisterInstall() {
	_ = registry.DeleteKey(registry.LOCAL_MACHINE, uninstallRegKey)
	_ = registry.DeleteKey(registry.CURRENT_USER, uninstallRegKey)
}

func writeVersionFile(dir, version string) error {
	return os.WriteFile(filepath.Join(dir, versionFileName), []byte(normalizeVersion(version)+"\n"), 0o644)
}
