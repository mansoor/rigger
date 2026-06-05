package notify

import (
	"fmt"

	apprise "github.com/unraid/apprise-go"
)

// EmbeddedApprise delivers notifications in-process using the apprise-go
// library — no Python, no sidecar container. This is the default backend; it
// speaks the same Apprise URL format as the sidecar, so channels are
// interchangeable. Selected when APPRISE_URL is unset.
type EmbeddedApprise struct{}

func NewEmbeddedApprise() *EmbeddedApprise { return &EmbeddedApprise{} }

// embeddedNotifyType maps our level to the apprise-go NotifyType.
func embeddedNotifyType(level string) apprise.NotifyType {
	switch level {
	case LevelSuccess:
		return apprise.NotifySuccess
	case LevelWarning:
		return apprise.NotifyWarning
	case LevelFailure:
		return apprise.NotifyFailure
	default:
		return apprise.NotifyInfo
	}
}

// Notify delivers the message to the given Apprise URLs via apprise-go.
func (e *EmbeddedApprise) Notify(urls []string, title, body, level string) error {
	if len(urls) == 0 {
		return fmt.Errorf("no apprise URLs provided")
	}
	return apprise.Send(urls, body,
		apprise.WithTitle(title),
		apprise.WithNotifyType(embeddedNotifyType(level)),
	)
}
