package audio

import (
	"bytes"
	"encoding/binary"

	"github.com/oandrew/ipod"
)

func init() {
	ipod.RegisterLingos(ipod.LingoDigitalAudioID, Lingos)
}

var Lingos struct {
	AccAck               `id:"0x00"`
	iPodAck              `id:"0x01"`
	GetAccSampleRateCaps `id:"0x02"`
	RetAccSampleRateCaps `id:"0x03"`
	NewiPodTrackInfo     `id:"0x04"`
	SetVideoDelay        `id:"0x05"`
}

type ACKStatus uint8

const (
	ACKStatusSuccess ACKStatus = 0x00
)

type AccAck struct {
	Status ACKStatus
	CmdID  uint8
}
type iPodAck struct {
	Status ACKStatus
	CmdID  uint8
}
type GetAccSampleRateCaps struct {
}
type RetAccSampleRateCaps struct {
	SampleRates []uint32
}

func (s *RetAccSampleRateCaps) UnmarshalBinary(data []byte) error {
	s.SampleRates = make([]uint32, len(data)/4)
	return binary.Read(bytes.NewReader(data), binary.BigEndian, s.SampleRates)
}

func (s *RetAccSampleRateCaps) MarshalBinary() ([]byte, error) {
	var buf bytes.Buffer
	err := binary.Write(&buf, binary.BigEndian, s.SampleRates)
	return buf.Bytes(), err
}

// NewiPodTrackInfo is sent by the accessory (us) to the car to open or
// re-open the USB audio stream. Per iAP spec it must be sent once when starting
// a new track, and re-sent only if the sample rate changes.
type NewiPodTrackInfo struct {
	// SampleRate is the PCM sample rate in Hz (e.g. 44100).
	SampleRate uint32
	// SoundCheck is the iTunesSound Check gain adjustment in dB, rounded to the
	// nearest integer and stored as a uint32. 0 means Sound Check is disabled /
	// no adjustment.
	SoundCheck uint32
	// VolumeAdjustment is the per-track volume adjustment (replay gain) in dB,
	// rounded to the nearest integer and stored as a uint32. 0 means no
	// adjustment.
	VolumeAdjustment uint32
}
type SetVideoDelay struct {
	Delay uint32
}
