package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os/exec"
	"strings"
	"sync"
	"time"

	"os"

	"github.com/davecgh/go-spew/spew"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli"

	"github.com/oandrew/ipod"
	"github.com/oandrew/ipod/avrcp"
	"github.com/oandrew/ipod/hid"
	audio "github.com/oandrew/ipod/lingo-audio"
	dispremote "github.com/oandrew/ipod/lingo-dispremote"
	extremote "github.com/oandrew/ipod/lingo-extremote"
	general "github.com/oandrew/ipod/lingo-general"
	_ "github.com/oandrew/ipod/lingo-simpleremote"
	"github.com/oandrew/ipod/trace"
)

var log = logrus.StandardLogger()

func openDevice(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR, os.ModePerm)
	if err != nil {
		return nil, err
	}
	stat, _ := f.Stat()
	if stat.Mode()&os.ModeCharDevice != os.ModeCharDevice {
		return nil, fmt.Errorf("not a char device")
	}
	return f, nil
}

func openTraceFile(path string) (*os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func newTraceFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR|os.O_CREATE, os.ModePerm)
}

type UsageError struct {
	error
}

var hidReportDefs = hid.DefaultReportDefs

func main() {
	logOut := os.Stdout
	log.Formatter = &TextFormatter{
		Colored: checkIfTerminal(logOut),
	}

	log.Out = logOut

	spew.Config.DisablePointerAddresses = true

	app := cli.NewApp()
	app.Name = "ipod"
	app.Authors = []cli.Author{
		cli.Author{
			Name: "Andrew Onyshchuk",
		},
	}
	app.Usage = "ipod accessory protocol client"
	app.HideVersion = true

	app.ErrWriter = os.Stderr
	app.Flags = []cli.Flag{
		cli.BoolFlag{
			Name:  "debug, d",
			Usage: "verbose logging",
		},
		cli.BoolFlag{
			Name:  "legacy, l",
			Usage: "use legacy hid descriptor",
		},
	}

	app.ExitErrHandler = func(c *cli.Context, err error) {
		if err != nil {
			if _, ok := err.(UsageError); ok {
				fmt.Fprintf(c.App.ErrWriter, "usage error: %v\n\n", err)
				cli.ShowCommandHelp(c, c.Command.Name)
			} else {
				fmt.Fprintf(c.App.ErrWriter, "error: %v\n\n", err)
			}
			os.Exit(1)
		}
	}

	app.Before = func(c *cli.Context) error {
		if c.GlobalBool("debug") {
			log.SetLevel(logrus.DebugLevel)
		}

		if c.GlobalBool("legacy") {
			hidReportDefs = hid.LegacyReportDefs
		}

		return nil
	}

	app.Commands = []cli.Command{
		{
			Name:  "lingos",
			Usage: "print all lingos/commands/ids",
			Action: func(c *cli.Context) error {
				fmt.Println("Registered lingos:")
				fmt.Println(ipod.DumpLingos())
				return nil
			},
		},
		{
			Name:      "serve",
			Aliases:   []string{"s"},
			ArgsUsage: "<dev>",
			Usage:     "respond to requests from a char device i.e. /dev/iap0",
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:  "write-trace, w",
					Usage: "Write trace to a `file`",
				},
				cli.StringFlag{
					Name:  "sample-rate-cmd",
					Value: "",
					Usage: "Shell command to run when the negotiated sample rate changes (rate passed as $IPOD_SAMPLE_RATE)",
				},
			},
			Action: func(c *cli.Context) error {
				initAVRCP()
				devAudio.sampleRateCmd = c.String("sample-rate-cmd")

				path := c.Args().First()
				if path == "" {
					return UsageError{fmt.Errorf("device path is missing")}
				}
				f, err := openDevice(path)
				le := log.WithField("path", path)
				if err != nil {
					le.WithError(err).Errorf("could not open the device")
					return err
				}
				le.Info("device opened")

				var rw io.ReadWriter = f
				if tracePath := c.String("write-trace"); tracePath != "" {
					traceFile, err := newTraceFile(tracePath)
					le := log.WithField("path", tracePath)
					if err != nil {
						le.WithError(err).Errorf("could not create a trace file")
						return err
					}
					le.Warningf("writing trace")
					rw = trace.NewTracer(traceFile, f)
				}

				reportR, reportW := hid.NewReportReader(rw), hid.NewReportWriter(rw)
				frameTransport := hid.NewTransport(reportR, reportW, hidReportDefs)
				processFrames(frameTransport)
				return nil
			},
		},
		{
			Name:    "replay",
			Aliases: []string{"r"},
			Usage:   "respond to requests from a trace file",
			Action: func(c *cli.Context) error {
				path := c.Args().First()
				if path == "" {
					return UsageError{cli.NewExitError("trace file path is missing", 1)}
				}

				f, err := openTraceFile(path)
				le := log.WithField("path", path)
				if err != nil {
					le.WithError(err).Errorf("could not open the trace file")
					return err
				}
				le.Warningf("trace file opened")

				tr := trace.NewReader(f)
				tdr := trace.NewTraceDirReader(tr, trace.DirIn)
				reportR, reportW := hid.NewReportReader(tdr), hid.NewReportWriter(ioutil.Discard)
				frameTransport := hid.NewTransport(reportR, reportW, hidReportDefs)
				processFrames(frameTransport)
				return nil
			},
		},
		{
			Name:    "view",
			Aliases: []string{"v"},
			Usage:   "view a trace file",
			Action: func(c *cli.Context) error {
				path := c.Args().First()
				if path == "" {
					return UsageError{cli.NewExitError("trace file path is missing", 1)}
				}

				f, err := openTraceFile(path)
				le := log.WithField("path", path)
				if err != nil {
					le.WithError(err).Errorf("could not open the trace file")
					return err
				}
				le.Warningf("trace file opened")
				tr := trace.NewReader(f)
				dumpTrace(tr)
				return nil
			},
		},
		{
			Name: "send",
			Flags: []cli.Flag{
				cli.StringFlag{
					Name:  "write-trace, w",
					Usage: "Write trace to a `file`",
				},
			},
			Usage: "acc mode / send accessory requests from a trace file",
			Action: func(c *cli.Context) error {
				path := c.Args().Get(0)
				if path == "" {
					return UsageError{fmt.Errorf("device path is missing")}
				}
				f, err := openDevice(path)
				le := log.WithField("path", path)
				if err != nil {
					le.WithError(err).Errorf("could not open the device")
					return err
				}
				le.Info("device opened")

				tpath := c.Args().Get(1)
				if tpath == "" {
					return UsageError{cli.NewExitError("trace file path is missing", 1)}
				}

				tf, err := openTraceFile(tpath)
				tle := log.WithField("path", tpath)
				if err != nil {
					tle.WithError(err).Errorf("could not open the trace file")
					return err
				}
				tle.Warningf("trace file opened")
				tr := trace.NewReader(tf)
				tdr := trace.NewTraceDirReader(tr, trace.DirIn)

				var rw io.ReadWriter = f
				if tracePath := c.String("write-trace"); tracePath != "" {
					traceFile, err := newTraceFile(tracePath)
					le := log.WithField("path", tracePath)
					if err != nil {
						le.WithError(err).Errorf("could not create a trace file")
						return err
					}
					le.Warningf("writing trace")
					rw = trace.NewTracer(traceFile, f)
				}
				reportR, reportW := hid.NewReportReader(rw), hid.NewReportWriter(rw)
				dummyW := hid.NewReportWriter(ioutil.Discard)
				traceR := hid.NewReportReader(tdr)

				frameTransport := hid.NewTransport(reportR, dummyW, hidReportDefs)

				go processFrames(frameTransport)

				for {
					report, err := traceR.ReadReport()
					if err != nil {
						break
					}

					reportW.WriteReport(report)
					log.Infof("writing report\n%s", spew.Sdump(report))

					time.Sleep(1000 * time.Millisecond)
				}

				select {}

				return nil
			},
		},
	}

	app.Setup()
	startBTAliasRefresher()
	app.Run(os.Args)

}

func logFrame(frame []byte, err error, msg string) {
	le := FrameLogEntry(logrus.NewEntry(log), frame)
	if err != nil {
		le.WithError(err).Errorf(msg)
		return
	}
	le.Infof(msg)
	if log.Level == logrus.DebugLevel {
		spew.Fdump(log.Out, frame)
	}

}

func logPacket(pkt []byte, err error, msg string) {
	//le := PacketLogEntry(logrus.NewEntry(log), frame)
	le := log.WithField("len", len(pkt))
	if err != nil {
		le.WithError(err).Errorf(msg)
		return
	}
	le.Infof(msg)
	if log.Level == logrus.DebugLevel {
		spew.Fdump(log.Out, pkt)
	}
}

func logCmd(cmd *ipod.Command, err error, msg string) {
	le := CommandLogEntry(logrus.NewEntry(log), cmd)
	if err != nil {
		le.WithError(err).Errorf(msg)
		return
	}
	le.Infof(msg)
	if log.Level == logrus.DebugLevel {
		spew.Fdump(log.Out, cmd)
	}

}

func processFrames(frameTransport ipod.FrameReadWriter) {
	// Reset session-scoped state so reconnections start fresh.
	extRemoteHandler = extremote.NewExtRemoteHandler()

	serde := ipod.CommandSerde{}

	type frameResult struct {
		data []byte
		err  error
	}
	frameCh := make(chan frameResult, 1)

	// writeMu guards concurrent writes to frameTransport: the main loop
	// writes responses to car packets while the notify goroutine may also
	// write unsolicited track-change frames at any time.
	var writeMu sync.Mutex

	sendCmds := func(outCmdBuf *ipod.CmdBuffer) {
		for i := range outCmdBuf.Commands {
			outCmd := outCmdBuf.Commands[i]
			logCmd(outCmd, nil, ">> CMD")
			outPacket, err := serde.MarshalCmd(outCmd)
			logPacket(outPacket, err, ">> PACKET")
			if err != nil {
				continue
			}
			packetWriter := ipod.NewPacketWriter()
			packetWriter.WritePacket(outPacket)
			outFrame := packetWriter.Bytes()
			writeMu.Lock()
			outFrameErr := frameTransport.WriteFrame(outFrame)
			writeMu.Unlock()
			logFrame(outFrame, outFrameErr, ">> FRAME")
		}
	}

	startRead := func() {
		go func() {
			f, err := frameTransport.ReadFrame()
			frameCh <- frameResult{f, err}
		}()
	}

	// iAP1 handshake: the iPod must send RequestIdentify first to prompt the
	// accessory (car radio) to send its Identify (Cmd 0x01). Without this the
	// radio sits silent and the session never starts.
	log.Info("sending RequestIdentify")
	{
		initBuf := ipod.CmdBuffer{}
		ipod.Send(&initBuf, &general.RequestIdentify{})
		sendCmds(&initBuf)
	}

	startRead()

	// Subscribe to AVRCP track-change notifications so we can push them
	// to the car immediately without waiting for the next car poll.
	var notifyCh <-chan struct{}
	if avrcpSource != nil {
		notifyCh = avrcpSource.Notify()
	}

	// 500ms ticker to push PlayStatusChangeNotification{EventID:0x04, posMs}
	// via lingo 0x04 (ExtendedInterface). Both rockbox and libiap send this
	// unconditionally every 500ms in extended mode — it is what drives the
	// car's on-screen playback timer.
	positionTicker := time.NewTicker(500 * time.Millisecond)
	defer positionTicker.Stop()

	for {
		select {
		case <-positionTicker.C:
			if avrcpSource != nil && extRemoteHandler.IsPlaying() {
				_, posMs, _ := avrcpSource.PlaybackStatus()
				outCmdBuf := ipod.CmdBuffer{}
				ipod.Send(&outCmdBuf, &extremote.PlayStatusChangeNotificationPosition{
					EventID:    0x04,
					PositionMs: posMs,
				})
				sendCmds(&outCmdBuf)
			}

		case <-notifyCh:
			// Phone changed track — push TrackIndexChanged so the car refreshes
			// its display with the new title/artist/album. The car will respond
			// with PlayCurrentSelection, which reopens the USB audio stream via
			// TrackNewAudioAttributes. Reset the audio debounce so that
			// PlayCurrentSelection always fires TrackNewAudioAttributes even if
			// one was sent recently for the previous track.
			if avrcpSource.TrackChanged() {
				extRemoteHandler.OnTrackChanged()
				outCmdBuf := ipod.CmdBuffer{}
				ipod.Send(&outCmdBuf, &extremote.PlayStatusChangeNotificationTrackIndex{
					EventID:    0x01,
					TrackIndex: 0,
				})
				sendCmds(&outCmdBuf)
			}
			// Drain the play-state-changed flag but don't push it — the timer
			// is driven by position ticks (EventID=0x04 every 500ms), and
			// spurious Paused→Playing transitions from BlueZ glitches cause
			// malformed PlaybackStopped frames that break the audio session.
			if avrcpSource != nil {
				avrcpSource.PlayStateChanged() // consume flag, no push
			}

		case fr := <-frameCh:
			if fr.err == io.EOF {
				log.Warnf("EOF")
				return
			}
			logFrame(fr.data, fr.err, "<< FRAME")
			startRead() // queue next read immediately
			if fr.err != nil {
				continue
			}

			packetReader := ipod.NewPacketReader(fr.data)
			inCmdBuf := ipod.CmdBuffer{}
			for {
				inPacket, err := packetReader.ReadPacket()
				if err == io.EOF {
					break
				}
				logPacket(inPacket, err, "<< PACKET")
				if err != nil {
					continue
				}
				inCmd, err := serde.UnmarshalCmd(inPacket)
				logCmd(inCmd, err, "<< CMD")
				inCmdBuf.WriteCommand(inCmd)
			}

			outCmdBuf := ipod.CmdBuffer{}
			devGeneral.cmdWriter = &outCmdBuf
			for i := range inCmdBuf.Commands {
				handlePacket(&outCmdBuf, inCmdBuf.Commands[i])
			}
			sendCmds(&outCmdBuf)
		}
	}
}

var devGeneral = &DevGeneral{}

// audioDevice implements audio.DeviceAudio.  SetSampleRate runs a
// configurable shell command so the ALSA gadget is reopened at the
// negotiated rate (e.g. restarting bluealsa-aplay).
type audioDevice struct {
	sampleRateCmd string
	lastRate      uint32
}

func (a *audioDevice) SetSampleRate(rate uint32) {
	if rate == a.lastRate {
		return
	}
	a.lastRate = rate
	if a.sampleRateCmd == "" {
		return
	}
	log.WithField("rate", rate).Info("[Audio] sample rate changed, running rate cmd")
	go func() {
		// Small delay so the TrackNewAudioAttributes ACK reaches the car first.
		time.Sleep(200 * time.Millisecond)
		cmd := exec.Command("sh", "-c", a.sampleRateCmd)
		cmd.Env = append(cmd.Environ(), fmt.Sprintf("IPOD_SAMPLE_RATE=%d", rate))
		if out, err := cmd.CombinedOutput(); err != nil {
			log.WithError(err).WithField("output", string(out)).Warn("[Audio] sample-rate-cmd failed")
		}
	}()
}

// SupportedSampleRates queries BlueALSA via D-Bus for the sampling frequency
// of every A2DP source PCM (phone → accessory).  The set of rates is returned
// so the audio lingo handler can intersect it with what the car advertises and
// pick the single highest mutually-supported rate.
// Returns nil when BlueALSA is unavailable; the handler treats nil as
// "no constraint" and falls back to picking the highest car-advertised rate.
func (a *audioDevice) SupportedSampleRates() []uint32 {
	// Enumerate all BlueALSA objects and collect paths that contain a2dpsrc.
	out, err := exec.Command("busctl", "--system", "tree", "org.bluealsa").Output()
	if err != nil || len(out) == 0 {
		return nil
	}

	var paths []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimLeft(sc.Text(), "├─└│ ")
		if strings.HasPrefix(line, "/org/bluealsa") && strings.Contains(line, "a2dpsnk") {
			paths = append(paths, line)
		}
	}

	type busctlVariant struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}

	rateSet := make(map[uint32]bool)
	for _, path := range paths {
		propOut, err := exec.Command("busctl", "--system", "--json=short",
			"get-property", "org.bluealsa", path,
			"org.bluealsa.PCM1", "Rate").Output()
		if err != nil || len(propOut) == 0 {
			continue
		}
		var v busctlVariant
		if err := json.Unmarshal(bytes.TrimSpace(propOut), &v); err != nil {
			continue
		}
		var freq uint32
		if err := json.Unmarshal(v.Data, &freq); err != nil {
			continue
		}
		if freq > 0 {
			rateSet[freq] = true
		}
	}

	if len(rateSet) == 0 {
		return nil
	}
	rates := make([]uint32, 0, len(rateSet))
	for r := range rateSet {
		rates = append(rates, r)
	}
	return rates
}

var devAudio = &audioDevice{}
var avrcpSource *avrcp.Source

// extRemoteHandler is reset for every new USB session in processFrames so that
// the playing-state flag starts as false (paused) on each reconnect.
var extRemoteHandler = extremote.NewExtRemoteHandler()

func initAVRCP() {
	src, err := avrcp.NewSource()
	if err != nil {
		log.WithError(err).Warn("[AVRCP] Could not connect to D-Bus, playback metadata will be static")
		return
	}
	log.Info("[AVRCP] D-Bus source started")
	avrcpSource = src
	devGeneral.BtSource = src
}

func handlePacket(cmdWriter ipod.CommandWriter, cmd *ipod.Command) {
	switch cmd.ID.LingoID() {
	case ipod.LingoGeneralID:
		general.HandleGeneral(cmd, cmdWriter, devGeneral)

	case ipod.LingoSimpleRemoteID:
		//todo
		log.Warn("Lingo SimpleRemote is not supported yet")
	case ipod.LingoDisplayRemoteID:
		var dispDev dispremote.DeviceDispRemote
		if avrcpSource != nil {
			dispDev = avrcpSource
		}
		dispremote.HandleDispRemote(cmd, cmdWriter, dispDev)
	case ipod.LingoExtRemoteID:
		var extDev extremote.DeviceExtRemote
		if avrcpSource != nil {
			extDev = avrcpSource
		}
		extRemoteHandler.Handle(cmd, cmdWriter, extDev)
	case ipod.LingoDigitalAudioID:
		audio.HandleAudio(cmd, cmdWriter, devAudio)
	}
}
func dirPrefix(dir trace.Dir, text string) string {
	switch dir {
	case trace.DirIn:
		return "<< " + text
	case trace.DirOut:
		return ">> " + text
	default:
		return "?? " + text
	}
}
func dumpTrace(tr *trace.Reader) {
	q := trace.Queue{}
	for {
		var msg trace.Msg
		err := tr.ReadMsg(&msg)
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Fatal(err)
		}
		q.Enqueue(&msg)
	}

	serde := ipod.CommandSerde{}

	for {
		head := q.Head()
		if head == nil {
			break
		}
		dir := head.Dir
		tdr := trace.NewQueueDirReader(&q, dir)
		d := hid.NewDecoder(hid.NewReportReader(tdr), hidReportDefs)

		frame, err := d.ReadFrame()
		if err == io.EOF {
			break
		}
		logFrame(frame, err, dirPrefix(dir, "FRAME"))
		if err != nil {
			continue
		}

		packetReader := ipod.NewPacketReader(frame)
		for {
			packet, err := packetReader.ReadPacket()
			if err == io.EOF {
				break
			}
			logPacket(packet, err, dirPrefix(dir, "PACKET"))
			if err != nil {
				continue
			}

			cmd, err := serde.UnmarshalCmd(packet)
			logCmd(cmd, err, dirPrefix(dir, "CMD"))
		}
	}
	log.Warnf("EOF")
}
