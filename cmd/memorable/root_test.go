package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootCmd_Help(t *testing.T) {
	t.Parallel()
	is := assert.New(t)

	cmd := newRootCmd(buildInfo{Version: "1.2.3", Commit: "abc", Date: "2026-11-14"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--help"})

	require.NoError(t, cmd.Execute())
	is.Contains(out.String(), "memorable")
	is.True(cmd.SilenceUsage)
	is.True(cmd.SilenceErrors)
}

func TestVersionCmd_JSON(t *testing.T) {
	t.Parallel()
	is := assert.New(t)

	cmd := newRootCmd(buildInfo{Version: "1.2.3", Commit: "abc", Date: "2026-11-14"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"version", "--output", "json"})

	require.NoError(t, cmd.Execute())
	var got buildInfo
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	is.Equal(buildInfo{Version: "1.2.3", Commit: "abc", Date: "2026-11-14"}, got)
}
