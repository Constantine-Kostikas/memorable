package scheduler

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlan_Linux(t *testing.T) {
	t.Parallel()
	files, cmds, err := Plan("linux", "/usr/local/bin/memorable", "/home/u", 1000)
	require.NoError(t, err)
	require.Len(t, files, 2)
	assert.Equal(t, "/home/u/.config/systemd/user/memorable-backup.service", files[0].Path)
	assert.Contains(t, files[0].Content, "ExecStart=/usr/local/bin/memorable backup")
	assert.Contains(t, files[0].Content, "Type=oneshot")
	assert.Equal(t, "/home/u/.config/systemd/user/memorable-backup.timer", files[1].Path)
	assert.Contains(t, files[1].Content, "OnCalendar=daily")
	assert.Contains(t, files[1].Content, "Persistent=true")
	assert.Equal(t, [][]string{
		{"systemctl", "--user", "daemon-reload"},
		{"systemctl", "--user", "enable", "--now", "memorable-backup.timer"},
	}, cmds)
}

func TestPlan_Darwin(t *testing.T) {
	t.Parallel()
	files, cmds, err := Plan("darwin", "/opt/homebrew/bin/memorable", "/Users/u", 501)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "/Users/u/Library/LaunchAgents/com.memorable.backup.plist", files[0].Path)
	c := files[0].Content
	assert.Contains(t, c, "<string>/opt/homebrew/bin/memorable</string>")
	assert.Contains(t, c, "<string>backup</string>")
	assert.Contains(t, c, "<key>Hour</key>")
	assert.True(t, strings.HasPrefix(c, "<?xml"))
	assert.Equal(t, [][]string{{"launchctl", "bootstrap", "gui/501", "/Users/u/Library/LaunchAgents/com.memorable.backup.plist"}}, cmds)
}

func TestPlan_Unsupported(t *testing.T) {
	t.Parallel()
	_, _, err := Plan("windows", "x", "y", 0)
	require.Error(t, err)
}
