package envfile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kilian1103/usdc-liquidity-orchestrator/internal/envfile"
)

func TestLoad_MissingFile_Noop(t *testing.T) {
	err := envfile.Load(filepath.Join(t.TempDir(), "no-such.env"))
	require.NoError(t, err)
}

func TestLoad_SetsUnsetKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "# comment\n" +
		"ENVFILE_TEST_A=alpha\n" +
		"export ENVFILE_TEST_B=beta\n" +
		"ENVFILE_TEST_C=\"quoted value\"\n" +
		"\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	t.Setenv("ENVFILE_TEST_A", "")
	t.Setenv("ENVFILE_TEST_B", "")
	t.Setenv("ENVFILE_TEST_C", "")

	require.NoError(t, envfile.Load(path))
	assert.Equal(t, "alpha", os.Getenv("ENVFILE_TEST_A"))
	assert.Equal(t, "beta", os.Getenv("ENVFILE_TEST_B"))
	assert.Equal(t, "quoted value", os.Getenv("ENVFILE_TEST_C"))
}

func TestLoad_DoesNotOverrideExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	require.NoError(t, os.WriteFile(path, []byte("ENVFILE_TEST_KEEP=fromfile\n"), 0o600))
	t.Setenv("ENVFILE_TEST_KEEP", "already")

	require.NoError(t, envfile.Load(path))
	assert.Equal(t, "already", os.Getenv("ENVFILE_TEST_KEEP"))
}

func TestLoad_InvalidLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	require.NoError(t, os.WriteFile(path, []byte("NOT_A_KV\n"), 0o600))
	err := envfile.Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid line")
}