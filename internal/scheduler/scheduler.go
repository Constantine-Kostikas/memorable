// Package scheduler installs a daily backup job: a systemd user timer on Linux, a launchd agent on macOS.
package scheduler

import (
	"fmt"
	"html"
	"path/filepath"
)

// File is a unit file to write.
type File struct {
	Path    string
	Content string
}

const (
	unitName  = "memorable-backup"
	plistName = "com.memorable.backup.plist"
)

// Plan returns the files to write and the commands that enable the job.
func Plan(goos, binPath, home string, uid int) ([]File, [][]string, error) {
	switch goos {
	case "linux":
		dir := filepath.Join(home, ".config", "systemd", "user")
		service := fmt.Sprintf(`[Unit]
Description=memorable daily database backup

[Service]
Type=oneshot
ExecStart=%s backup
`, binPath)
		timer := `[Unit]
Description=Run the memorable backup daily

[Timer]
OnCalendar=daily
Persistent=true

[Install]
WantedBy=timers.target
`
		files := []File{
			{Path: filepath.Join(dir, unitName+".service"), Content: service},
			{Path: filepath.Join(dir, unitName+".timer"), Content: timer},
		}
		cmds := [][]string{
			{"systemctl", "--user", "daemon-reload"},
			{"systemctl", "--user", "enable", "--now", unitName + ".timer"},
		}
		return files, cmds, nil
	case "darwin":
		path := filepath.Join(home, "Library", "LaunchAgents", plistName)
		logPath := filepath.Join(home, "Library", "Logs", "memorable-backup.log")
		plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.memorable.backup</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>backup</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin</string>
  </dict>
  <key>StartCalendarInterval</key>
  <dict>
    <key>Hour</key>
    <integer>3</integer>
    <key>Minute</key>
    <integer>0</integer>
  </dict>
  <key>StandardErrorPath</key>
  <string>%s</string>
</dict>
</plist>
`, html.EscapeString(binPath), html.EscapeString(logPath))
		return []File{{Path: path, Content: plist}},
			[][]string{{"launchctl", "bootstrap", fmt.Sprintf("gui/%d", uid), path}}, nil
	default:
		return nil, nil, fmt.Errorf("scheduled backups are not supported on %s", goos)
	}
}
