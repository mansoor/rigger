package composegen

import (
	"strings"
	"testing"
)

func TestWebEntryFallbackSingleImage(t *testing.T) {
	cfg := []byte(`{
	  "project":{"name":"nginx","resource_prefix":"mcl_ngi","type":"image"},
	  "services":[{"name":"nginx","image":"nginx","tag":"latest","env_file":true}],
	  "environments":{"dev":{"deployment":"compose","traefik_enabled":true,"traefik_network":"traefik_net","domain":""}}
	}`)
	out, err := GenerateRouted(cfg, "dev", RouteOpts{AutoURLMode: "nip", AutoURLHost: "10.10.10.111"})
	if err != nil { t.Fatal(err) }
	s := string(out)
	for _, want := range []string{"traefik.enable=true", "mcl-ngi-dev.10.10.10.111.nip.io", "loadbalancer.server.port=80", "traefik_net"} {
		if !strings.Contains(s, want) { t.Errorf("missing %q in:\n%s", want, s) }
	}
}
