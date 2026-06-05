package builder

import "fmt"

// promote ports scripts/promote.sh: retag the exact image built in the source
// environment to the destination environment (pull → tag → push, no rebuild),
// then deploy to the destination. --dry-run previews the docker commands.
func (o Options) promote() error {
	srcEnv := o.Env
	dstEnv := ""
	dryRun := false
	for _, arg := range o.Extra {
		switch arg {
		case "--dry-run":
			dryRun = true
		default:
			if dstEnv == "" {
				dstEnv = arg
			}
		}
	}
	if dstEnv == "" {
		return fmt.Errorf("usage: promote <src_env> <dst_env> [--dry-run]")
	}

	cfg, err := o.loadConfig()
	if err != nil {
		return err
	}
	if err := cfg.ValidateEnv(srcEnv); err != nil {
		return err
	}
	if err := cfg.ValidateEnv(dstEnv); err != nil {
		return err
	}
	if srcEnv == dstEnv {
		return fmt.Errorf("source and destination environments must differ")
	}

	o.info("Promoting '%s' → '%s' (v%s)", srcEnv, dstEnv, cfg.VersionString())
	if dryRun {
		o.info("Dry-run mode — no changes will be made")
	}

	retag := func(service string) error {
		srcTag := cfg.ImageTag(service, srcEnv)
		dstTag := cfg.ImageTag(service, dstEnv)
		o.info("Promoting %s: %s → %s", service, srcTag, dstTag)
		if dryRun {
			fmt.Fprintf(o.out(), "  [dry-run] docker pull %s\n", srcTag)
			fmt.Fprintf(o.out(), "  [dry-run] docker tag %s %s\n", srcTag, dstTag)
			fmt.Fprintf(o.out(), "  [dry-run] docker push %s\n", dstTag)
			return nil
		}
		if err := o.dockerRun("pull", srcTag); err != nil {
			return err
		}
		if err := o.dockerRun("tag", srcTag, dstTag); err != nil {
			return err
		}
		if err := o.dockerRun("push", dstTag); err != nil {
			return err
		}
		o.success("%s promoted → %s", service, dstTag)
		return nil
	}

	if err := retag("backend"); err != nil {
		return err
	}
	if cfg.Environments[dstEnv].FrontendEnabled {
		if err := retag("frontend"); err != nil {
			return err
		}
	} else {
		o.info("Frontend disabled for %q — skipping", dstEnv)
	}

	if dryRun {
		o.info("Dry-run complete — run without --dry-run to apply")
		return nil
	}

	o.info("Deploying to '%s'...", dstEnv)
	if err := o.runDeploy(dstEnv); err != nil {
		return err
	}
	o.success("Promotion complete: %s → %s (no rebuild)", srcEnv, dstEnv)
	return nil
}
