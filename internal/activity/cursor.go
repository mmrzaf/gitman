package activity

import (
	"encoding/base64"
	"encoding/json"
	"github.com/mmrzaf/gitman/internal/apperr"
	"time"
)

type feedCursor struct {
	At     time.Time
	Source Kind
	ID     string
}

func (e RepoEntry) Cursor() string {
	b, _ := json.Marshal(feedCursor{At: e.At, Source: e.Kind, ID: e.id})
	return base64.RawURLEncoding.EncodeToString(b)
}
func parseCursor(encoded string) (*feedCursor, error) {
	if encoded == "" {
		return nil, nil
	}
	invalid := apperr.New(apperr.KindInvalid, "The activity cursor is invalid. Open the newest activity page.")
	if len(encoded) > 512 {
		return nil, invalid
	}
	b, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, invalid
	}
	var c feedCursor
	if err := json.Unmarshal(b, &c); err != nil || c.At.IsZero() || c.ID == "" || len(c.ID) > 128 {
		return nil, invalid
	}
	switch c.Source {
	case KindPush, KindRun, KindDeployment, KindEvent, KindRefusal:
	default:
		return nil, invalid
	}
	return &c, nil
}
func cursorArgs(c *feedCursor) []any {
	if c == nil {
		return []any{nil, "", ""}
	}
	return []any{c.At, c.Source, c.ID}
}
