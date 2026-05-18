package dispremote

import (
	"encoding/binary"

	"github.com/oandrew/ipod"
)

// Event mask bits for SetRemoteEventNotification (Table 3-49 of the iAP spec).
const (
	EventMaskTrackPositionMs  uint32 = 1 << 0
	EventMaskTrackIndex       uint32 = 1 << 1
	EventMaskChapterIndex     uint32 = 1 << 2
	EventMaskPlayStatus       uint32 = 1 << 3
	EventMaskVolume           uint32 = 1 << 4
	EventMaskPower            uint32 = 1 << 5
	EventMaskEqualizer        uint32 = 1 << 6
	EventMaskShuffle          uint32 = 1 << 7
	EventMaskRepeat           uint32 = 1 << 8
	EventMaskDateTime         uint32 = 1 << 9
	EventMaskBacklight        uint32 = 1 << 11
	EventMaskHoldSwitch       uint32 = 1 << 12
	EventMaskSoundCheck       uint32 = 1 << 13
	EventMaskAudiobookSpeed   uint32 = 1 << 14
	EventMaskTrackPositionSec uint32 = 1 << 15
	EventMaskVolume2          uint32 = 1 << 16
)

// Event numbers for RemoteEventNotification (Table 3-51 of the iAP spec).
const (
	EventNumTrackPositionMs  byte = 0x00
	EventNumTrackIndex       byte = 0x01
	EventNumChapterIndex     byte = 0x02
	EventNumPlayStatus       byte = 0x03
	EventNumVolume           byte = 0x04
	EventNumPower            byte = 0x05
	EventNumEqualizer        byte = 0x06
	EventNumShuffle          byte = 0x07
	EventNumRepeat           byte = 0x08
	EventNumDateTime         byte = 0x09
	EventNumBacklight        byte = 0x0B
	EventNumHoldSwitch       byte = 0x0C
	EventNumSoundCheck       byte = 0x0D
	EventNumAudiobookSpeed   byte = 0x0E
	EventNumTrackPositionSec byte = 0x0F
	EventNumVolume2          byte = 0x10
)

// dispNotifyState tracks which events the radio subscribed to via
// SetRemoteEventNotification and provides the send methods for each.
type dispNotifyState struct {
	eventMask uint32
}

func (s *dispNotifyState) setEventMask(mask uint32) { s.eventMask = mask }

func (s *dispNotifyState) isEnabled(mask uint32) bool { return s.eventMask&mask != 0 }

// EventMask returns the current remote event subscription bitmask.
func (h *DispRemoteHandler) EventMask() uint32 { return h.eventMask }

// SendPlayStatus sends a play-status RemoteEventNotification if the radio subscribed to it.
func (h *DispRemoteHandler) SendPlayStatus(tr ipod.CommandWriter, playing bool) {
	if !h.isEnabled(EventMaskPlayStatus) {
		return
	}
	status := PlayStatusPaused
	if playing {
		status = PlayStatusPlaying
	}
	ipod.Send(tr, &RemoteEventNotification{
		EventNum:  EventNumPlayStatus,
		EventData: []byte{byte(status)},
	})
}

// SendTrackIndex sends a track-index RemoteEventNotification if the radio subscribed to it.
func (h *DispRemoteHandler) SendTrackIndex(tr ipod.CommandWriter) {
	if !h.isEnabled(EventMaskTrackIndex) {
		return
	}
	data := make([]byte, 4)
	binary.BigEndian.PutUint32(data, h.trackIndex)
	ipod.Send(tr, &RemoteEventNotification{
		EventNum:  EventNumTrackIndex,
		EventData: data,
	})
}

// SendTrackPositionSec sends a track-position (in seconds) RemoteEventNotification
// if the radio subscribed to it. posMs is the current position in milliseconds.
func (h *DispRemoteHandler) SendTrackPositionSec(tr ipod.CommandWriter, posMs uint32) {
	if !h.isEnabled(EventMaskTrackPositionSec) {
		return
	}
	data := make([]byte, 2)
	binary.BigEndian.PutUint16(data, uint16(posMs/1000))
	ipod.Send(tr, &RemoteEventNotification{
		EventNum:  EventNumTrackPositionSec,
		EventData: data,
	})
}
