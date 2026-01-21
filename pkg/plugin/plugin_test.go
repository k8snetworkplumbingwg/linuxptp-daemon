package plugin

import (
	"testing"

	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/assert"
	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// mockValidPlugins simulates the real mapping.PluginMapping for testing
// without creating import cycles
var mockValidPlugins = map[string]New{
	"e810":        nil,
	"e825":        nil,
	"e830":        nil,
	"reference":   nil,
	"ntpfailover": nil,
}

func TestValidateProfilePlugins(t *testing.T) {
	tests := []struct {
		name           string
		plugins        map[string]*apiextensions.JSON
		expectedErrors []string
	}{
		{
			name:           "nil plugins - should pass",
			plugins:        nil,
			expectedErrors: []string{},
		},
		{
			name: "valid plugin e810",
			plugins: map[string]*apiextensions.JSON{
				"e810": {},
			},
			expectedErrors: []string{},
		},
		{
			name: "valid plugin e825",
			plugins: map[string]*apiextensions.JSON{
				"e825": {},
			},
			expectedErrors: []string{},
		},
		{
			name: "valid plugin e830",
			plugins: map[string]*apiextensions.JSON{
				"e830": {},
			},
			expectedErrors: []string{},
		},
		{
			name: "valid plugin reference",
			plugins: map[string]*apiextensions.JSON{
				"reference": {},
			},
			expectedErrors: []string{},
		},
		{
			name: "valid plugin ntpfailover",
			plugins: map[string]*apiextensions.JSON{
				"ntpfailover": {},
			},
			expectedErrors: []string{},
		},
		{
			name: "typo e81 instead of e810",
			plugins: map[string]*apiextensions.JSON{
				"e81": {},
			},
			expectedErrors: []string{"e81"},
		},
		{
			name: "typo e82 instead of e825",
			plugins: map[string]*apiextensions.JSON{
				"e82": {},
			},
			expectedErrors: []string{"e82"},
		},
		{
			name: "typo E810 (case sensitivity)",
			plugins: map[string]*apiextensions.JSON{
				"E810": {},
			},
			expectedErrors: []string{"E810"},
		},
		{
			name: "multiple valid plugins",
			plugins: map[string]*apiextensions.JSON{
				"e810":      {},
				"reference": {},
			},
			expectedErrors: []string{},
		},
		{
			name: "mix of valid and invalid plugins",
			plugins: map[string]*apiextensions.JSON{
				"e810":    {},
				"e81":     {},
				"invalid": {},
			},
			expectedErrors: []string{"e81", "invalid"},
		},
		{
			name: "completely wrong plugin name",
			plugins: map[string]*apiextensions.JSON{
				"nvidia_mellanox": {},
			},
			expectedErrors: []string{"nvidia_mellanox"},
		},
		{
			name: "empty string plugin name",
			plugins: map[string]*apiextensions.JSON{
				"": {},
			},
			expectedErrors: []string{""},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			profileName := "test-profile"
			profile := &ptpv1.PtpProfile{
				Name:    &profileName,
				Plugins: tc.plugins,
			}

			unrecognized := ValidateProfilePlugins(profile, mockValidPlugins)

			if len(tc.expectedErrors) == 0 {
				assert.Empty(t, unrecognized, "Expected no unrecognized plugins")
			} else {
				assert.Equal(t, len(tc.expectedErrors), len(unrecognized),
					"Expected %d unrecognized plugins, got %d: %v",
					len(tc.expectedErrors), len(unrecognized), unrecognized)

				for _, expected := range tc.expectedErrors {
					assert.Contains(t, unrecognized, expected,
						"Expected unrecognized plugin '%s' not found in %v", expected, unrecognized)
				}
			}
		})
	}
}

func TestValidateProfilePlugins_NilProfile(t *testing.T) {
	unrecognized := ValidateProfilePlugins(nil, mockValidPlugins)
	assert.Empty(t, unrecognized, "Nil profile should return empty slice")
}

func TestValidateProfilePlugins_EmptyValidPlugins(t *testing.T) {
	profileName := "test-profile"
	profile := &ptpv1.PtpProfile{
		Name: &profileName,
		Plugins: map[string]*apiextensions.JSON{
			"e810": {},
		},
	}

	// With empty valid plugins map, all plugins should be unrecognized
	emptyValid := map[string]New{}
	unrecognized := ValidateProfilePlugins(profile, emptyValid)
	assert.Equal(t, 1, len(unrecognized), "All plugins should be unrecognized with empty valid map")
	assert.Contains(t, unrecognized, "e810")
}
