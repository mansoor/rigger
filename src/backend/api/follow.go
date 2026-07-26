package api

// isFollowCommand reports whether a bridge command streams until the client goes
// away. `logs` runs `docker … -f`, which never exits on its own, so it has to be
// bound to the client connection — otherwise closing the log view leaves the
// docker process (and the compose plugin under it) running for the lifetime of
// the container. That was worth roughly one leaked process per log view.
//
// Deliberately NOT every command. Binding a deploy, build or backup to the
// request would SIGKILL it half-way through the moment the user navigated away
// or their laptop slept, which is far worse than a process outliving the page.
// Those are meant to run to completion regardless of who is watching.
func isFollowCommand(cmd string) bool {
	return cmd == "logs"
}
