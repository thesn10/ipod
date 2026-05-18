package extremote

import (
	"time"

	audio "github.com/oandrew/ipod/lingo-audio"

	"github.com/oandrew/ipod"
)

type DeviceExtRemote interface {
	// PlaybackStatus returns track duration (ms), current position (ms), and
	// whether the player is currently playing (vs paused/stopped).
	PlaybackStatus() (trackLength, trackPos uint32, playing bool)
	// IsPlaying returns the current playing state from the phone via AVRCP.
	IsPlaying() bool
	TrackTitle() string
	TrackArtist() string
	TrackAlbum() string
	// MediaControl sends a playback command to the phone via AVRCP.
	// method is a BlueZ MediaPlayer1 method name: "Play", "Pause",
	// "Next", "Previous", "FastForward", "Rewind".
	MediaControl(method string)
}

func ackSuccess(req *ipod.Command) *ACK {
	return &ACK{Status: ACKStatusSuccess, CmdID: req.ID.CmdID()}
}

// audioAttrDebounce is the minimum interval between consecutive
// NewiPodTrackInfo sends. The car's stream-reopen cycle takes ~500ms
// and causes it to send another PlayCurrentSelection; without debouncing this
// creates a tight feedback loop.
const audioAttrDebounce = 5 * time.Second

// ExtRemoteHandler manages session-scoped state for lingo 0x04 (Extended Remote).
// A new instance must be created for each USB session so that state resets
// correctly on reconnect.
type ExtRemoteHandler struct {
	// audioEstablished is true once NewiPodTrackInfo has been sent at
	// least once this session. Starts true so the IDPS send (from the audio
	// lingo) counts — suppressing spurious TrackIndex pushes from notifyCh
	// during start-up before the first PlayCurrentSelection.
	audioEstablished bool
	// lastAudioAttrSent is the time we last sent NewiPodTrackInfo.
	// Per spec, NewiPodTrackInfo must be sent on every PlayCurrentSelection
	// to (re)open the USB audio stream. However the car's stream-reopen cycle
	// itself triggers another PlayCurrentSelection, so we debounce resends
	// within audioAttrDebounce to break the feedback loop.
	// Zero value means "never sent" — PlayCurrentSelection will always fire it.
	lastAudioAttrSent time.Time
	// debounceAudioAttr suppresses duplicate NewiPodTrackInfo sends within
	// audioAttrDebounce. Enable with --audio-attr-debounce for head units that
	// re-send PlayCurrentSelection when the audio stream is reopened.
	debounceAudioAttr bool
	extNotifyState
}

// NewExtRemoteHandler returns a handler with audioEstablished=true.
// If debounce is true, duplicate NewiPodTrackInfo sends within audioAttrDebounce
// are suppressed (for head units that loop on PlayCurrentSelection).
func NewExtRemoteHandler(debounce bool) *ExtRemoteHandler {
	return &ExtRemoteHandler{audioEstablished: true, debounceAudioAttr: debounce}
}

// AudioEstablished reports whether the USB audio stream has been opened at
// least once this session.
func (h *ExtRemoteHandler) AudioEstablished() bool { return h.audioEstablished }

// OnTrackChanged resets the audio-attributes debounce so that the
// PlayCurrentSelection which follows a car-side TrackIndexChanged notification
// always sends TrackNewAudioAttributes to reopen the USB audio stream.
func (h *ExtRemoteHandler) OnTrackChanged() {
	h.lastAudioAttrSent = time.Time{}
}

// SendAudioOpen sends NewiPodTrackInfo to (re)open the USB audio stream.
// Used when the phone resumes playback without a PlayControl from the car
// (e.g. phone-side play, auto-advance after track end). Skipped if
// NewiPodTrackInfo was already sent within 2s (covers radio-side PlayControl
// which sends it directly in the handler).
func (h *ExtRemoteHandler) SendAudioOpen(tr ipod.CommandWriter) {
	if time.Since(h.lastAudioAttrSent) < 2*time.Second {
		return
	}
	h.lastAudioAttrSent = time.Now()
	h.audioEstablished = true
	ipod.Send(tr, &audio.NewiPodTrackInfo{SampleRate: audio.NegotiatedRate()})
}

// HandleExtRemote is kept for callers that don't need session state.
// Prefer ExtRemoteHandler.Handle for new code.
func HandleExtRemote(req *ipod.Command, tr ipod.CommandWriter, dev DeviceExtRemote) error {
	return (&ExtRemoteHandler{audioEstablished: true, debounceAudioAttr: false}).Handle(req, tr, dev)
}

func (h *ExtRemoteHandler) Handle(req *ipod.Command, tr ipod.CommandWriter, dev DeviceExtRemote) error {
	//log.Printf("Req: %#v", req)
	switch msg := req.Payload.(type) {

	case *GetCurrentPlayingTrackChapterInfo:
		ipod.Respond(req, tr, &ReturnCurrentPlayingTrackChapterInfo{
			CurrentChapterIndex: 0,
			ChapterCount:        1,
		})
	case *SetCurrentPlayingTrackChapter:
		ipod.Respond(req, tr, ackSuccess(req))
	case *GetCurrentPlayingTrackChapterPlayStatus:
		ipod.Respond(req, tr, &ReturnCurrentPlayingTrackChapterPlayStatus{
			ChapterPosition: 0,
			ChapterLength:   0,
		})
	case *GetCurrentPlayingTrackChapterName:
		ipod.Respond(req, tr, &ReturnCurrentPlayingTrackChapterName{
			ChapterName: ipod.StringToBytes("chapter"),
		})
	case *GetAudiobookSpeed:
		ipod.Respond(req, tr, &ReturnAudiobookSpeed{
			Speed: 0,
		})
	case *SetAudiobookSpeed:
		ipod.Respond(req, tr, ackSuccess(req))
	case *GetIndexedPlayingTrackInfo:
		var info interface{}
		switch msg.InfoType {
		case TrackInfoCaps:
			capLength := uint32(300_000)
			if dev != nil {
				cl, _, _ := dev.PlaybackStatus()
				if cl > 0 {
					capLength = cl
				}
			}
			info = &TrackCaps{
				Caps:         0x0,
				TrackLength:  capLength,
				ChapterCount: 1,
			}
		case TrackInfoDescription, TrackInfoLyrics:
			info = &TrackLongText{
				Flags:       0x0,
				PacketIndex: 0,
				Text:        0x00,
			}
		case TrackInfoArtworkCount:
			info = struct{}{}
		default:
			info = []byte{0x00}

		}
		ipod.Respond(req, tr, &ReturnIndexedPlayingTrackInfo{
			InfoType: msg.InfoType,
			Info:     info,
		})
	case *GetArtworkFormats:
		ipod.Respond(req, tr, &RetArtworkFormats{})
	case *GetTrackArtworkData:
		ipod.Respond(req, tr, &ACK{
			Status: ACKStatusFailed,
			CmdID:  req.ID.CmdID(),
		})
	case *ResetDBSelection:
		// The car sends ResetDBSelection during DB browsing (e.g. after a
		// TrackIndex notification). We just ack it. Do NOT reset
		// audioEstablished here — doing so caused a spurious TrackIndex push
		// from the next PlayControl, which triggered another 8-deep
		// ResetDBSelection storm with no PlayCurrentSelection ever following.
		ipod.Respond(req, tr, ackSuccess(req))
	case *SelectDBRecord:
		ipod.Respond(req, tr, ackSuccess(req))
	case *GetNumberCategorizedDBRecords:
		// Per rockbox iap-lingo4.c and libiap: returning 0 for Playlist and
		// Track causes some head units (e.g. Alpine) to hang or loop forever.
		// Return a non-zero dummy count for Playlist and Track; 0 for
		// categories we don't support (Genre, Composer, AudioBook, Podcast).
		var count int32
		switch msg.CategoryType {
		case DbCategoryPlaylist:
			count = 1 // at least one playlist (the current queue)
		case DbCategoryTrack, DbCategoryArtist, DbCategoryAlbum:
			count = 1 // at least one track playing
		default:
			count = 0
		}
		ipod.Respond(req, tr, &ReturnNumberCategorizedDBRecords{
			RecordCount: count,
		})
	case *RetrieveCategorizedDatabaseRecords:
		// The car fetches the record(s) it found via GetNumberCategorizedDBRecords.
		// We only have one virtual entry per category; return it regardless of
		// the requested Offset/Count.
		var name [16]byte
		copy(name[:], "Bluetooth")
		ipod.Respond(req, tr, &ReturnCategorizedDatabaseRecord{
			RecordCategoryIndex: 0,
			String:              name,
		})
	case *GetPlayStatus:
		length, pos, playing := uint32(300_000), uint32(0), false
		if dev != nil {
			length, pos, playing = dev.PlaybackStatus()
		}
		if length == 0 {
			length = pos + 300_000
		}
		state := PlayerStatePaused
		if playing {
			state = PlayerStatePlaying
		}
		ipod.Respond(req, tr, &ReturnPlayStatus{
			TrackLength:   length,
			TrackPosition: pos,
			State:         state,
		})
	case *GetCurrentPlayingTrackIndex:
		ipod.Respond(req, tr, &ReturnCurrentPlayingTrackIndex{
			TrackIndex: 0,
		})
	case *GetIndexedPlayingTrackTitle:
		title := "Bluetooth"
		if dev != nil {
			title = dev.TrackTitle()
		}
		ipod.Respond(req, tr, &ReturnIndexedPlayingTrackTitle{
			Title: ipod.StringToBytes(ipod.TruncateRunes(title, 20)),
		})
	case *GetIndexedPlayingTrackArtistName:
		artist := ""
		if dev != nil {
			artist = dev.TrackArtist()
		}
		ipod.Respond(req, tr, &ReturnIndexedPlayingTrackArtistName{
			ArtistName: ipod.StringToBytes(ipod.TruncateRunes(artist, 20)),
		})
	case *GetIndexedPlayingTrackAlbumName:
		album := ""
		if dev != nil {
			album = dev.TrackAlbum()
		}
		ipod.Respond(req, tr, &ReturnIndexedPlayingTrackAlbumName{
			AlbumName: ipod.StringToBytes(ipod.TruncateRunes(album, 20)),
		})
	case *SetPlayStatusChangeNotification:
		h.setNotifyMask(msg.EventMask)
		ipod.Respond(req, tr, ackSuccess(req))
		// Report the actual current play state. Reporting always-Paused here
		// caused cars that periodically re-send SetPlayStatusChangeNotification
		// (session renewal) to issue a spurious PlayControl(Toggle) every time,
		// because they saw "Paused" and tried to start playback again.
		h.SendPlayStatus(tr, dev != nil && dev.IsPlaying())
	case *SetPlayStatusChangeNotificationShort:
		// One-byte form: 0x01 enables basic types 0x00–0x05; 0x00 disables all.
		if msg.Enabled {
			h.setNotifyMask(notifyMaskOneByteEnable)
		} else {
			h.setNotifyMask(0)
		}
		ipod.Respond(req, tr, ackSuccess(req))
		h.SendPlayStatus(tr, dev != nil && dev.IsPlaying())
	case *PlayCurrentSelection:
		if dev != nil {
			dev.MediaControl("Play")
		}
		ipod.Respond(req, tr, ackSuccess(req))
		if !h.debounceAudioAttr || time.Since(h.lastAudioAttrSent) >= audioAttrDebounce {
			h.lastAudioAttrSent = time.Now()
			h.audioEstablished = true
			ipod.Send(tr, &audio.NewiPodTrackInfo{SampleRate: audio.NegotiatedRate()})
		}
	case *PlayControl:
		// currentlyPlaying is read before issuing any command so Toggle can
		// determine the correct direction.
		currentlyPlaying := dev != nil && dev.IsPlaying()
		var avrcpCmd string
		sendAudio := false
		switch msg.Cmd {
		case PlayControlToggle:
			if currentlyPlaying {
				avrcpCmd = "Pause"
			} else {
				avrcpCmd = "Play"
				sendAudio = true
			}
		case PlayControlPlay:
			avrcpCmd = "Play"
			sendAudio = true
		case PlayControlPause:
			avrcpCmd = "Pause"
		case PlayControlStop:
			avrcpCmd = "Pause"
		case PlayControlNextTrack, PlayControlNext, PlayControlNextChapter:
			avrcpCmd = "Next"
			h.lastAudioAttrSent = time.Time{}
		case PlayControlPrevTrack, PlayControlPrev, PlayControlPrevChapter:
			avrcpCmd = "Previous"
			h.lastAudioAttrSent = time.Time{}
		case PlayControlStartFF:
			avrcpCmd = "FastForward"
		case PlayControlStartRew:
			avrcpCmd = "Rewind"
		case PlayControlEndFFRew:
			// FastForward/Rewind run until another method is called (BlueZ docs).
			avrcpCmd = "Play"
		}
		if avrcpCmd != "" && dev != nil {
			dev.MediaControl(avrcpCmd)
		}
		ipod.Respond(req, tr, ackSuccess(req))
		if sendAudio {
			h.lastAudioAttrSent = time.Now()
			ipod.Send(tr, &audio.NewiPodTrackInfo{SampleRate: audio.NegotiatedRate()})
		}
		// Play state notifications are sent via MediaControl → signalPlayStateChanged
		// → notifyCh → main.go, which covers ExtRemote and DispRemote uniformly.
	case *GetTrackArtworkTimes:
		ipod.Respond(req, tr, &RetTrackArtworkTimes{})
	case *GetShuffle:
		ipod.Respond(req, tr, &ReturnShuffle{Mode: ShuffleOff})
	case *SetShuffle:
		ipod.Respond(req, tr, ackSuccess(req))

	case *GetRepeat:
		ipod.Respond(req, tr, &ReturnRepeat{Mode: RepeatOff})
	case *SetRepeat:
		ipod.Respond(req, tr, ackSuccess(req))

	case *SetDisplayImage:
		ipod.Respond(req, tr, ackSuccess(req))
	case *GetMonoDisplayImageLimits:
		ipod.Respond(req, tr, &ReturnMonoDisplayImageLimits{
			MaxWidth:    640,
			MaxHeight:   960,
			PixelFormat: 0x01,
		})
	case *GetNumPlayingTracks:
		ipod.Respond(req, tr, &ReturnNumPlayingTracks{
			NumTracks: 1,
		})
	case *SetCurrentPlayingTrack:
		ipod.Respond(req, tr, ackSuccess(req))
	case *SelectSortDBRecord:
		ipod.Respond(req, tr, ackSuccess(req))
	case *GetColorDisplayImageLimits:
		ipod.Respond(req, tr, &ReturnColorDisplayImageLimits{
			MaxWidth:    640,
			MaxHeight:   960,
			PixelFormat: 0x01,
		})
	case *ResetDBSelectionHierarchy:
		ipod.Respond(req, tr, &ACK{Status: ACKStatusFailed, CmdID: req.ID.CmdID()})

	case *GetDBiTunesInfo:
	// RetDBiTunesInfo:
	case *GetUIDTrackInfo:
	// RetUIDTrackInfo:
	case *GetDBTrackInfo:
	// RetDBTrackInfo:
	case *GetPBTrackInfo:
	// RetPBTrackInfo:

	default:
		_ = msg
	}
	return nil
}
