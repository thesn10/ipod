package extremote

import "github.com/oandrew/ipod"

// extNotifyState tracks whether the radio subscribed to PlayStatusChangeNotification
// events via SetPlayStatusChangeNotification or SetPlayStatusChangeNotificationShort.
// Unsolicited notifications must not be sent before the radio subscribes.
type extNotifyState struct {
	subscribed bool
}

func (s *extNotifyState) setSubscribed(enabled bool) { s.subscribed = enabled }

// SendPlayStatus sends a PlayStatusChangeNotification if the radio subscribed.
func (h *ExtRemoteHandler) SendPlayStatus(tr ipod.CommandWriter, playing bool) {
	if !h.subscribed {
		return
	}
	state := PlayerStatePaused
	if playing {
		state = PlayerStatePlaying
	}
	ipod.Send(tr, &PlayStatusChangeNotification{
		EventID:     0x00,
		PlayerState: byte(state),
	})
}

// SendTrackIndex sends a TrackIndexChanged PlayStatusChangeNotification if the radio subscribed.
func (h *ExtRemoteHandler) SendTrackIndex(tr ipod.CommandWriter, index uint32) {
	if !h.subscribed {
		return
	}
	ipod.Send(tr, &PlayStatusChangeNotificationTrackIndex{
		EventID:    0x01,
		TrackIndex: index,
	})
}

// SendTrackPositionMs sends a position PlayStatusChangeNotification if the radio subscribed.
func (h *ExtRemoteHandler) SendTrackPositionMs(tr ipod.CommandWriter, posMs uint32) {
	if !h.subscribed {
		return
	}
	ipod.Send(tr, &PlayStatusChangeNotificationPosition{
		EventID:    0x04,
		PositionMs: posMs,
	})
}
