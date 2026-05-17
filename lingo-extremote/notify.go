package extremote

import "github.com/oandrew/ipod"

// Notification event mask bits for the four-byte form of
// SetPlayStatusChangeNotification (Table 4-55 of the iAP spec).
const (
	NotifyBasicPlayState    uint32 = 1 << 0 // status 0x00/0x02/0x03: stop, FFW/REW seek stop
	NotifyExtendedPlayState uint32 = 1 << 1 // status 0x06: play/pause via PlayControl state codes
	NotifyTrackIndex        uint32 = 1 << 2 // status 0x01: track index changed
	NotifyTrackOffsetMs     uint32 = 1 << 3 // status 0x04: track time position in ms
	NotifyTrackOffsetSec    uint32 = 1 << 4 // status 0x07: track time position in seconds
	NotifyChapterIndex      uint32 = 1 << 5 // status 0x05: chapter index changed
	// bits 6–10 (chapter time offsets, UID, media type, lyrics ready) not implemented
)

// notifyMaskOneByteEnable is the mask set when the one-byte form of
// SetPlayStatusChangeNotification is received with value 0x01.
// Per spec it enables status types 0x00–0x05 (bits 0, 2, 3, 5).
// Extended play state (bit 1, type 0x06) is NOT part of the one-byte form.
const notifyMaskOneByteEnable uint32 = NotifyBasicPlayState | NotifyTrackIndex |
	NotifyTrackOffsetMs | NotifyChapterIndex

// extNotifyState tracks which notification event types the radio subscribed to
// via SetPlayStatusChangeNotification. Zero means all notifications disabled.
type extNotifyState struct {
	notifyMask uint32
}

func (s *extNotifyState) setNotifyMask(mask uint32) { s.notifyMask = mask }
func (s *extNotifyState) isEnabled(mask uint32) bool { return s.notifyMask&mask != 0 }

// SendPlayStatus sends a PlayStatusChangeNotificationExtended (type 0x06) if
// the radio subscribed to extended play state changes (bit 1).
// Extended play state uses PlayControl state codes: 0x0A=Playing, 0x0B=Paused.
func (h *ExtRemoteHandler) SendPlayStatus(tr ipod.CommandWriter, playing bool) {
	if !h.isEnabled(NotifyExtendedPlayState) {
		return
	}
	state := byte(0x0B) // Paused
	if playing {
		state = 0x0A // Playing
	}
	ipod.Send(tr, &PlayStatusChangeNotificationExtended{
		EventID: 0x06,
		State:   state,
	})
}

// SendTrackIndex sends a track-index PlayStatusChangeNotification (type 0x01)
// if the radio subscribed to track index changes (bit 2).
func (h *ExtRemoteHandler) SendTrackIndex(tr ipod.CommandWriter, index uint32) {
	if !h.isEnabled(NotifyTrackIndex) {
		return
	}
	ipod.Send(tr, &PlayStatusChangeNotificationTrackIndex{
		EventID:    0x01,
		TrackIndex: index,
	})
}

// SendTrackPositionMs sends a position PlayStatusChangeNotification (type 0x04)
// if the radio subscribed to track time offset in ms (bit 3).
func (h *ExtRemoteHandler) SendTrackPositionMs(tr ipod.CommandWriter, posMs uint32) {
	if !h.isEnabled(NotifyTrackOffsetMs) {
		return
	}
	ipod.Send(tr, &PlayStatusChangeNotificationPosition{
		EventID:    0x04,
		PositionMs: posMs,
	})
}
