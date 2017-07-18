package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Sirupsen/logrus"
	"github.com/Sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/netlify/gocommerce/conf"
)

func TestTraceWrapper(t *testing.T) {
	l, hook := test.NewNullLogger()
	globalConfig := new(conf.GlobalConfiguration)
	config := new(conf.Configuration)
	config.Payment.Stripe.Enabled = true
	config.Payment.Stripe.SecretKey = "secret"

	api := NewAPI(globalConfig, config, nil)
	api.log = logrus.NewEntry(l)

	server := httptest.NewServer(api.handler)
	defer server.Close()

	client := http.Client{}
	rsp, err := client.Get(server.URL)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rsp.StatusCode)
	assert.True(t, len(hook.Entries) > 0)

	for _, entry := range hook.Entries {
		if _, ok := entry.Data["request_id"]; !ok {
			assert.Fail(t, "expected entry: request_id")
		}
		expected := map[string]string{
			"method": "GET",
			"path":   "/",
		}
		for k, v := range expected {
			if value, ok := entry.Data[k]; ok {
				assert.Equal(t, v, value)
			} else {
				assert.Fail(t, "expected entry: "+k)
			}
		}
	}
}
