package api

import "testing"

// TestValidateConfigServicesWebEntry covers the web-entry host-collision guard
// and the env_file_mount absolute-path check in validateConfigServices.
func TestValidateConfigServicesWebEntry(t *testing.T) {
	cases := []struct {
		name      string
		config    string
		wantError bool
	}{
		{
			name: "two web entries on apex collide",
			config: `{"services":[
				{"name":"web","image":"nginx","web_routed":true},
				{"name":"app","image":"node","web_routed":true}
			]}`,
			wantError: true,
		},
		{
			name: "two web entries on distinct subdomains are fine",
			config: `{"services":[
				{"name":"web","image":"nginx","web_routed":true},
				{"name":"api","image":"node","web_routed":true,"subdomain":"api"}
			]}`,
			wantError: false,
		},
		{
			name: "same subdomain collides",
			config: `{"services":[
				{"name":"a","image":"nginx","web_routed":true,"subdomain":"app"},
				{"name":"b","image":"node","web_routed":true,"subdomain":"app"}
			]}`,
			wantError: true,
		},
		{
			name:      "single web entry is fine",
			config:    `{"services":[{"name":"web","image":"nginx","web_routed":true}]}`,
			wantError: false,
		},
		{
			name:      "relative env_file_mount path rejected",
			config:    `{"services":[{"name":"app","build":{},"env_file_mount":"var/www/.env"}]}`,
			wantError: true,
		},
		{
			name:      "absolute env_file_mount path accepted",
			config:    `{"services":[{"name":"app","build":{},"env_file_mount":"/var/www/html/.env"}]}`,
			wantError: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := validateConfigServices([]byte(tc.config))
			if (msg != "") != tc.wantError {
				t.Errorf("validateConfigServices = %q, wantError=%v", msg, tc.wantError)
			}
		})
	}
}
