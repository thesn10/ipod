package dispremote

import (
	"errors"
	"time"

	"github.com/oandrew/ipod"
)

type DeviceDispRemote interface {
	// TrackPositionMs returns the current track position in milliseconds.
	TrackPositionMs() uint32
	// TrackLengthMs returns the track/stream duration in milliseconds.
	TrackLengthMs() uint32
	// IsPlaying returns the current playing state from the phone via AVRCP.
	IsPlaying() bool
	// TrackTitle returns the current track title.
	TrackTitle() string
	// TrackArtist returns the current track artist.
	TrackArtist() string
	// TrackAlbum returns the current track album.
	TrackAlbum() string
}

func ackSuccess(req *ipod.Command) *ACK {
	return &ACK{Status: ACKStatusSuccess, CmdID: uint8(req.ID.CmdID())}
}

func HandleDispRemote(req *ipod.Command, tr ipod.CommandWriter, dev DeviceDispRemote) error {
	switch msg := req.Payload.(type) {

	case *GetCurrentEQProfileIndex:
		ipod.Respond(req, tr, &RetCurrentEQProfileIndex{
			CurrentEQIndex: 0,
		})

	case *SetCurrentEQProfileIndex:
		ipod.Respond(req, tr, ackSuccess(req))

	case *GetNumEQProfiles:
		ipod.Respond(req, tr, &RetNumEQProfiles{
			NumEQProfiles: 1,
		})
	case *GetIndexedEQProfileName:
		ipod.Respond(req, tr, &RetIndexedEQProfileName{
			EQProfileName: ipod.StringToBytes("Default"),
		})
	case *SetRemoteEventNotification:
		ipod.Respond(req, tr, ackSuccess(req))

	case *GetRemoteEventStatus:
		ipod.Respond(req, tr, &RetRemoteEventStatus{
			EventStatus: 0,
		})

	case *GetiPodStateInfo:
		t := &RetiPodStateInfo{
			InfoType: msg.InfoType,
		}

		switch msg.InfoType {
		case InfoTypeTrackPositionMs:
			var pos uint32
			if dev != nil {
				pos = dev.TrackPositionMs()
			}
			t.InfoData = &InfoTrackPositionMs{TrackPositionMs: pos}
		case InfoTypeTrackIndex:
			t.InfoData = &InfoTrackIndex{TrackIndex: 0}
		case InfoTypeChapterIndex:
			t.InfoData = &InfoChapterIndex{
				TrackIndex:   0,
				ChapterCount: 0,
				ChapterIndex: 0,
			}
		case InfoTypePlayStatus:
			status := PlayStatusPlaying
			if dev != nil && !dev.IsPlaying() {
				status = PlayStatusPaused
			}
			t.InfoData = &InfoPlayStatus{PlayStatus: status}
		case InfoTypeVolume:
			t.InfoData = &InfoVolume{MuteState: 0x00, UIVolumeLevel: 255}
		case InfoTypePower:
			t.InfoData = &InfoPower{PowerState: 0x05, BatteryLevel: 255}
		case InfoTypeEqualizer:
			t.InfoData = &InfoEqualizer{0x00}
		case InfoTypeShuffle:
			t.InfoData = &InfoShuffle{0x00}
		case InfoTypeRepeat:
			t.InfoData = &InfoRepeat{0x00}
		case InfoTypeDateTime:
			d := time.Now()
			t.InfoData = &InfoDateTime{
				Year:   uint16(d.Year()),
				Month:  uint8(d.Month()),
				Day:    uint8(d.Day()),
				Hour:   uint8(d.Hour()),
				Minute: uint8(d.Minute()),
			}
		case InfoTypeBacklight:
			t.InfoData = &InfoBacklight{BacklightLevel: 255}
		case InfoTypeHoldSwitch:
			t.InfoData = &InfoHoldSwitch{HoldSwitchState: 0x00}
		case InfoTypeSoundCheck:
			t.InfoData = &InfoSoundCheck{SoundCheckState: 0x00}
		case InfoTypeAudiobookSpeed:
			t.InfoData = &InfoAudiobookSpeed{0x00}
		case InfoTypeTrackPositionSec:
			var posSec uint16
			if dev != nil {
				posSec = uint16(dev.TrackPositionMs() / 1000)
			}
			t.InfoData = &InfoTrackPositionSec{posSec}
		case InfoTypeVolume2:
			t.InfoData = &InfoVolume2{
				MuteState:           0x00,
				UIVolumeLevel:       255,
				AbsoluteVolumeLevel: 255,
			}
		default:
			return errors.New("unknown info type")
		}

		ipod.Respond(req, tr, t)

	case *SetiPodStateInfo:
		ipod.Respond(req, tr, ackSuccess(req))

	case *GetPlayStatus:
		pos := uint32(0)
		totalMs := uint32(300_000)
		if dev != nil {
			pos = dev.TrackPositionMs()
			l := dev.TrackLengthMs()
			if l > 0 {
				totalMs = l
			} else {
				totalMs = pos + 300_000
			}
		}
		state := PlayStatusPlaying
		if dev != nil && !dev.IsPlaying() {
			state = PlayStatusPaused
		}
		ipod.Respond(req, tr, &RetPlayStatus{
			PlayState:   byte(state),
			TrackIndex:  0,
			TrackLength: totalMs,
			TrackPos:    pos,
		})

	case *SetCurrentPlayingTrack:
		ipod.Respond(req, tr, ackSuccess(req))
	case *GetIndexedPlayingTrackInfo:
		t := &RetIndexedPlayingTrackInfo{
			InfoType: msg.InfoType,
		}

		switch msg.InfoType {
		case TrackInfoTypeCaps:
			totalMs := uint32(300_000)
			if dev != nil {
				l := dev.TrackLengthMs()
				if l > 0 {
					totalMs = l
				}
			}
			t.InfoData = &TrackInfoCaps{
				Caps:         0x00,
				TrackTotalMs: totalMs,
				ChapterCount: 1,
			}
		case TrackInfoTypeChapterTimeName:
			t.InfoData = &TrackInfoChapterTimeName{
				ChapterTime: 0,
				ChapterName: ipod.StringToBytes(""),
			}
		case TrackInfoTypeArtist:
			artist := ""
			if dev != nil {
				artist = dev.TrackArtist()
			}
			t.InfoData = &TrackInfoArtist{
				Name: ipod.StringToBytes(ipod.TruncateRunes(artist, 20)),
			}
		case TrackInfoTypeAlbum:
			album := ""
			if dev != nil {
				album = dev.TrackAlbum()
			}
			t.InfoData = &TrackInfoAlbum{
				Name: ipod.StringToBytes(ipod.TruncateRunes(album, 20)),
			}
		case TrackInfoTypeGenre:
			t.InfoData = &TrackInfoGenre{
				Name: ipod.StringToBytes(""),
			}
		case TrackInfoTypeTrack:
			title := "Bluetooth"
			if dev != nil {
				title = dev.TrackTitle()
			}
			t.InfoData = &TrackInfoTrack{
				Title: ipod.StringToBytes(ipod.TruncateRunes(title, 20)),
			}
		case TrackInfoTypeComposer:
			t.InfoData = &TrackInfoComposer{
				Name: ipod.StringToBytes(""),
			}
		case TrackInfoTypeLyrics:
			t.InfoData = &TrackInfoLyrics{
				Flags:       0x00,
				PacketIndex: 0,
				Lyrics:      ipod.StringToBytes(""),
			}
		case TrackInfoTypeArtworkCount:
			t.InfoData = &TrackInfoArtworkCount{
				None: 0x08,
			}
		default:
			return errors.New("unknown info type")
		}

		ipod.Respond(req, tr, t)
	case *GetNumPlayingTracks:
		ipod.Respond(req, tr, &RetNumPlayingTracks{
			NumPlayTracks: 1,
		})
	case *GetArtworkFormats:
		ipod.Respond(req, tr, &RetArtworkFormats{})
	case *GetTrackArtworkData:
	// RetTrackArtworkData:
	//todo
	case *GetPowerBatteryState:
		ipod.Respond(req, tr, &RetPowerBatteryState{
			BatteryLevel: 255, // 100%
			PowerState:   0x01,
		})
	case *GetSoundCheckState:
		ipod.Respond(req, tr, &RetSoundCheckState{
			Enabled: false,
		})
	case *SetSoundCheckState:
		ipod.Respond(req, tr, ackSuccess(req))
	case *GetTrackArtworkTimes:
		ipod.Respond(req, tr, &RetTrackArtworkTimes{})

	default:
		_ = msg
	}
	return nil
}
