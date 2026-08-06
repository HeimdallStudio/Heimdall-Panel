package websocket

import "testing"

func TestPresenceMessagesAreNeverThrottled(t *testing.T) {
	hub := NewHub()
	if hub.shouldThrottle(MessageTypePresence) {
		t.Fatal("first presence message was throttled")
	}
	if hub.shouldThrottle(MessageTypePresence) {
		t.Fatal("second immediate presence message was throttled")
	}
}
