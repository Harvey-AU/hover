package lighthouse

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseReport_HappyPath(t *testing.T) {
	raw := []byte(`{
		"categories": {
			"performance": { "score": 0.87 }
		},
		"audits": {
			"largest-contentful-paint":  { "numericValue": 2400.4 },
			"cumulative-layout-shift":   { "numericValue": 0.083 },
			"interaction-to-next-paint": { "numericValue": 180 },
			"total-blocking-time":       { "numericValue": 240 },
			"first-contentful-paint":    { "numericValue": 1300.6 },
			"speed-index":               { "numericValue": 2900 },
			"server-response-time":      { "numericValue": 320 },
			"total-byte-weight":         { "numericValue": 1500000.7 }
		}
	}`)

	result, err := ParseReport(raw)
	require.NoError(t, err)

	require.NotNil(t, result.PerformanceScore)
	assert.Equal(t, 87, *result.PerformanceScore)
	require.NotNil(t, result.LCPMs)
	assert.Equal(t, 2400, *result.LCPMs)
	require.NotNil(t, result.CLS)
	assert.InDelta(t, 0.083, *result.CLS, 1e-9)
	require.NotNil(t, result.INPMs)
	assert.Equal(t, 180, *result.INPMs)
	require.NotNil(t, result.TBTMs)
	assert.Equal(t, 240, *result.TBTMs)
	require.NotNil(t, result.FCPMs)
	assert.Equal(t, 1301, *result.FCPMs)
	require.NotNil(t, result.SpeedIndexMs)
	assert.Equal(t, 2900, *result.SpeedIndexMs)
	require.NotNil(t, result.TTFBMs)
	assert.Equal(t, 320, *result.TTFBMs)
	require.NotNil(t, result.TotalByteWeight)
	assert.Equal(t, int64(1500001), *result.TotalByteWeight)
}

func TestParseReport_MissingAuditsRemainNil(t *testing.T) {
	// Lighthouse occasionally omits audits on pages it can't audit
	// cleanly (e.g. CLS on pages that never paint). Missing audit IDs
	// must surface as nil pointers so the lighthouse_runs row stores
	// NULL rather than a misleading zero.
	raw := []byte(`{
		"categories": {
			"performance": { "score": 0.5 }
		},
		"audits": {
			"largest-contentful-paint": { "numericValue": 3000 }
		}
	}`)

	result, err := ParseReport(raw)
	require.NoError(t, err)

	require.NotNil(t, result.PerformanceScore)
	assert.Equal(t, 50, *result.PerformanceScore)
	require.NotNil(t, result.LCPMs)
	assert.Equal(t, 3000, *result.LCPMs)
	assert.Nil(t, result.CLS)
	assert.Nil(t, result.INPMs)
	assert.Nil(t, result.TBTMs)
	assert.Nil(t, result.FCPMs)
	assert.Nil(t, result.SpeedIndexMs)
	assert.Nil(t, result.TTFBMs)
	assert.Nil(t, result.TotalByteWeight)
}

func TestParseReport_NullNumericValueStaysNil(t *testing.T) {
	// Lighthouse emits `"numericValue": null` for some audits when the
	// page failed to produce a measurement. Treat as missing.
	raw := []byte(`{
		"categories": { "performance": { "score": null } },
		"audits": {
			"largest-contentful-paint": { "numericValue": null }
		}
	}`)

	result, err := ParseReport(raw)
	require.NoError(t, err)
	assert.Nil(t, result.PerformanceScore)
	assert.Nil(t, result.LCPMs)
}

func TestParseReport_MalformedJSONErrors(t *testing.T) {
	_, err := ParseReport([]byte(`{not valid json`))
	assert.Error(t, err)
}

func TestParseReport_RoundsScoreHalfUp(t *testing.T) {
	cases := []struct {
		score float64
		want  int
	}{
		{0.0, 0},
		{0.005, 1}, // .5 rounds up via math.Round
		{0.494, 49},
		{0.495, 50},
		{0.999, 100},
		{1.0, 100},
	}
	for _, tc := range cases {
		t.Run("", func(t *testing.T) {
			raw := []byte(`{"categories":{"performance":{"score":` +
				floatString(tc.score) + `}},"audits":{}}`)
			r, err := ParseReport(raw)
			require.NoError(t, err)
			require.NotNil(t, r.PerformanceScore)
			assert.Equal(t, tc.want, *r.PerformanceScore)
		})
	}
}

func floatString(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}
