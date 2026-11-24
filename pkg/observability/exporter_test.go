package observability

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExporterCreatedWithValidConfig(t *testing.T) {
	cfg := Config{
		Enabled:          true,
		PublicKey:        "pk-lf-test",
		SecretKey:        "sk-lf-test",
		BaseURL:          DefaultBaseURL,
		SampleRate:       1.0,
		ExportTimeoutSec: 5,
		FlushTimeoutSec:  5,
		MaxContentBytes:  8192,
	}
	exp, err := New(cfg)
	require.NoError(t, err)
	require.NotNil(t, exp)
	require.True(t, exp.IsEnabled())
	t.Cleanup(func() { require.NoError(t, exp.Shutdown(context.Background())) })
}

func TestExporterDisabled(t *testing.T) {
	cfg := Config{Enabled: false}
	exp, err := New(cfg)
	require.NoError(t, err)
	require.NotNil(t, exp)
	assert.False(t, exp.IsEnabled())
}

func TestExporterInvalidConfig(t *testing.T) {
	cfg := Config{
		Enabled:   true,
		PublicKey: "pk",
		SecretKey: "sk",
		BaseURL:   "http://bad.example.com", // not https
	}
	exp, err := New(cfg)
	require.Error(t, err)
	assert.False(t, exp.IsEnabled())
}

func TestExporterMissingKeys(t *testing.T) {
	cfg := Config{
		Enabled:    true,
		BaseURL:    DefaultBaseURL,
		SampleRate: 1.0,
	}
	exp, err := New(cfg)
	require.Error(t, err)
	assert.False(t, exp.IsEnabled())
	assert.Nil(t, exp.provider)
}

func TestExporterMaskedKeys(t *testing.T) {
	cfg := Config{
		Enabled:    true,
		PublicKey:  "pk-lf-••••",
		SecretKey:  "sk-lf-******",
		BaseURL:    DefaultBaseURL,
		SampleRate: 1.0,
	}
	exp, err := New(cfg)
	require.Error(t, err)
	assert.False(t, exp.IsEnabled())
}

func TestGlobalTracerNoop(t *testing.T) {
	SetGlobalExporter(nil)
	assert.False(t, IsEnabled())
	tr := GlobalTracer("test")
	require.NotNil(t, tr)
}

func TestSetGlobalExporter(t *testing.T) {
	cfg := Config{
		Enabled:          true,
		PublicKey:        "pk-lf-test",
		SecretKey:        "sk-lf-test",
		BaseURL:          DefaultBaseURL,
		SampleRate:       1.0,
		ExportTimeoutSec: 5,
		FlushTimeoutSec:  5,
		MaxContentBytes:  8192,
	}
	exp, err := New(cfg)
	require.NoError(t, err)
	SetGlobalExporter(exp)
	t.Cleanup(func() { SetGlobalExporter(nil); require.NoError(t, exp.Shutdown(context.Background())) })
	assert.True(t, IsEnabled())
	tr := GlobalTracer("test")
	require.NotNil(t, tr)
}
