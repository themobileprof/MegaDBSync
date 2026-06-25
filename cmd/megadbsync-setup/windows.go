//go:build windows

package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/themobileprof/megadbsync/internal/platform"
)

func stopApp() {
	_ = exec.Command("taskkill", "/IM", defaultAsset, "/F").Run()
}

func launchApp(exePath, workDir, addr, dataDir string) error {
	cmd := exec.Command("cmd.exe", "/c", "start", "", exePath, "-addr", addr, "-data", dataDir)
	cmd.Dir = workDir
	return cmd.Run()
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		os.Remove(dst)
		return err
	}
	return out.Close()
}

func copySetupToInstallDir(installDir string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	dest := filepath.Join(installDir, setupCopyName)
	if err := copyFile(self, dest); err != nil {
		return err
	}
	unblockFile(dest)
	return nil
}

func createLogonTask(exePath, addr, dataDir string) error {
	tr := fmt.Sprintf(`\"%s\" -addr %s -data \"%s\"`, exePath, addr, dataDir)
	cmd := exec.Command("schtasks", "/Create",
		"/TN", scheduledTaskName,
		"/TR", tr,
		"/SC", "ONLOGON",
		"/RL", "LIMITED",
		"/F",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("schtasks: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func removeLogonTask() {
	_ = exec.Command("schtasks", "/Delete", "/TN", scheduledTaskName, "/F").Run()
}

func createStartMenuShortcuts(installDir, exePath, addr, dataDir string) error {
	setupPath := filepath.Join(installDir, setupCopyName)
	ps := fmt.Sprintf(`
$dir = Join-Path $env:APPDATA "Microsoft\Windows\Start Menu\Programs\MegaDBSync"
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$sh = New-Object -ComObject WScript.Shell
$s = $sh.CreateShortcut((Join-Path $dir "MegaDBSync.lnk"))
$s.TargetPath = '%s'
$s.Arguments = '-addr %s -data %s'
$s.WorkingDirectory = '%s'
$s.Description = 'Oracle to SQL Server migration dashboard'
$s.Save()
$s2 = $sh.CreateShortcut((Join-Path $dir "MegaDBSync Setup.lnk"))
$s2.TargetPath = '%s'
$s2.WorkingDirectory = '%s'
$s2.Description = 'Install, upgrade, or uninstall MegaDBSync'
$s2.Save()
$s3 = $sh.CreateShortcut((Join-Path $dir "Uninstall MegaDBSync.lnk"))
$s3.TargetPath = '%s'
$s3.Arguments = '-uninstall'
$s3.WorkingDirectory = '%s'
$s3.Description = 'Remove MegaDBSync'
$s3.Save()
`, escapePS(exePath), addr, escapePS(dataDir), escapePS(installDir),
		escapePS(setupPath), escapePS(installDir),
		escapePS(setupPath), escapePS(installDir))

	cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", ps)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("shortcuts: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func removeStartMenuShortcuts() {
	ps := `Remove-Item -Recurse -Force -ErrorAction SilentlyContinue (Join-Path $env:APPDATA "Microsoft\Windows\Start Menu\Programs\MegaDBSync")`
	_ = exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", ps).Run()
}

func escapePS(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

func writeInstallReadme(installDir, addr string) error {
	text := fmt.Sprintf(`%s
===========

Dashboard: http://%s

Install location: %s
Data (settings, connections, jobs): %s\data

Upgrade or repair
-----------------
Run MegaDBSync-Setup.exe in this folder, or re-run the setup you downloaded.

Uninstall
---------
- Settings -> Apps -> MegaDBSync -> Uninstall
- Start Menu -> MegaDBSync -> Uninstall MegaDBSync
- Run: MegaDBSync-Setup.exe -uninstall

Start at Windows logon
----------------------
If you enabled this during setup, a scheduled task named "%s" starts the app when you sign in.
Remove it during uninstall, or: schtasks /Delete /TN "%s" /F

Migration engine
----------------
For scheduled incremental sync, open the dashboard and ensure the migration engine is running.
Setup can enable "auto-start engine" so it starts with the app.
`, productName, addr, installDir, installDir, scheduledTaskName, scheduledTaskName)
	return os.WriteFile(filepath.Join(installDir, readmeFileName), []byte(text), 0o644)
}

func writeSetupPending(dataDir string, autoStartEngine bool) error {
	return platform.WriteSetupPending(dataDir, platform.SetupPending{AutoStartEngine: autoStartEngine})
}

func resolveAutostartLogon(opt *bool) bool {
	if opt != nil {
		return *opt
	}
	return promptYesNo("Start MegaDBSync when you sign in to Windows?", true)
}

func resolveAutoStartEngine(opt *bool) bool {
	if opt != nil {
		return *opt
	}
	return promptYesNo("Auto-start the migration engine when the app starts? (needed for scheduled incremental sync)", true)
}
