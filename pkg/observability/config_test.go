package observability

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadFromEnvDefaults(t *testing.T) {
	for _, name := range []string{"LANGFUSE_ENABLED", "LANGFUSE_PUBLIC_KEY", "LANGFUSE_SECRET_KEY", "LANGFUSE_BASE_URL", "LANGFUSE_ENVIRONMENT", "LANGFUSE_SAMPLE_RATE", "LANGFUSE_CAPTURE_CONTENT", "LANGFUSE_MAX_CONTENT_BYTES", "LANGFUSE_EXPORT_TIMEOUT_SECONDS", "LANGFUSE_FLUSH_TIMEOUT_SECONDS"} {
		t.Setenv(name, "")
	}

	cfg := LoadFromEnv()
	assert.False(t, cfg.Enabled)
	assert.Equal(t, DefaultBaseURL, cfg.BaseURL)
	assert.Equal(t, DefaultEnvironment, cfg.Environment)
	assert.Equal(t, DefaultSampleRate, cfg.SampleRate)
	assert.False(t, cfg.CaptureContent)
	assert.Equal(t, DefaultMaxContentBytes, cfg.MaxContentBytes)
	assert.Equal(t, DefaultExportTimeoutSec, cfg.ExportTimeoutSec)
	assert.Equal(t, DefaultFlushTimeoutSec, cfg.FlushTimeoutSec)
	assert.Empty(t, cfg.PublicKey)
	assert.Empty(t, cfg.SecretKey)
}

func TestConfigValidateDisabled(t *testing.T) {
	cfg := Config{Enabled: false}
	require.NoError(t, cfg.Validate())
}

func TestConfigValidateMissingKeys(t *testing.T) {
	cfg := Config{Enabled: true, BaseURL: DefaultBaseURL}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing")
}

func TestConfigValidateMaskedKeys(t *testing.T) {
	cfg := Config{
		Enabled:    true,
		PublicKey:  "pk-lf-••••••••",
		SecretKey:  "sk-lf-******",
		BaseURL:    DefaultBaseURL,
		SampleRate: 1.0,
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "masked")
}

func TestConfigValidateInvalidURLs(t *testing.T) {
	base := Config{
		Enabled:   true,
		PublicKey: "pk-lf-valid",
		SecretKey: "sk-lf-valid",
	}

	cases := []struct {
		name string
		url  string
		cont string
	}{
		{"http scheme", "http://us.cloud.langfuse.com", "scheme must be https"},
		{"empty host", "https://", "missing host"},
		{"path present", "https://us.cloud.langfuse.com/x", "must be an origin only"},
		{"query present", "https://us.cloud.langfuse.com?a=1", "must not contain query string"},
		{"fragment present", "https://us.cloud.langfuse.com#x", "must not contain fragment"},
		{"userinfo", "https://user:pass@us.cloud.langfuse.com", "must not contain userinfo"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := base
			cfg.BaseURL = c.url
			err := cfg.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.cont)
		})
	}
}

func validConfig() Config {
	return Config{
		Enabled:          true,
		PublicKey:        "pk-lf-valid",
		SecretKey:        "sk-lf-valid",
		BaseURL:          DefaultBaseURL,
		SampleRate:       1.0,
		MaxContentBytes:  DefaultMaxContentBytes,
		ExportTimeoutSec: DefaultExportTimeoutSec,
		FlushTimeoutSec:  DefaultFlushTimeoutSec,
	}
}

func TestConfigValidateValidURLs(t *testing.T) {
	for _, u := range []string{
		DefaultBaseURL,
		"https://us.cloud.langfuse.com/",
	} {
		cfg := validConfig()
		cfg.BaseURL = u
		require.NoError(t, cfg.Validate(), u)
	}
}

func TestConfigValidateSampleRate(t *testing.T) {
	for _, sr := range []float64{0, 0.5, 1} {
		cfg := validConfig()
		cfg.SampleRate = sr
		require.NoError(t, cfg.Validate(), "sample rate %g", sr)
	}

	for _, sr := range []float64{-0.1, 1.1} {
		cfg := validConfig()
		cfg.SampleRate = sr
		err := cfg.Validate()
		require.Error(t, err, "sample rate %g", sr)
	}
}

func TestConfigValidateContentBytes(t *testing.T) {
	base := validConfig()
	for _, b := range []int{MinContentBytes, MaxContentBytes} {
		cfg := base
		cfg.MaxContentBytes = b
		require.NoError(t, cfg.Validate(), "bytes %d", b)
	}
	for _, b := range []int{MinContentBytes - 1, MaxContentBytes + 1} {
		cfg := base
		cfg.MaxContentBytes = b
		err := cfg.Validate()
		require.Error(t, err, "bytes %d", b)
	}
}

func TestConfigValidateTimeouts(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*Config)
	}{
		{"export 1", func(c *Config) { c.ExportTimeoutSec = 1 }},
		{"export 30", func(c *Config) { c.ExportTimeoutSec = 30 }},
		{"export 0", func(c *Config) { c.ExportTimeoutSec = 0 }},
		{"export 31", func(c *Config) { c.ExportTimeoutSec = 31 }},
		{"flush 1", func(c *Config) { c.FlushTimeoutSec = 1 }},
		{"flush 30", func(c *Config) { c.FlushTimeoutSec = 30 }},
		{"flush 0", func(c *Config) { c.FlushTimeoutSec = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			tc.set(&cfg)
			err := cfg.Validate()
			switch tc.name {
			case "export 1", "export 30", "flush 1", "flush 30":
				require.NoError(t, err)
			default:
				require.Error(t, err)
			}
		})
	}
}

func TestIngestionURL(t *testing.T) {
	cfg := Config{BaseURL: DefaultBaseURL}
	u, err := cfg.IngestionURL()
	require.NoError(t, err)
	assert.Equal(t, "https://us.cloud.langfuse.com"+IngestionPath, u)
}

func TestBasicAuth(t *testing.T) {
	cfg := Config{PublicKey: "pk", SecretKey: "sk"}
	auth := cfg.BasicAuth()
	assert.NotEmpty(t, auth)
}

func TestInvalidEnvironmentNumbersDisableExport(t *testing.T) {
	for _, value := range []string{"not-a-number", "NaN", "Inf", "-1", "1.01"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("LANGFUSE_ENABLED", "true")
			t.Setenv("LANGFUSE_PUBLIC_KEY", "pk-lf-fixture")
			t.Setenv("LANGFUSE_SECRET_KEY", "sk-lf-fixture")
			t.Setenv("LANGFUSE_SAMPLE_RATE", value)
			exp, err := New(LoadFromEnv())
			require.Error(t, err)
			assert.False(t, exp.IsEnabled())
		})
	}
}

func TestConfigurationErrorsDoNotExposeURLSecrets(t *testing.T) {
	for _, url := range []string{"https://host/SECRET_CANARY", "https://host/%SECRET_CANARY", "SECRET_CANARY://host", "https://user:SECRET_CANARY@host"} {
		cfg := validConfig()
		cfg.BaseURL = url
		err := cfg.Validate()
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "SECRET_CANARY")
	}
	for _, key := range []string{"bad-prefix", "pk-lf-", "pk-lf-abc:SECRET_CANARY"} {
		cfg := validConfig()
		cfg.PublicKey = key
		_, err := New(cfg)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "SECRET_CANARY")
	}
}
