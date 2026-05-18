// Package avrcp reads playback state from the BlueZ org.bluez.MediaPlayer1
// D-Bus interface (AVRCP) by shelling out to busctl. This avoids external
// Go D-Bus library dependencies.
package avrcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// PlayState is the complete snapshot of AVRCP playback state.
type PlayState struct {
	Title    string
	Artist   string
	Album    string
	Duration uint32 // milliseconds; 0 if unknown
	Position uint32 // milliseconds
	Playing  bool
}

// Source polls BlueZ via busctl for AVRCP MediaPlayer1 state.
type Source struct {
	mu               sync.RWMutex
	state            PlayState
	positionTime     time.Time     // wall-clock time when state.Position was last set from BlueZ
	posRefreshedAt   time.Time     // wall-clock time when position was last refreshed while playing
	deviceName       string        // friendly name of the most recently connected BT A2DP device
	lastTrackLog     string        // dedup track log lines
	trackChanged     uint32        // atomic: 1 when new track data arrived, cleared by TrackChanged()
	playStateChanged uint32        // atomic: 1 when Playing transitions, cleared by PlayStateChanged()
	lastKnownTitle  string    // detect title changes to avoid spurious notifications
	lastKnownPlaying bool     // detect play/pause transitions
	// optimisticUntil suppresses BlueZ poll results that contradict a recent
	// optimistic Play/Pause update. BlueZ lags behind the busctl command by
	// up to ~500ms; without this guard the poll loop emits a spurious bounce
	// notification (e.g. Paused→Playing→Paused) which confuses the car radio.
	optimisticUntil time.Time
	notifyCh        chan struct{} // signalled (non-blocking) on track change or play state change
}

// Notify returns a channel that receives a value whenever track metadata
// changes. Consumers should drain it promptly; sends are non-blocking so
// a slow consumer only misses duplicates, never stalls the source.
func (s *Source) Notify() <-chan struct{} { return s.notifyCh }

// TrackChanged returns true (and resets the flag) if new track metadata has
// arrived since the last call. Used by the iAP handler to notify the car.
func (s *Source) TrackChanged() bool {
	return atomic.SwapUint32(&s.trackChanged, 0) != 0
}

// signalTrackChanged sets the trackChanged flag and does a non-blocking send
// on the notify channel so processFrames can push a notification immediately.
func (s *Source) signalTrackChanged() {
	atomic.StoreUint32(&s.trackChanged, 1)
	select {
	case s.notifyCh <- struct{}{}:
	default:
	}
}

// PlayStateChanged returns true (and resets the flag) if the Playing state
// has changed since the last call. Used to push PlayStatusChangeNotification
// to the car so it can start/stop its built-in playback timer.
func (s *Source) PlayStateChanged() (changed bool, playing bool) {
	if atomic.SwapUint32(&s.playStateChanged, 0) == 0 {
		return false, false
	}
	s.mu.RLock()
	p := s.state.Playing
	s.mu.RUnlock()
	return true, p
}

// signalPlayStateChanged sets the playStateChanged flag and wakes the notify
// channel so processFrames can immediately push the new state to the car.
func (s *Source) signalPlayStateChanged() {
	atomic.StoreUint32(&s.playStateChanged, 1)
	select {
	case s.notifyCh <- struct{}{}:
	default:
	}
}

func newPlayState() PlayState {
	return PlayState{Duration: 300_000}
}

// NewSource starts a background AVRCP poller and signal watcher, returning immediately.
// It never returns an error — polling failures are handled gracefully.
func NewSource() (*Source, error) {
	s := &Source{
		state:    newPlayState(),
		notifyCh: make(chan struct{}, 1),
	}
	go s.loop()
	go s.watchSignals()
	return s, nil
}

// PlaybackStatus implements extremote.DeviceExtRemote.
// Returns (trackLength ms, trackPosition ms, playing).
// The position is dead-reckoned forward from the last BlueZ report so that
// the car's timer advances smoothly between BlueZ's ~1Hz position updates.
func (s *Source) PlaybackStatus() (trackLength, trackPos uint32, playing bool) {
	s.mu.RLock()
	st := s.state
	refreshedAt := s.posRefreshedAt
	s.mu.RUnlock()

	dur := st.Duration
	if dur == 0 {
		dur = 300_000 // 5-minute fallback when duration unknown
	}
	pos := st.Position
	if st.Playing && !refreshedAt.IsZero() {
		if elapsed := uint32(time.Since(refreshedAt).Milliseconds()); elapsed < dur {
			pos += elapsed
			if pos > dur {
				pos = dur
			}
		}
	}
	return dur, pos, st.Playing
}

// TrackTitle implements extremote.DeviceExtRemote.
func (s *Source) TrackTitle() string {
	t := s.snapshot().Title
	if t == "" {
		return "Bluetooth"
	}
	return t
}

// TrackArtist implements extremote.DeviceExtRemote.
func (s *Source) TrackArtist() string { return s.snapshot().Artist }

// TrackAlbum implements extremote.DeviceExtRemote.
func (s *Source) TrackAlbum() string { return s.snapshot().Album }

// IsPlaying implements extremote.DeviceExtRemote and dispremote.DeviceDispRemote.
func (s *Source) IsPlaying() bool { return s.snapshot().Playing }

// TrackPositionMs implements dispremote.DeviceDispRemote.
func (s *Source) TrackPositionMs() uint32 {
	_, pos, _ := s.PlaybackStatus()
	return pos
}

// TrackLengthMs implements dispremote.DeviceDispRemote.
func (s *Source) TrackLengthMs() uint32 {
	st := s.snapshot()
	dur := st.Duration
	if dur == 0 {
		dur = 300_000
	}
	return dur
}

// MediaControl calls a BlueZ MediaPlayer1 method on the phone to control
// playback via AVRCP. method is one of: Play, Pause, Stop, Next, Previous,
// FastForward, Rewind, Release.
//
// Play/Pause/Stop update the internal state immediately (optimistic update) so
// that IsPlaying() is accurate before the next AVRCP poll cycle (~500ms).
// lastKnownPlaying is also updated to suppress the spurious PlayStateChanged
// signal that would otherwise fire when the poll confirms the new state.
func (s *Source) MediaControl(method string) {
	s.mu.Lock()
	switch method {
	case "Play":
		s.state.Playing = true
		s.lastKnownPlaying = true
		s.optimisticUntil = time.Now().Add(1500 * time.Millisecond)
	case "Pause", "Stop":
		s.state.Playing = false
		s.lastKnownPlaying = false
		s.posRefreshedAt = time.Time{}
		s.optimisticUntil = time.Now().Add(1500 * time.Millisecond)
	}
	s.mu.Unlock()

	switch method {
	case "Play", "Pause", "Stop":
		// Signal immediately so the car gets a play-state notification without
		// waiting for the next AVRCP poll cycle (~500ms). The lastKnownPlaying
		// suppression in refresh() prevents a duplicate when the poll confirms.
		s.signalPlayStateChanged()
	}

	path := findPlayerPath()
	if path == "" {
		return
	}
	exec.Command("busctl", "--system", "call",
		"org.bluez", path,
		"org.bluez.MediaPlayer1", method).Run()
}

// ConnectedDeviceName returns the cached friendly name of the most recently
// connected Bluetooth A2DP device (e.g. "Alex's iPhone"), or "" if none.
func (s *Source) ConnectedDeviceName() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.deviceName
}

// queryDeviceName derives the BlueZ Device1 path from a MediaPlayer1 path
// and retrieves the device's friendly Name property.
// playerPath looks like /org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF/player0.
func queryDeviceName(playerPath string) string {
	idx := strings.LastIndex(playerPath, "/player")
	if idx < 0 {
		return ""
	}
	devicePath := playerPath[:idx]
	out, err := exec.Command("busctl", "--system", "--json=short",
		"get-property", "org.bluez", devicePath,
		"org.bluez.Device1", "Name").Output()
	if err != nil || len(out) == 0 {
		return ""
	}
	var v busctlVariant
	if json.Unmarshal(bytes.TrimSpace(out), &v) != nil {
		return ""
	}
	var name string
	if json.Unmarshal(v.Data, &name) != nil {
		return ""
	}
	return name
}

func (s *Source) snapshot() PlayState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

// snapshotWithPosition returns the current state and an interpolated position.
// When playing, it adds time elapsed since the last BlueZ position update so
// the car sees a continuously advancing position rather than a frozen one.
func (s *Source) snapshotWithPosition() (PlayState, uint32) {
	s.mu.RLock()
	st := s.state
	pt := s.positionTime
	s.mu.RUnlock()

	pos := st.Position
	if st.Playing {
		elapsed := uint32(time.Since(pt).Milliseconds())
		pos += elapsed
		// Don't overshoot the track duration.
		if st.Duration > 0 && pos > st.Duration {
			pos = st.Duration
		}
	}
	return st, pos
}

func (s *Source) loop() {
	for {
		s.refresh()
		time.Sleep(500 * time.Millisecond)
	}
}

// watchSignals runs dbus-monitor and triggers an immediate refresh whenever
// the BlueZ MediaPlayer1 properties change (e.g. track skip, play/pause).
// When Track appears in the invalidated list, it also retries Get("Track")
// directly since GetAll never includes it (emits-invalidates annotation).
func (s *Source) watchSignals() {
	for {
		cmd := exec.Command("dbus-monitor", "--system",
			"type=signal,sender=org.bluez,interface=org.freedesktop.DBus.Properties,member=PropertiesChanged")
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			time.Sleep(5 * time.Second)
			continue
		}
		if err := cmd.Start(); err != nil {
			time.Sleep(5 * time.Second)
			continue
		}
		sc := bufio.NewScanner(stdout)
		isPlayer := false
		for sc.Scan() {
			line := sc.Text()
			if strings.Contains(line, "/player") {
				isPlayer = true
				go s.refresh()
			} else if isPlayer && strings.Contains(line, `"Track"`) {
				// Track appeared in invalidated array — BlueZ dropped its cache.
				// Retry Get("Track") so BlueZ re-fetches from the phone.
				go s.fetchTrackWithRetry(5, 300*time.Millisecond)
				isPlayer = false
			} else if strings.HasPrefix(strings.TrimSpace(line), "signal ") {
				// start of a new signal — reset context
				isPlayer = false
			}
		}
		cmd.Wait()
		time.Sleep(time.Second)
	}
}

// fetchTrackWithRetry calls Get("Track") on MediaPlayer1 and retries up to
// maxTries times with the given interval between attempts. BlueZ may need a
// moment to issue GetElementAttributes to the phone after an invalidation.
func (s *Source) fetchTrackWithRetry(maxTries int, interval time.Duration) {
	path := findPlayerPath()
	if path == "" {
		return
	}
	for i := 0; i < maxTries; i++ {
		if i > 0 {
			time.Sleep(interval)
		}
		if s.fetchTrack(path) {
			return
		}
	}
	s.logOnce("[AVRCP] Track still unavailable after retries (phone not sending metadata)")
}

// fetchTrack calls Get("Track") directly via busctl. Returns true if track
// data was successfully parsed and cached.
func (s *Source) fetchTrack(path string) bool {
	out, err := exec.Command("busctl", "--system", "--json=short",
		"call", "org.bluez", path,
		"org.freedesktop.DBus.Properties", "Get",
		"ss", "org.bluez.MediaPlayer1", "Track").Output()
	if err != nil || len(out) == 0 {
		return false
	}
	// busctl Get returns: {"type":"v","data":{"type":"a{sv}","data":{"Title":{...},...}}}
	var wrapper struct {
		Type string `json:"type"`
		Data struct {
			Type string                   `json:"type"`
			Data map[string]busctlVariant `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &wrapper); err != nil {
		return false
	}
	track := wrapper.Data.Data
	if len(track) == 0 {
		return false
	}
	s.mu.Lock()
	next := s.state
	s.mu.Unlock()
	got := false
	if t, ok := track["Title"]; ok {
		var title string
		if json.Unmarshal(t.Data, &title) == nil && title != "" {
			next.Title = title
			got = true
		}
	}
	if a, ok := track["Artist"]; ok {
		var artist string
		if json.Unmarshal(a.Data, &artist) == nil {
			next.Artist = artist
			got = true
		}
	}
	if a, ok := track["Album"]; ok {
		var album string
		if json.Unmarshal(a.Data, &album) == nil {
			next.Album = album
			got = true
		}
	}
	if d, ok := track["Duration"]; ok {
		var dur uint32
		if json.Unmarshal(d.Data, &dur) == nil && dur > 0 {
			next.Duration = dur
			got = true
		}
	}
	if !got {
		return false
	}
	msg := fmt.Sprintf("[AVRCP] track (Get): title=%q artist=%q album=%q duration=%d",
		next.Title, next.Artist, next.Album, next.Duration)
	s.logOnce(msg)
	s.mu.Lock()
	changed := next.Title != s.lastKnownTitle
	if changed {
		s.lastKnownTitle = next.Title
	}
	s.state = next
	s.mu.Unlock()
	if changed {
		s.signalTrackChanged()
	}
	return true
}

// findPlayerPath returns the D-Bus object path of the first BlueZ
// MediaPlayer1 object by parsing `busctl tree org.bluez`.
func findPlayerPath() string {
	out, err := exec.Command("busctl", "--system", "tree", "org.bluez").Output()
	if err != nil {
		return ""
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		// Match leaf player objects like /org/bluez/hci0/dev_.../player0
		if strings.Contains(line, "/player") && !strings.Contains(line, "/player/") &&
			!strings.Contains(line, "NowPlaying") && !strings.Contains(line, "Filesystem") {
			// strip tree-drawing characters
			path := strings.TrimLeft(line, "├─└│ ")
			if strings.HasPrefix(path, "/org/bluez") {
				return path
			}
		}
	}
	return ""
}

// busctlVariant holds the minimal structure returned by busctl --json=short.
type busctlVariant struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// getAllProperties calls GetAll on org.bluez.MediaPlayer1 via busctl and returns
// the property map. Returns nil on any error.
func getAllProperties(path string) map[string]busctlVariant {
	out, err := exec.Command("busctl", "--system", "--json=short",
		"call", "org.bluez", path,
		"org.freedesktop.DBus.Properties", "GetAll",
		"s", "org.bluez.MediaPlayer1").Output()
	if err != nil || len(out) == 0 {
		return nil
	}
	// busctl wraps the reply: {"type":"a{sv}","data":[{...}]}
	var wrapper struct {
		Data []map[string]busctlVariant `json:"data"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &wrapper); err != nil || len(wrapper.Data) == 0 {
		return nil
	}
	return wrapper.Data[0]
}

// getString extracts a string value from a busctlVariant map.
func getString(props map[string]busctlVariant, key string) (string, bool) {
	v, ok := props[key]
	if !ok {
		return "", false
	}
	var s string
	if json.Unmarshal(v.Data, &s) != nil {
		return "", false
	}
	return s, true
}

// getUint32 extracts a uint32 value from a busctlVariant map.
func getUint32(props map[string]busctlVariant, key string) (uint32, bool) {
	v, ok := props[key]
	if !ok {
		return 0, false
	}
	var n uint32
	if json.Unmarshal(v.Data, &n) != nil {
		return 0, false
	}
	return n, true
}

func (s *Source) logOnce(msg string) {
	s.mu.Lock()
	if s.lastTrackLog != msg {
		s.lastTrackLog = msg
		s.mu.Unlock()
		fmt.Fprintln(os.Stderr, msg)
	} else {
		s.mu.Unlock()
	}
}

func (s *Source) refresh() {
	path := findPlayerPath()
	if path == "" {
		return
	}

	// Update cached device name whenever we have a valid player path.
	if name := queryDeviceName(path); name != "" {
		s.mu.Lock()
		s.deviceName = name
		s.mu.Unlock()
	}

	props := getAllProperties(path)
	if props == nil {
		return
	}

	// Start from the existing state so we never lose previously-cached
	// metadata (Title/Artist/Album) on a poll cycle where Track is absent.
	s.mu.RLock()
	next := s.state
	s.mu.RUnlock()

	gotAny := false
	gotPos := false

	if pos, ok := getUint32(props, "Position"); ok {
		next.Position = pos
		gotAny = true
		gotPos = true
	}

	if status, ok := getString(props, "Status"); ok {
		next.Playing = status == "playing"
		gotAny = true
	}

	// Track is an a{sv} nested inside the property map.
	// busctl renders it as {"type":"a{sv}","data":{"Title":{...},...}}.
	if trackProp, ok := props["Track"]; ok {
		var track map[string]busctlVariant
		if json.Unmarshal(trackProp.Data, &track) == nil && len(track) > 0 {
			if t, ok := track["Title"]; ok {
				var title string
				if json.Unmarshal(t.Data, &title) == nil && title != "" {
					next.Title = title
					gotAny = true
				}
			}
			if a, ok := track["Artist"]; ok {
				var artist string
				if json.Unmarshal(a.Data, &artist) == nil {
					next.Artist = artist
					gotAny = true
				}
			}
			if a, ok := track["Album"]; ok {
				var album string
				if json.Unmarshal(a.Data, &album) == nil {
					next.Album = album
					gotAny = true
				}
			}
			if d, ok := track["Duration"]; ok {
				var dur uint32
				if json.Unmarshal(d.Data, &dur) == nil && dur > 0 {
					next.Duration = dur
					gotAny = true
				}
			}
			msg := fmt.Sprintf("[AVRCP] track (GetAll): title=%q artist=%q album=%q duration=%d",
				next.Title, next.Artist, next.Album, next.Duration)
			s.logOnce(msg)
			// Detect title change and signal the car to refresh its display.
			s.mu.RLock()
			titleChanged := next.Title != s.lastKnownTitle
			s.mu.RUnlock()
			if titleChanged {
				s.mu.Lock()
				s.lastKnownTitle = next.Title
				s.mu.Unlock()
				s.signalTrackChanged()
			}
		} else {
			s.logOnce("[AVRCP] Track property present but empty (phone not sending metadata)")
		}
	}

	// If Track wasn't in GetAll (emits-invalidates means BlueZ won't include
	// it unless it has a fresh value), try a direct Get("Track") as fallback.
	// fetchTrack writes directly to s.state; re-read it afterwards so we
	// don't overwrite the title/artist/album it just set when we write back.
	if _, hasTrack := props["Track"]; !hasTrack {
		s.fetchTrack(path)
		// Merge: take whatever fetchTrack stored, then patch in position/playing.
		var playingChanged bool
		var newPlaying bool
		s.mu.Lock()
		if pos, ok := getUint32(props, "Position"); ok {
			s.state.Position = pos
			if s.state.Playing {
				s.posRefreshedAt = time.Now()
			}
		}
		if status, ok := getString(props, "Status"); ok {
			newPlaying = status == "playing"
			if newPlaying != s.lastKnownPlaying {
				if time.Now().Before(s.optimisticUntil) {
					newPlaying = s.lastKnownPlaying // suppress bounce within guard window
				} else {
					playingChanged = true
					s.lastKnownPlaying = newPlaying
				}
			}
			if !newPlaying {
				s.posRefreshedAt = time.Time{}
			}
			s.state.Playing = newPlaying
		}
		s.mu.Unlock()
		if playingChanged {
			s.signalPlayStateChanged()
		}
		return
	}

	if !gotAny {
		return
	}
	s.mu.Lock()
	prevPlaying := s.lastKnownPlaying
	s.state = next
	if next.Playing != prevPlaying && time.Now().Before(s.optimisticUntil) {
		s.state.Playing = prevPlaying // suppress bounce within guard window
	}
	playingChanged := s.state.Playing != prevPlaying
	if playingChanged {
		s.lastKnownPlaying = s.state.Playing
	}
	if gotPos && s.state.Playing {
		s.posRefreshedAt = time.Now()
	} else if !s.state.Playing {
		// Reset baseline on pause so we don't extrapolate stale position on resume.
		s.posRefreshedAt = time.Time{}
	}
	s.mu.Unlock()
	if playingChanged {
		s.signalPlayStateChanged()
	}
}
