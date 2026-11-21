package state

import "errors"

var (
	// ErrUnreadable means the config file could not be read, parsed, or hashed.
	// It is not an empty config.
	ErrUnreadable = errors.New("config unreadable")
	// ErrUsageUnknown means the usage store could not be read. It is not zero.
	ErrUsageUnknown = errors.New("usage unknown")
	// ErrConversationUnknown means a conversation file could not be read or parsed. It is not an empty dialogue.
	ErrConversationUnknown = errors.New("conversation unknown")
	// ErrInUse means a delete would leave a dangling id.
	ErrInUse = errors.New("in_use")
	// ErrNotFound means the id is not in the current config.
	ErrNotFound = errors.New("not_found")
)
