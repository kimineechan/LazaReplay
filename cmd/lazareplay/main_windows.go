//go:build windows

package main

import (
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"lazareplay/internal/audio"
	"lazareplay/internal/autostart"
	"lazareplay/internal/mf"
	"lazareplay/internal/recorder"
	"lazareplay/internal/settings"
	"lazareplay/internal/winui"
)

const (
	appName  = "LazaReplay"
	appClass = "LazaReplayNativeWindow"
	ready    = "Ready. All work stays on this computer."

	iconIdle      = 2 // numbers of the icons in the program's resources: by the clock while nothing is recorded
	iconRecording = 3 // and while recording

	signalShow  = 0 // what a second copy of the program can ask of the first
	signalSave  = 1
	signalExit  = 2
	signalStart = 3
	signalStop  = 4
)

var (
	lengthNames  = []string{"30 seconds", "1 minute", "2 minutes", "5 minutes", "10 minutes"}
	qualityNames = []string{"Low", "Medium", "High"}
	sizeNames    = []string{"Up to 720p", "Up to 1080p", "Up to 1440p", "Same as the screen"}
	rateNames    = []string{"30 pictures a second", "60 pictures a second"}
)

// app is the window and what it controls.
type app struct {
	w        *winui.Window
	settings settings.Settings
	monitors []recorder.Monitor
	mics     []audio.Device

	rec        *recorder.Recorder // nil while stopped or between restarts
	generation int                // counts restarts, so a late one can tell it was overtaken
	recording  bool               // the user has started recording and not stopped it
	saving     bool
	leaving    bool   // exit was asked for during a save and waits for it
	hinted     bool   // the "still open" note has been shown
	shortcut   string // why the shortcut does not work, or ""
	icon       int    // what the icon by the clock shows
	tip        string //

	// Replay page.
	status, encoder, held, sound, clip, keys *winui.Control
	light, bar                               *winui.Control
	toggle, save                             *winui.Control
	foot                                     *winui.Control
	lastClip                                 string

	// Settings page.
	length, quality, size, rate, screen, mic, hotkey, folder *winui.Control
	memory, bitrate, taken                                   *winui.Control
	soundBox, chimeBox, startBox, recordBox                  *winui.Control
}

// request is what the command line asks for.
type request struct {
	hidden bool // --tray: open with no window
	start  bool // --start: record from the moment the program opens
	ask    int  // what to ask of a copy that is already running, or -1 for nothing
	alone  bool // whether the program opens when no copy is running
}

// parse reads the command line. One copy runs at a time, and starting the
// program again talks to the copy that is running: it comes to the front,
// or with --start, --stop, --save or --exit it does that. --tray opens the
// program with no window.
func parse(arguments []string) request {
	r := request{ask: signalShow, alone: true}
	for _, argument := range arguments {
		switch argument {
		case "--tray":
			r.hidden = true
		case "--start":
			r.start = true
		case "--stop":
			r.ask, r.alone = signalStop, false
		case "--save":
			r.ask, r.alone = signalSave, false
		case "--exit":
			r.ask, r.alone = signalExit, false
		}
	}
	switch {
	case !r.alone: // there has to be a running copy to stop, to save from or to close
	case r.start:
		r.ask = signalStart
	case r.hidden:
		r.ask = -1
	}
	return r
}

// profile is added to the names by which copies of the program find each
// other. It is empty in ordinary use. A copy given a settings file of its
// own through LAZAREPLAY_SETTINGS, as in testing, gets a name of its own
// from that file, so it runs beside the copy in daily use and the switches
// on the command line reach the copy they were meant for.
func profile() string {
	path := os.Getenv("LAZAREPLAY_SETTINGS")
	if path == "" {
		return ""
	}
	if full, err := filepath.Abs(path); err == nil {
		path = full
	}
	hash := fnv.New32a()
	hash.Write([]byte(strings.ToLower(path)))
	return fmt.Sprintf("-%08x", hash.Sum32())
}

func main() {
	asked := parse(os.Args[1:])
	name, _ := syscall.UTF16PtrFromString("Local\\LazaReplaySingleInstance" + profile())
	_, _, exists := syscall.NewLazyDLL("kernel32.dll").NewProc("CreateMutexW").Call(0, 0, uintptr(unsafe.Pointer(name)))
	if exists == syscall.Errno(183) {
		if asked.ask >= 0 {
			winui.Signal(appClass+profile(), asked.ask)
		}
		return
	}
	if !asked.alone {
		return
	}
	// The loop is hundreds of megabytes of data the collector never needs to
	// look inside. Collecting early keeps memory use close to what is held.
	debug.SetGCPercent(10)
	if err := mf.Startup(); err != nil {
		winui.Fatal(appName, err)
		return
	}
	w, err := winui.New("LazaReplay | save the last minutes of your screen", appClass+profile(), 760, 678)
	if err != nil {
		winui.Fatal(appName, err)
		return
	}
	a := &app{w: w, settings: settings.Load(), monitors: recorder.Monitors()}
	// Opening the program records nothing. Recording starts when the user
	// starts it, unless they have asked for it to start by itself.
	a.recording = asked.start || a.settings.Record
	a.build()
	a.fill()
	a.applyShortcut()
	w.Tray(iconIdle, "", w.Show, a.menu)
	a.restart()

	w.OnHotkey(func(int) { a.saveClip() })
	w.OnSignal(func(command int) {
		switch command {
		case signalSave:
			a.saveClip()
		case signalExit:
			a.exit()
		case signalStart:
			a.start()
		case signalStop:
			a.stop()
		default:
			w.Show()
		}
	})
	// Closing the window leaves the program by the clock, where its icon
	// shows whether it is recording. Exit is on that icon's menu.
	w.OnClose(func() bool {
		w.Hide()
		if !a.hinted {
			a.hinted = true
			if a.recording {
				w.Notify("LazaReplay is still recording", "It keeps the last "+a.lengthName()+" from here. Right-click this icon to save, to stop or to exit.")
			} else {
				w.Notify("LazaReplay is still open", "It is not recording. Right-click this icon to start recording or to exit.")
			}
		}
		return false
	})
	w.OnPage(func(page int) {
		if page == 1 {
			a.listScreens()
			a.listMics()
		}
	})
	w.ShowPage(0)
	w.Every(500, a.refresh)
	w.Run(asked.hidden)
	if a.rec != nil {
		a.rec.Stop() // wipes the loop
	}
}

// build creates the controls.
func (a *app) build() {
	w := a.w
	w.Grip(100)
	w.Frame()
	w.Title(appName, 32, 24, 400, 40)
	w.Quiet("A rolling loop. One shortcut. The last five minutes.", 33, 66, 600, 22)
	w.Strip(32, 104, 696, 44)
	w.Section("01  Replay", 0, 36, 108, 342, 36)
	w.Section("02  Settings", 1, 382, 108, 342, 36)
	w.Rule(32, 622, 696)
	a.foot = w.Quiet(ready, 32, 632, 696, 24)

	// 01  Replay
	w.Page(0)
	w.Heading("Save what just happened", 32, 168, 696, 30)
	w.Label("While it records, the screen is kept in memory and anything older than the length you chose is forgotten. Nothing is written to disk until you save.", 32, 202, 696, 44)
	w.Panel(32, 256, 696, 232)
	w.Caption("Status", 52, 270, 110, 28)
	a.light = w.Light(172, 270, 12, 28)
	a.status = w.Line("", 194, 270, 514, 28)
	w.Caption("Encoder", 52, 306, 110, 28)
	a.encoder = w.Line("", 172, 306, 536, 28)
	w.Caption("In the loop", 52, 342, 110, 28)
	a.held = w.Line("", 172, 342, 536, 28)
	a.bar = w.Bar(172, 376, 536, 6)
	w.Caption("Sound", 52, 394, 110, 28)
	a.sound = w.Line("", 172, 394, 536, 28)
	w.Caption("Last clip", 52, 438, 110, 34)
	a.clip = w.Path("Nothing saved yet", 172, 438, 392, 34)
	w.Button("Show in folder", 574, 438, 134, 34, func() {
		if _, err := os.Stat(a.lastClip); a.lastClip != "" && err == nil {
			w.Reveal(a.lastClip)
		} else {
			a.openFolder()
		}
	})
	a.keys = w.Quiet("", 32, 506, 696, 24)
	a.toggle = w.Button("Start recording", 286, 556, 208, 40, a.flip)
	a.save = w.Button("Save the last 5 minutes", 506, 556, 222, 40, a.saveClip)

	// 02  Settings
	w.Page(1)
	w.Heading("Choose what is kept", 32, 168, 696, 30)
	w.Label("Changes take effect at once. While recording, changing the length, the picture, the screen, the microphone or the sound starts the loop afresh, so save anything you want to keep first.", 32, 202, 696, 44)
	w.Panel(32, 256, 696, 286)
	w.Caption("Keep the last", 52, 268, 116, 34)
	a.length = w.Choice(lengthNames, 172, 268, 200, 34, a.changed)
	a.memory = w.Note("", 386, 268, 322, 34)
	w.Caption("Quality", 52, 306, 116, 34)
	a.quality = w.Choice(qualityNames, 172, 306, 200, 34, a.changed)
	a.bitrate = w.Note("", 386, 306, 322, 34)
	w.Caption("Picture", 52, 344, 116, 34)
	a.size = w.Choice(sizeNames, 172, 344, 200, 34, a.changed)
	a.rate = w.Choice(rateNames, 386, 344, 200, 34, a.changed)
	w.Caption("Screen", 52, 382, 116, 34)
	a.screen = w.Choice(nil, 172, 382, 414, 34, a.changed)
	w.Caption("Microphone", 52, 420, 116, 34)
	a.mic = w.Choice(nil, 172, 420, 414, 34, a.changed)
	w.Caption("Shortcut", 52, 458, 116, 34)
	a.hotkey = w.Shortcut(172, 458, 200, 34, a.shortcutChanged)
	a.taken = w.Note("", 386, 458, 322, 34)
	w.Caption("Save clips to", 52, 496, 116, 34)
	a.folder = w.Path("", 172, 496, 392, 34)
	w.Button("Change...", 574, 496, 134, 34, func() {
		folder, err := w.FolderDialog("Choose where clips are saved")
		if err != nil {
			a.say(sentence(err))
		} else if folder != "" {
			a.settings.Folder = folder
			a.store()
			a.folder.SetText(folder)
		}
	})
	a.soundBox = w.Switch("Record what the computer plays", 52, 554, 320, 26, a.changed)
	a.chimeBox = w.Switch("Play a sound when a clip is saved", 52, 586, 320, 26, func() {
		a.settings.Chime = a.chimeBox.On()
		a.store()
	})
	a.startBox = w.Switch("Start with Windows", 386, 554, 322, 26, a.startChanged)
	a.recordBox = w.Switch("Start recording when LazaReplay opens", 386, 586, 322, 26, func() {
		a.settings.Record = a.recordBox.On()
		a.store()
	})
}

// fill sets the controls from the settings.
func (a *app) fill() {
	s := a.settings
	a.length.Choose(index(settings.Lengths, s.Seconds))
	a.quality.Choose(s.Quality)
	a.size.Choose(index(settings.Heights, s.Height))
	a.rate.Choose(index(settings.Rates, s.Rate))
	a.hotkey.SetKeys(s.Key, s.Modifiers)
	a.soundBox.Turn(s.Sound)
	a.chimeBox.Turn(s.Chime)
	a.startBox.Turn(autostart.Command(appName+profile()) != "")
	a.recordBox.Turn(s.Record)
	a.folder.SetText(a.clipFolder())
	a.listScreens()
	a.listMics()
	a.notes()
}

func index(list []int, value int) int {
	for i, v := range list {
		if v == value {
			return i
		}
	}
	return 0
}

func (a *app) lengthName() string { return lengthNames[index(settings.Lengths, a.settings.Seconds)] }

// listScreens fills the list of screens with those connected now.
func (a *app) listScreens() {
	a.monitors = recorder.Monitors()
	names := make([]string, len(a.monitors))
	chosen := 0
	for i, m := range a.monitors {
		names[i] = fmt.Sprintf("%s (%d by %d)", screenName(a.monitors, m.Name, true), m.Width, m.Height)
		if m.Name == a.settings.Monitor {
			chosen = i
		}
	}
	a.screen.SetEntries(names)
	a.screen.Choose(chosen)
}

// listMics fills the list of microphones with those plugged in now. No
// microphone is recorded unless one is chosen here.
func (a *app) listMics() {
	a.mics = audio.Microphones()
	names, chosen := micEntries(a.mics, a.settings.Mic, a.settings.MicDevice)
	a.mic.SetEntries(names)
	a.mic.Choose(chosen)
}

// micEntries returns the entries of the microphone list and which is
// chosen: Off, then whichever microphone Windows has as its default, then
// each microphone by name. A microphone that was chosen and is not plugged
// in keeps its place at the end, so the choice is not lost.
func micEntries(mics []audio.Device, on bool, device string) (names []string, chosen int) {
	names = []string{"Off", "Default microphone"}
	found := device == ""
	for i, m := range mics {
		names = append(names, m.Name)
		if m.ID == device {
			found, chosen = true, i+2
		}
	}
	if !found {
		names = append(names, "The microphone chosen before (not connected)")
		chosen = len(names) - 1
	}
	switch {
	case !on:
		chosen = 0
	case device == "":
		chosen = 1
	}
	return names, chosen
}

// screenName names a screen the way a person would.
func screenName(monitors []recorder.Monitor, name string, capital bool) string {
	text := "the main screen"
	for i, m := range monitors {
		if m.Name == name && !m.Main {
			text = fmt.Sprintf("screen %d", i+1)
		}
	}
	if capital {
		text = strings.TrimPrefix(text, "the ")
		text = strings.ToUpper(text[:1]) + text[1:]
	}
	return text
}

// changed runs when a choice that affects recording is made.
func (a *app) changed() {
	s := &a.settings
	s.Seconds = settings.Lengths[max(a.length.Chosen(), 0)]
	s.Quality = max(a.quality.Chosen(), 0)
	s.Height = settings.Heights[max(a.size.Chosen(), 0)]
	s.Rate = settings.Rates[max(a.rate.Chosen(), 0)]
	s.Sound = a.soundBox.On()
	switch i := a.mic.Chosen(); {
	case i <= 0:
		s.Mic = false
	case i == 1:
		s.Mic, s.MicDevice = true, ""
	case i-2 < len(a.mics):
		s.Mic, s.MicDevice = true, a.mics[i-2].ID
	default: // the one chosen before, which is not connected
		s.Mic = true
	}
	s.Monitor = ""
	if i := a.screen.Chosen(); i >= 0 && i < len(a.monitors) && !a.monitors[i].Main {
		s.Monitor = a.monitors[i].Name
	}
	a.store()
	a.notes()
	a.restart()
}

// notes shows what the choices cost.
func (a *app) notes() {
	o := a.options()
	width, height := 1920, 1080
	for _, m := range a.monitors {
		if m.Name == a.settings.Monitor || (a.settings.Monitor == "" && m.Main) {
			width, height = m.Width, m.Height
		}
	}
	w, h := o.Size(width, height)
	a.memory.SetText(fmt.Sprintf("Uses up to about %s of memory", megabytes(int64(o.Memory(width, height)))))
	a.bitrate.SetText(fmt.Sprintf("%d by %d at about %.0f megabits a second", w, h, float64(o.Bitrate(w, h, o.Rate))/1e6))
}

func (a *app) store() {
	if err := a.settings.Save(); err != nil {
		a.say("The settings could not be saved. " + sentence(err))
	}
}

func (a *app) options() recorder.Options {
	s := a.settings
	return recorder.Options{
		Window: time.Duration(s.Seconds) * time.Second, Rate: s.Rate, Quality: s.Quality,
		Height: s.Height, Monitor: s.Monitor, Sound: s.Sound, Mic: s.Mic, MicDevice: s.MicDevice,
	}
}

// restart stops the recorder, if one is running, and, when the user has
// recording switched on, starts another with the current settings. Both
// happen off the window's thread so the window never waits for the
// graphics driver.
func (a *app) restart() {
	old := a.rec
	a.rec = nil
	a.generation++
	generation, recording, options := a.generation, a.recording, a.options()
	if old != nil || recording {
		go func() {
			if old != nil {
				old.Stop() // wipes the loop
			}
			if !recording {
				return
			}
			fresh := recorder.Start(options)
			a.w.Post(func() {
				if generation != a.generation {
					go fresh.Stop() // overtaken by a later change
					return
				}
				a.rec = fresh
				a.refresh()
			})
		}()
	}
	a.refresh()
}

// start begins recording into an empty loop.
func (a *app) start() {
	if !a.recording {
		a.recording = true
		a.say("Recording. Nothing is written to disk until you save.")
		a.restart()
	}
}

// stop ends recording and wipes the loop.
func (a *app) stop() {
	if a.recording {
		a.recording = false
		a.say("Stopped. The loop was wiped from memory.")
		a.restart()
	}
}

// flip is the button that starts recording, or stops it.
func (a *app) flip() {
	if a.recording {
		a.stop()
	} else {
		a.start()
	}
}

// menu is shown on a right click of the notification area icon.
func (a *app) menu() []winui.MenuEntry {
	flip := "Start recording"
	if a.recording {
		flip = "Stop recording"
	}
	return []winui.MenuEntry{
		{Text: flip, Action: a.flip},
		{Text: "Save the last " + a.lengthName(), Action: a.saveClip, Greyed: !a.recording || a.saving},
		{},
		{Text: "Open LazaReplay", Action: a.w.Show},
		{Text: "Open the clips folder", Action: a.openFolder},
		{},
		{Text: "Exit", Action: a.exit},
	}
}

// face is everything the window and the icon by the clock say about the
// state of the recorder. It is worked out apart from the window, so that
// what each state shows can be tested.
type face struct {
	Status, Encoder, Held, Sound string
	Hint                         string // the line above the buttons
	Level                        float64
	Light                        winui.Tone
	Flip                         string // the button that starts and stops
	CanSave                      bool
	Icon                         int // by the clock
	Tip                          string
}

// look works out what to show. recording is whether the user has recording
// switched on, running whether a recorder exists yet, and trouble why the
// shortcut does not work, if it does not.
func look(s settings.Settings, monitors []recorder.Monitor, recording, running, saving bool, r recorder.Status, trouble string) face {
	length := lengthNames[index(settings.Lengths, s.Seconds)]
	if !recording {
		f := face{
			Status:  "Not recording. Nothing is kept until you press Start recording.",
			Encoder: "Chosen when recording starts",
			Held:    "Empty",
			Sound:   soundPlan(s.Sound, s.Mic),
			Hint:    "Opening LazaReplay records nothing. Press Start recording when you want the loop to run.",
			Flip:    "Start recording",
			Icon:    iconIdle,
			Tip:     "LazaReplay: not recording",
		}
		return f
	}
	window := time.Duration(s.Seconds) * time.Second
	f := face{
		Status:  "Starting...",
		Held:    fmt.Sprintf("%s of %s, %s in memory", span(r.Length), span(window), megabytes(int64(r.Bytes))),
		Sound:   "Off",
		Level:   r.Length.Seconds() / window.Seconds(),
		Light:   winui.Waiting,
		Flip:    "Stop recording",
		CanSave: running && !saving,
		Icon:    iconRecording,
		Tip:     "LazaReplay: keeping the last " + length + " of the screen",
	}
	switch {
	case !running || r.State == recorder.Starting:
	case r.State == recorder.Recording:
		f.Light = winui.Live
		f.Status = fmt.Sprintf("Recording %s at %d by %d, %d pictures a second", screenName(monitors, r.Screen, false), r.Width, r.Height, r.Rate)
		if r.Rotated {
			f.Status = "Recording a turned screen on its side, without the pointer"
		}
	default:
		f.Status = r.Problem
	}
	if r.Encoder != "" {
		f.Encoder = strings.TrimSuffix(strings.ReplaceAll(r.Encoder, "®", ""), " MFT")
		if r.Hardware {
			f.Encoder += ", on the graphics chip"
		} else {
			f.Encoder = "Windows software encoder, on the processor, kept light at 720p"
		}
	}
	f.Sound = soundNow(s.Sound, s.Mic, r)
	switch {
	case trouble != "":
		f.Hint = trouble
	case s.Key != 0:
		f.Hint = "Press " + winui.ShortcutName(s.Key, s.Modifiers) + " in any program, or in a game, to save."
	default:
		f.Hint = "No shortcut is set. Choose one in Settings to save from any program."
	}
	return f
}

// soundPlan says what sound will be recorded once recording starts.
func soundPlan(computer, mic bool) string {
	switch {
	case computer && mic:
		return "The computer and the microphone, once recording starts"
	case computer:
		return "Recorded once recording starts"
	case mic:
		return "The microphone, once recording starts"
	}
	return "Off"
}

// soundNow says what sound is being recorded, and what is in the way of
// the rest. computer and mic are what the user asked for.
func soundNow(computer, mic bool, r recorder.Status) string {
	switch {
	case !computer && !mic:
		return "Off"
	case r.Sound && r.Mic:
		return "Recording what the computer plays and the microphone"
	case r.Sound && mic && r.MicNote != "":
		return "Recording what the computer plays. " + r.MicNote
	case r.Sound:
		return "Recording what the computer plays"
	case r.Mic:
		return "Recording the microphone"
	case computer && r.Silent != "":
		return r.Silent
	case !computer && r.MicNote != "":
		return r.MicNote
	}
	return "Starting..."
}

// refresh shows the state of the recorder, in the window and by the clock.
func (a *app) refresh() {
	var status recorder.Status
	if a.rec != nil {
		status = a.rec.Status()
	}
	f := look(a.settings, a.monitors, a.recording, a.rec != nil, a.saving, status, a.shortcut)
	a.status.SetText(f.Status)
	a.light.Show(f.Light)
	a.encoder.SetText(f.Encoder)
	a.held.SetText(f.Held)
	a.bar.SetLevel(f.Level)
	a.sound.SetText(f.Sound)
	a.keys.SetText(f.Hint)
	// The button for the thing to do next is the light one: Start while
	// nothing is recorded, Save once something is.
	a.toggle.SetText(f.Flip)
	a.toggle.Lead(!a.recording)
	a.save.SetText("Save the last " + a.lengthName())
	a.save.Lead(a.recording)
	a.save.Enable(f.CanSave)
	if a.shortcut != "" {
		a.taken.SetText("Taken by another program. Choose another.")
	} else {
		a.taken.SetText("Click the box, then press the keys")
	}
	if f.Icon != a.icon || f.Tip != a.tip {
		a.icon, a.tip = f.Icon, f.Tip
		a.w.SetTray(f.Icon, f.Tip)
	}
}

// say puts a message on the line at the foot of the window.
func (a *app) say(text string) { a.foot.SetText(text) }

// clipFolder returns where clips are saved.
func (a *app) clipFolder() string {
	if a.settings.Folder != "" {
		return a.settings.Folder
	}
	videos := winui.VideosFolder()
	if videos == "" {
		home, _ := os.UserHomeDir()
		videos = filepath.Join(home, "Videos")
	}
	return filepath.Join(videos, appName)
}

// refuse says why no clip was saved: on the line at the foot, with a sound
// that is not the one for a saved clip, and by the clock when the window is
// not on show. The shortcut is pressed from inside other programs, where
// silence would leave the user guessing.
func (a *app) refuse(title, text string) {
	a.say(text)
	if a.settings.Chime {
		a.w.Warn()
	}
	if !a.w.Visible() {
		a.w.Notify(title, text)
	}
}

// saveClip writes the loop to a new file. It is called by the button, the
// shortcut, the notification area menu and the --save switch.
func (a *app) saveClip() {
	if a.saving {
		return
	}
	if !a.recording {
		a.refuse("Nothing to save", "LazaReplay is not recording, so there is nothing to save. Press Start recording first.")
		return
	}
	rec := a.rec
	if rec == nil {
		a.refuse("Nothing to save yet", "The recorder is still starting. Try again in a moment.")
		return
	}
	path := unused(filepath.Join(a.clipFolder(), "Replay "+time.Now().Format("2006-01-02 15.04.05")+".mp4"))
	a.saving = true
	a.say("Saving...")
	a.refresh()
	go func() {
		clip, err := rec.Save(path)
		a.w.Post(func() {
			a.saving = false
			a.refresh()
			if a.leaving {
				a.w.Close()
				return
			}
			if err != nil {
				a.refuse("The clip was not saved", "Not saved. "+sentence(err))
				return
			}
			a.lastClip = clip.Path
			a.clip.SetText(clip.Path)
			text := fmt.Sprintf("Saved %s (%s, %s).", filepath.Base(clip.Path), span(clip.Length), megabytes(clip.Bytes))
			a.say(text)
			if a.settings.Chime {
				a.w.Chime()
			}
			if !a.w.Visible() {
				a.w.Notify("Clip saved", text)
			}
		})
	}()
}

// exit closes the program. A clip that is being written is finished first.
func (a *app) exit() {
	if a.saving {
		a.leaving = true
		a.say("Finishing the clip, then closing...")
		return
	}
	a.w.Close()
}

// openFolder shows the folder clips are saved in, creating it if this is
// the first time.
func (a *app) openFolder() {
	folder := a.clipFolder()
	err := os.MkdirAll(folder, 0755)
	if err == nil {
		err = a.w.OpenFolder(folder)
	}
	if err != nil {
		a.say(sentence(err))
	}
}

// unused returns the path, or the path with a number added when a file of
// that name already exists.
func unused(path string) string {
	extension := filepath.Ext(path)
	base := strings.TrimSuffix(path, extension)
	for n := 2; ; n++ {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) || n > 99 {
			return path
		}
		path = fmt.Sprintf("%s (%d)%s", base, n, extension)
	}
}

func (a *app) shortcutChanged() {
	a.settings.Key, a.settings.Modifiers = a.hotkey.Keys()
	a.store()
	a.applyShortcut()
	a.refresh()
}

// applyShortcut registers the shortcut with Windows.
func (a *app) applyShortcut() {
	a.shortcut = ""
	if err := a.w.SetHotkey(1, a.settings.Key, a.settings.Modifiers); err != nil {
		a.shortcut = winui.ShortcutName(a.settings.Key, a.settings.Modifiers) + " is taken by another program. Choose another shortcut in Settings."
	}
}

// startChanged adds the program to the list Windows starts at sign-in, or
// takes it off. Started that way it has no window, and it records only if
// the user has also asked for that.
func (a *app) startChanged() {
	var err error
	if a.startBox.On() {
		var program string
		if program, err = os.Executable(); err == nil {
			err = autostart.Enable(appName+profile(), `"`+program+`" --tray`)
		}
	} else {
		err = autostart.Disable(appName + profile())
	}
	if err != nil {
		a.say(sentence(err))
		a.startBox.Turn(autostart.Command(appName+profile()) != "")
	}
}

// span writes a length of time out, such as 4 min 12 s.
func span(d time.Duration) string {
	seconds := int(d.Round(time.Second).Seconds())
	switch {
	case seconds < 60:
		return fmt.Sprintf("%d s", seconds)
	case seconds%60 == 0:
		return fmt.Sprintf("%d min", seconds/60)
	}
	return fmt.Sprintf("%d min %02d s", seconds/60, seconds%60)
}

// megabytes writes a size out, such as 212 MB or 1.4 GB.
func megabytes(bytes int64) string {
	mb := float64(bytes) / (1 << 20)
	switch {
	case mb >= 1000:
		return fmt.Sprintf("%.1f GB", mb/1024)
	case mb >= 10:
		return fmt.Sprintf("%.0f MB", mb)
	}
	return fmt.Sprintf("%.1f MB", mb)
}

// sentence turns an error into a message that starts with a capital and ends with a full stop.
func sentence(e error) string {
	text := e.Error()
	if text == "" {
		return "Unknown error."
	}
	text = strings.ToUpper(text[:1]) + text[1:]
	if !strings.HasSuffix(text, ".") {
		text += "."
	}
	return text
}
