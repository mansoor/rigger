package composegen

import (
	"strings"
	"testing"
)

const chownCfg = `{
	"project": {"name":"sftp","resource_prefix":"mcl_sftp","version":{"major":1,"minor":0,"patch":0,"build":0}},
	"services": [{"name":"app","image":"drakkan/sftpgo","tag":"latest","web_routed":true,"port":8080,
		"volumes":["./volumes/sftpgo_data:/var/lib/sftpgo","./volumes/sftpgo_home:/srv/sftpgo","db_named:/data","./conf/app.conf:/etc/app.conf"]}],
	"environments": {"dev": {"deployment":"compose","http_port":8080}}
}`

func TestChownInitSynthesized(t *testing.T) {
	out, err := GenerateRouted([]byte(chownCfg), "dev", RouteOpts{ChownUIDs: map[string]string{"app": "1000:1000"}})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		"app-init-perms:",
		"image: busybox:stable",
		// dir binds are chowned; the named volume + the .conf file bind are NOT.
		"chown -R 1000:1000 /var/lib/sftpgo /srv/sftpgo",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "/etc/app.conf") && strings.Contains(s, "chown -R 1000:1000") &&
		strings.Contains(s, "chown -R 1000:1000 /var/lib/sftpgo /srv/sftpgo /etc/app.conf") {
		t.Error("file bind /etc/app.conf must not be chowned")
	}
	// The app must be gated on the init one-shot.
	if !strings.Contains(s, "app-init-perms:\n        condition: service_completed_successfully") &&
		!strings.Contains(s, "service_completed_successfully") {
		t.Error("app should depend_on app-init-perms with service_completed_successfully")
	}
}

func TestNoChownInitWithoutUIDs(t *testing.T) {
	out, _ := GenerateRouted([]byte(chownCfg), "dev", RouteOpts{})
	if strings.Contains(string(out), "init-perms") {
		t.Error("no init-perms service should be emitted when ChownUIDs is empty")
	}
}

func TestNoChownInitOnSwarm(t *testing.T) {
	cfg := strings.Replace(chownCfg, `"deployment":"compose"`, `"deployment":"swarm"`, 1)
	out, _ := GenerateRouted([]byte(cfg), "dev", RouteOpts{ChownUIDs: map[string]string{"app": "1000:1000"}})
	if strings.Contains(string(out), "init-perms") {
		t.Error("swarm ignores depends_on conditions — no init-perms should be synthesized")
	}
}
