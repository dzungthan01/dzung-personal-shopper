package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStartRefusesHTTPWithoutToken(t *testing.T) {
	t.Setenv("SHOPPER_HTTP_TOKEN", "")
	databasePath := filepath.Join(t.TempDir(), "test.db")

	err := runStart([]string{"--http", "127.0.0.1:0", "--db", databasePath})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "SHOPPER_HTTP_TOKEN")
	_, statErr := os.Stat(databasePath)
	assert.True(t, os.IsNotExist(statErr), "refused before touching the database")
}

func TestStartRejectsUnknownTool(t *testing.T) {
	err := runStart([]string{"--tools", "list_items,list_itmes", "--db", filepath.Join(t.TempDir(), "test.db")})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "list_itmes")
}

func TestParseToolList(t *testing.T) {
	assert.Empty(t, parseToolList(""))
	assert.Equal(t, []string{"list_items", "add_item"}, parseToolList(" list_items, ,add_item,"))
}
