package api

import "testing"

// TestValidateConfigRoutes covers the project-level routing-table validator: unknown service
// targets, bad type, non-absolute path, duplicate (type,match) tuples, and multiple catch-alls.
func TestValidateConfigRoutes(t *testing.T) {
	svcs := `"services":[{"name":"web","image":"nginx"},{"name":"api","image":"node"}]`
	cases := []struct {
		name      string
		config    string
		wantError bool
	}{
		{
			name:      "no routes is always valid",
			config:    `{` + svcs + `}`,
			wantError: false,
		},
		{
			name:      "valid path + subdomain routes",
			config:    `{` + svcs + `,"routes":[{"service":"web","type":"path","match":"/"},{"service":"api","type":"path","match":"/api"},{"service":"api","type":"path","match":"/r"},{"service":"api","type":"subdomain","match":"admin"}]}`,
			wantError: false,
		},
		{
			name:      "unknown target service",
			config:    `{` + svcs + `,"routes":[{"service":"ghost","type":"path","match":"/x"}]}`,
			wantError: true,
		},
		{
			name:      "bad type",
			config:    `{` + svcs + `,"routes":[{"service":"api","type":"regex","match":"/x"}]}`,
			wantError: true,
		},
		{
			name:      "path without leading slash",
			config:    `{` + svcs + `,"routes":[{"service":"api","type":"path","match":"api"}]}`,
			wantError: true,
		},
		{
			name:      "duplicate path on two services",
			config:    `{` + svcs + `,"routes":[{"service":"web","type":"path","match":"/api"},{"service":"api","type":"path","match":"/api"}]}`,
			wantError: true,
		},
		{
			name:      "two catch-alls",
			config:    `{` + svcs + `,"routes":[{"service":"web","type":"path","match":"/"},{"service":"api","type":"path","match":""}]}`,
			wantError: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := validateConfigRoutes([]byte(tc.config))
			if (msg != "") != tc.wantError {
				t.Errorf("validateConfigRoutes() = %q, wantError=%v", msg, tc.wantError)
			}
		})
	}
}
