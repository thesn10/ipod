package audio

import (
	"github.com/oandrew/ipod"
)

type DeviceAudio interface {
	// SetSampleRate is called once the best mutually-supported sample rate has
	// been selected so the audio stack can reconfigure itself accordingly.
	SetSampleRate(rate uint32)
	// SupportedSampleRates returns the sample rates the local audio backend
	// (e.g. BlueALSA) is currently able to deliver.  An empty or nil slice
	// means "no constraint" and all rates advertised by the car are eligible.
	SupportedSampleRates() []uint32
}

// func ackSuccess(req ipod.Packet) ACK {
// 	return ACK{Status: ACKStatusSuccess, CmdID: uint8(req.ID.CmdID())}
// }

// func ackPending(req ipod.Packet, maxWait uint32) ACKPending {
// 	return ACKPending{Status: ACKStatusPending, CmdID: uint8(req.ID.CmdID()), MaxWait: maxWait}
// }

// negotiatedRate is the sample rate agreed with the car during the audio
// handshake.  It is re-sent with each TrackNewAudioAttributes so the car
// reopens its audio stream after every PlayCurrentSelection.
var negotiatedRate uint32 = 44100

// NegotiatedRate returns the sample rate most recently agreed with the car
// via the audio handshake (RetAccSampleRateCaps → TrackNewAudioAttributes).
// Falls back to 44100 before any handshake has run.  Used by other lingos
// (e.g. ExtRemote) to send TrackNewAudioAttributes with the correct rate.
func NegotiatedRate() uint32 { return negotiatedRate }

func Start(tr ipod.CommandWriter) {
	ipod.Send(tr, &GetAccSampleRateCaps{})
}

// ReopenAudio re-sends TrackNewAudioAttributes using the last negotiated rate.
// Call this after PlayCurrentSelection so the car reopens its audio interface.
func ReopenAudio(tr ipod.CommandWriter) {
	ipod.Send(tr, &NewiPodTrackInfo{
		SampleRate: negotiatedRate,
	})
}

func HandleAudio(req *ipod.Command, tr ipod.CommandWriter, dev DeviceAudio) error {
	switch msg := req.Payload.(type) {
	case *AccAck:
		// Car acknowledges our sample rate caps request
		// No additional action needed

	case *RetAccSampleRateCaps:
		ipod.Respond(req, tr, &AccAck{
			Status: ACKStatusSuccess,
			CmdID:  0x03, // RetAccSampleRateCaps command ID
		})
		// Inform the car which rate we will stream at.
		ipod.Send(tr, &NewiPodTrackInfo{
			SampleRate: 44100,
		})

	case *NewiPodTrackInfo:
		// Car sends audio attributes and is ready for audio
		// Acknowledge with AccAck
		ipod.Respond(req, tr, &AccAck{
			Status: ACKStatusSuccess,
			CmdID:  0x04, // TrackNewAudioAttributes command ID
		})

	case *SetVideoDelay:
		// Car sets video delay offset
		// Acknowledge with AccAck
		ipod.Respond(req, tr, &AccAck{
			Status: ACKStatusSuccess,
			CmdID:  0x05, // SetVideoDelay command ID
		})

	default:
		_ = msg
	}
	return nil
}
