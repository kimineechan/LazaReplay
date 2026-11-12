//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"
	"unsafe"

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

	iconRecording = 1 // numbers of the icons in the program's resources
	iconPaused    = 2

	signalShow = 0 // what a second copy of the program can ask of the first
	signalSave = 1
	signalExit = 2
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

	rec        *recorder.Recorder // nil while paused or between restarts
	generation int                // counts restarts, so a late one can tell it was overtaken
	paused     bool
	saving     bool
	leaving    bool   // exit was asked for during a save and waits for it
	hinted     bool   // the "still running" note has been shown
	shortcut   string // why the shortcut does not work, or ""

	// Replay page.
	status, encoder, held, sound, clip, keys uintptr
	bar                                      *winui.Bar
	pause, save, show                        uintptr
	foot                                     uintptr
	lastClip                                 string

	// Settings page.
	length, quality, size, rate, screen, hotkey, folder uintptr
	memory, bitrate                                     uintptr
	soundBox, chimeBox, startBox                        uintptr
}

func main() {
	// One copy runs at a time. Starting the program again talks to the copy
	// that is running: it comes to the front, or with --save it saves a
	// clip, or with --exit it closes. --tray starts without the window.
	command, hidden := signalShow, false
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--tray":
			hidden = true
		case "--save":
			command = signalSave
		case "--exit":
			command = signalExit
		}
	}
	name, _ := syscall.UTF16PtrFromString("Local\\LazaReplaySingleInstance")
	_, _, exists := syscall.NewLazyDLL("kernel32.dll").NewProc("CreateMutexW").Call(0, 0, uintptr(unsafe.Pointer(name)))
	if exists == syscall.Errno(183) {
		if !hidden {
			winui.Signal(appClass, command)
		}
		return
	}
	if command != signalShow {
		return // nothing is running to save from or to close
	}
	// The loop is hundreds of megabytes of data the collector never needs to
	// look inside. Collecting early keeps memory use close to what is held.
	debug.SetGCPercent(10)
	if err := mf.Startup(); err != nil {
		winui.Fatal(appName, err)
		return
	}
	w, err := winui.New("LazaReplay | save the last minutes of your screen", appClass, 760, 580)
	if err != nil {
		winui.Fatal(appName, err)
		return
	}
	a := &app{w: w, settings: settings.Load(), monitors: recorder.Monitors()}
	a.build()
	a.fill()
	a.applyShortcut()
	a.restart()

	w.Tray(iconRecording, a.tip(), w.Show, a.menu)
	w.OnHotkey(func(int) { a.saveClip() })
	w.OnSignal(func(command int) {
		switch command {
		case signalSave:
			a.saveClip()
		case signalExit:
			a.exit()
		default:
			w.Show()
		}
	})
	// Closing the window leaves the recorder running by the clock, where its
	// icon shows that it is. Exit is on that icon's menu.
	w.OnClose(func() bool {
		w.Hide()
		if !a.hinted {
			a.hinted = true
			w.Notify("LazaReplay is still running", "It keeps recording from here. Right-click this icon to pause it or to exit.")
		}
		return false
	})
	w.OnPage(func(page int) {
		if page == 1 {
			a.listScreens()
		}
	})
	w.ShowPage(0)
	w.Every(500, a.refresh)
	w.Run(hidden)
	if a.rec != nil {
		a.rec.Stop() // wipes the loop
	}
}

// build creates the controls.
func (a *app) build() {
	w := a.w
	w.Title(appName, 28, 20, 430, 42)
	w.Label("A rolling loop. One shortcut. The last five minutes.", 29, 67, 700, 26)
	w.Section("01  Replay", 0, 28, 112, 347, 40)
	w.Section("02  Settings", 1, 385, 112, 347, 40)
	a.foot = w.Quiet(ready, 28, 542, 704, 26)

	// 01  Replay
	w.Page(0)
	w.Heading("Save what just happened", 28, 177, 700, 30)
	w.Label("The screen is recorded into memory, and anything older than the length you chose is forgotten. Nothing is written to disk until you save.", 28, 213, 704, 44)
	w.Label("Status", 28, 270, 110, 26)
	a.status = w.Line("Starting...", 143, 270, 589, 26)
	w.Label("Encoder", 28, 302, 110, 26)
	a.encoder = w.Line("", 143, 302, 589, 26)
	w.Label("In the loop", 28, 334, 110, 26)
	a.held = w.Line("", 143, 334, 589, 26)
	a.bar = w.Bar(143, 366, 589, 12)
	w.Label("Sound", 28, 392, 110, 26)
	a.sound = w.Line("", 143, 392, 589, 26)
	w.Label("Last clip", 28, 429, 110, 26)
	a.clip = w.Edit("", 143, 424, 447, 32)
	a.show = w.Button("Show in folder", 602, 423, 130, 34, func() {
		if _, err := os.Stat(a.lastClip); a.lastClip != "" && err == nil {
			w.Reveal(a.lastClip)
		} else {
			a.openFolder()
		}
	})
	a.keys = w.Quiet("", 28, 466, 704, 24)
	a.pause = w.Button("Pause recording", 176, 498, 272, 36, a.togglePause)
	a.save = w.Button("Save the last 5 minutes", 460, 498, 272, 36, a.saveClip)

	// 02  Settings
	w.Page(1)
	w.Heading("Choose what is kept", 28, 177, 700, 30)
	w.Label("Changes take effect at once. Changing the length, the picture, the screen or the sound starts the loop afresh, so save anything you want to keep first.", 28, 213, 704, 44)
	w.Label("Keep the last", 28, 270, 130, 26)
	a.length = w.Choice(lengthNames, 163, 266, 200, a.changed)
	a.memory = w.Quiet("", 375, 270, 357, 24)
	w.Label("Quality", 28, 308, 130, 26)
	a.quality = w.Choice(qualityNames, 163, 304, 200, a.changed)
	a.bitrate = w.Quiet("", 375, 308, 357, 24)
	w.Label("Picture", 28, 346, 130, 26)
	a.size = w.Choice(sizeNames, 163, 342, 200, a.changed)
	a.rate = w.Choice(rateNames, 375, 342, 200, a.changed)
	w.Label("Screen", 28, 384, 130, 26)
	a.screen = w.Choice(nil, 163, 380, 412, a.changed)
	w.Label("Shortcut", 28, 422, 130, 26)
	a.hotkey = w.Shortcut(163, 418, 200, 30, a.shortcutChanged)
	a.soundBox = w.Checkbox("Record what the computer plays", 375, 421, 357, 26, a.changed)
	w.Label("Save clips to", 28, 460, 130, 26)
	a.folder = w.Edit("", 163, 456, 427, 32)
	w.Button("Change...", 602, 455, 130, 34, func() {
		folder, err := w.FolderDialog("Choose where clips are saved")
		if err != nil {
			w.Alert("Choose a folder", err.Error())
		} else if folder != "" {
			a.settings.Folder = folder
			a.store()
			w.SetPath(a.folder, folder)
		}
	})
	a.chimeBox = w.Checkbox("Play a sound when a clip is saved", 163, 500, 280, 26, func() {
		a.settings.Chime = w.Checked(a.chimeBox)
		a.store()
	})
	a.startBox = w.Checkbox("Start with Windows", 452, 500, 280, 26, a.startChanged)
}

// fill sets the controls from the settings.
func (a *app) fill() {
	w, s := a.w, a.settings
	w.Choose(a.length, index(settings.Lengths, s.Seconds))
	w.Choose(a.quality, s.Quality)
	w.Choose(a.size, index(settings.Heights, s.Height))
	w.Choose(a.rate, index(settings.Rates, s.Rate))
	w.SetShortcutKeys(a.hotkey, s.Key, s.Modifiers)
	w.Check(a.soundBox, s.Sound)
	w.Check(a.chimeBox, s.Chime)
	w.Check(a.startBox, autostart.Command(appName) != "")
	w.SetPath(a.folder, a.clipFolder())
	w.SetText(a.save, "Save the last "+lengthNames[index(settings.Lengths, s.Seconds)])
	a.listScreens()
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
	a.w.SetEntries(a.screen, names)
	a.w.Choose(a.screen, chosen)
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
	w, s := a.w, &a.settings
	s.Seconds = settings.Lengths[max(w.Chosen(a.length), 0)]
	s.Quality = max(w.Chosen(a.quality), 0)
	s.Height = settings.Heights[max(w.Chosen(a.size), 0)]
	s.Rate = settings.Rates[max(w.Chosen(a.rate), 0)]
	s.Sound = w.Checked(a.soundBox)
	s.Monitor = ""
	if i := w.Chosen(a.screen); i >= 0 && i < len(a.monitors) && !a.monitors[i].Main {
		s.Monitor = a.monitors[i].Name
	}
	a.store()
	w.SetText(a.save, "Save the last "+lengthNames[index(settings.Lengths, s.Seconds)])
	a.notes()
	w.SetTray(a.icon(), a.tip())
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
	a.w.SetText(a.memory, fmt.Sprintf("Uses up to about %s of memory", megabytes(int64(o.Memory(width, height)))))
	a.w.SetText(a.bitrate, fmt.Sprintf("%d by %d at about %.0f megabits a second", w, h, float64(o.Bitrate(w, h, o.Rate))/1e6))
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
		Height: s.Height, Monitor: s.Monitor, Sound: s.Sound,
	}
}

// restart stops the recorder, if one is running, and starts another with
// the current settings. Both happen off the window's thread so the window
// never waits for the graphics driver.
func (a *app) restart() {
	old := a.rec
	a.rec = nil
	a.generation++
	generation, paused, options := a.generation, a.paused, a.options()
	go func() {
		if old != nil {
			old.Stop()
		}
		if paused {
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
	a.refresh()
}

func (a *app) togglePause() {
	a.paused = !a.paused
	if a.paused {
		a.w.SetText(a.pause, "Resume recording")
		a.say("Paused. The loop was wiped from memory.")
	} else {
		a.w.SetText(a.pause, "Pause recording")
		a.say(ready)
	}
	a.w.SetTray(a.icon(), a.tip())
	a.restart()
}

func (a *app) icon() int {
	if a.paused {
		return iconPaused
	}
	return iconRecording
}

// tip is the text shown when the mouse rests on the notification area icon.
func (a *app) tip() string {
	if a.paused {
		return "LazaReplay: paused, not recording"
	}
	return "LazaReplay: keeping the last " + lengthNames[index(settings.Lengths, a.settings.Seconds)] + " of the screen"
}

// menu is shown on a right click of the notification area icon.
func (a *app) menu() []winui.MenuEntry {
	pause := "Pause recording"
	if a.paused {
		pause = "Resume recording"
	}
	return []winui.MenuEntry{
		{Text: "Save the last " + lengthNames[index(settings.Lengths, a.settings.Seconds)], Action: a.saveClip, Greyed: a.paused || a.saving},
		{Text: "Open LazaReplay", Action: a.w.Show},
		{Text: "Open the clips folder", Action: a.openFolder},
		{},
		{Text: pause, Action: a.togglePause},
		{},
		{Text: "Exit", Action: a.exit},
	}
}

// refresh shows the state of the recorder.
func (a *app) refresh() {
	w := a.w
	var s recorder.Status
	if a.rec != nil {
		s = a.rec.Status()
	}
	window := time.Duration(a.settings.Seconds) * time.Second
	status, encoder, sound := "Starting...", "", "Off"
	switch {
	case a.paused:
		status = "Paused. Nothing is being recorded and the loop is empty."
	case a.rec == nil || s.State == recorder.Starting:
	case s.State == recorder.Recording:
		status = fmt.Sprintf("Recording %s at %d by %d, %d pictures a second", screenName(a.monitors, s.Screen, false), s.Width, s.Height, s.Rate)
		if s.Rotated {
			status = "Recording a turned screen on its side, without the pointer"
		}
	default:
		status = s.Problem
	}
	if s.Encoder != "" {
		encoder = strings.TrimSuffix(strings.ReplaceAll(s.Encoder, "®", ""), " MFT")
		if s.Hardware {
			encoder += ", on the graphics chip"
		} else {
			encoder = "Windows software encoder, on the processor, kept light at 720p"
		}
	}
	switch {
	case !a.settings.Sound || a.paused:
	case s.Sound:
		sound = "Recording what the computer plays"
	case s.Silent != "":
		sound = s.Silent
	default:
		sound = "Starting..."
	}
	w.SetText(a.status, status)
	w.SetText(a.encoder, encoder)
	w.SetText(a.sound, sound)
	w.SetText(a.held, fmt.Sprintf("%s of %s, %s in memory", span(s.Length), span(window), megabytes(int64(s.Bytes))))
	a.bar.Set(s.Length.Seconds() / window.Seconds())
	keys := "No shortcut is set. Choose one in Settings to save from any program."
	if a.shortcut != "" {
		keys = a.shortcut
	} else if a.settings.Key != 0 {
		keys = "Press " + shortcutName(a.settings.Key, a.settings.Modifiers) + " in any program, or in a game, to save."
	}
	w.SetText(a.keys, keys)
}

// say puts a message on the line at the foot of the window.
func (a *app) say(text string) { a.w.SetText(a.foot, text) }

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

// saveClip writes the loop to a new file. It is called by the button, the
// shortcut and the notification area menu.
func (a *app) saveClip() {
	if a.saving {
		return
	}
	rec := a.rec
	if rec == nil {
		if a.paused {
			a.say("Recording is paused, so there is nothing to save.")
		} else {
			a.say("The recorder is still starting. Try again in a moment.")
		}
		return
	}
	path := unused(filepath.Join(a.clipFolder(), "Replay "+time.Now().Format("2006-01-02 15.04.05")+".mp4"))
	a.saving = true
	a.w.Enable(a.save, false)
	a.say("Saving...")
	go func() {
		clip, err := rec.Save(path)
		a.w.Post(func() {
			a.saving = false
			a.w.Enable(a.save, true)
			if a.leaving {
				a.w.Close()
				return
			}
			if err != nil {
				a.say("Not saved. " + sentence(err))
				if !a.w.Visible() {
					a.w.Notify("The clip was not saved", sentence(err))
				}
				return
			}
			a.lastClip = clip.Path
			a.w.SetPath(a.clip, clip.Path)
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
	if err := os.MkdirAll(folder, 0755); err != nil {
		a.say(sentence(err))
		return
	}
	a.w.OpenFolder(folder)
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
	a.settings.Key, a.settings.Modifiers = a.w.ShortcutKeys(a.hotkey)
	a.store()
	a.applyShortcut()
	a.refresh()
}

// applyShortcut registers the shortcut with Windows.
func (a *app) applyShortcut() {
	a.shortcut = ""
	if err := a.w.SetHotkey(1, a.settings.Key, a.settings.Modifiers); err != nil {
		a.shortcut = shortcutName(a.settings.Key, a.settings.Modifiers) + " is taken by another program. Choose another shortcut in Settings."
	}
}

// shortcutName writes a key combination out, such as Ctrl + Shift + F9.
func shortcutName(key, modifiers uint32) string {
	var parts []string
	if modifiers&winui.Ctrl != 0 {
		parts = append(parts, "Ctrl")
	}
	if modifiers&winui.Shift != 0 {
		parts = append(parts, "Shift")
	}
	if modifiers&winui.Alt != 0 {
		parts = append(parts, "Alt")
	}
	return strings.Join(append(parts, winui.KeyName(key)), " + ")
}

func (a *app) startChanged() {
	var err error
	if a.w.Checked(a.startBox) {
		var program string
		if program, err = os.Executable(); err == nil {
			err = autostart.Enable(appName, `"`+program+`" --tray`)
		}
	} else {
		err = autostart.Disable(appName)
	}
	if err != nil {
		a.say(sentence(err))
		a.w.Check(a.startBox, autostart.Command(appName) != "")
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
