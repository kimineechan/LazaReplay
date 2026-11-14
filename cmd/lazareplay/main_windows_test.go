//go:build windows

package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"lazareplay/internal/recorder"
	"lazareplay/internal/settings"
	"lazareplay/internal/winui"
)

func TestOpeningTheProgramRecordsNothing(t *testing.T) {
	// The program records when the command line says --start or the user
	// has turned on "Start recording when LazaReplay opens". Neither is so
	// on a first run, with or without a window.
	for _, arguments := range [][]string{nil, {"--tray"}, {"--unknown"}} {
		if asked := parse(arguments); asked.start || settings.Default().Record {
			t.Errorf("%v would start recording", arguments)
		}
	}
	if !parse([]string{"--start"}).start || !parse([]string{"--tray", "--start"}).start {
		t.Error("--start should start recording")
	}
}

func TestParse(t *testing.T) {
	for _, c := range []struct {
		arguments string
		want      request
	}{
		{"", request{ask: signalShow, alone: true}},
		{"--tray", request{hidden: true, ask: -1, alone: true}},
		{"--start", request{start: true, ask: signalStart, alone: true}},
		{"--tray --start", request{hidden: true, start: true, ask: signalStart, alone: true}},
		{"--start --tray", request{hidden: true, start: true, ask: signalStart, alone: true}},
		{"--stop", request{ask: signalStop}},
		{"--save", request{ask: signalSave}},
		{"--exit", request{ask: signalExit}},
		{"--tray --save", request{hidden: true, ask: signalSave}},
		{"--nonsense", request{ask: signalShow, alone: true}},
	} {
		if got := parse(strings.Fields(c.arguments)); got != c.want {
			t.Errorf("%q: got %+v, want %+v", c.arguments, got, c.want)
		}
	}
}

func TestProfile(t *testing.T) {
	t.Setenv("LAZAREPLAY_SETTINGS", "")
	if got := profile(); got != "" {
		t.Fatalf("in ordinary use the names carry nothing extra, got %q", got)
	}
	t.Setenv("LAZAREPLAY_SETTINGS", `C:\Scratch\one.json`)
	one := profile()
	if len(one) != 9 || one[0] != '-' {
		t.Fatalf("got %q", one)
	}
	t.Setenv("LAZAREPLAY_SETTINGS", `c:\scratch\ONE.json`)
	if got := profile(); got != one {
		t.Errorf("the same file spelt differently gave %q and %q", one, got)
	}
	t.Setenv("LAZAREPLAY_SETTINGS", `C:\Scratch\two.json`)
	if got := profile(); got == one {
		t.Error("two settings files gave the same name")
	}
}

var monitors = []recorder.Monitor{
	{Name: `\\.\DISPLAY1`, Width: 1920, Height: 1080, Main: true},
	{Name: `\\.\DISPLAY5`, Width: 2560, Height: 1440},
}

func TestLookWhileStopped(t *testing.T) {
	f := look(settings.Default(), monitors, false, false, false, recorder.Status{}, "")
	if !strings.HasPrefix(f.Status, "Not recording") {
		t.Errorf("status %q", f.Status)
	}
	if f.Light != winui.Off || f.Level != 0 || f.Held != "Empty" {
		t.Errorf("the light, the bar or the loop shows something: %+v", f)
	}
	if f.Flip != "Start recording" || f.CanSave {
		t.Errorf("buttons: %q, can save %v", f.Flip, f.CanSave)
	}
	if f.Icon != iconIdle || f.Tip != "LazaReplay: not recording" {
		t.Errorf("by the clock: icon %d, %q", f.Icon, f.Tip)
	}
	if !strings.Contains(f.Hint, "records nothing") {
		t.Errorf("hint %q", f.Hint)
	}
	if f.Sound != "Recorded once recording starts" {
		t.Errorf("sound %q", f.Sound)
	}
	silent := settings.Default()
	silent.Sound = false
	if f := look(silent, monitors, false, false, false, recorder.Status{}, ""); f.Sound != "Off" {
		t.Errorf("sound %q", f.Sound)
	}
	// Whatever a recorder last reported, a stopped program shows none of it.
	stale := recorder.Status{State: recorder.Recording, Length: time.Minute, Bytes: 1 << 20, Encoder: "Some encoder"}
	if f := look(settings.Default(), monitors, false, true, false, stale, ""); f.Light != winui.Off || f.Level != 0 || f.CanSave {
		t.Errorf("a stopped program shows a recorder: %+v", f)
	}
}

func TestLookWhileStarting(t *testing.T) {
	for _, running := range []bool{false, true} {
		f := look(settings.Default(), monitors, true, running, false, recorder.Status{State: recorder.Starting}, "")
		if f.Status != "Starting..." || f.Light != winui.Waiting {
			t.Errorf("running %v: %q, light %d", running, f.Status, f.Light)
		}
		if f.Flip != "Stop recording" || f.Icon != iconRecording {
			t.Errorf("running %v: %q, icon %d", running, f.Flip, f.Icon)
		}
		if f.CanSave != running {
			t.Errorf("running %v: can save %v; there is nothing to save from until the recorder exists", running, f.CanSave)
		}
	}
}

func TestLookWhileRecording(t *testing.T) {
	s := settings.Default()
	r := recorder.Status{
		State: recorder.Recording, Screen: `\\.\DISPLAY1`, Width: 1920, Height: 1080, Rate: 60,
		Encoder: "Intel® Quick Sync Video H.264 Encoder MFT", Hardware: true, Sound: true,
		Length: 150 * time.Second, Bytes: 100 << 20,
	}
	f := look(s, monitors, true, true, false, r, "")
	if f.Status != "Recording the main screen at 1920 by 1080, 60 pictures a second" || f.Light != winui.Live {
		t.Errorf("%q, light %d", f.Status, f.Light)
	}
	if f.Encoder != "Intel Quick Sync Video H.264 Encoder, on the graphics chip" {
		t.Errorf("encoder %q", f.Encoder)
	}
	if f.Held != "2 min 30 s of 5 min, 100 MB in memory" || f.Level != 0.5 {
		t.Errorf("loop %q, level %v", f.Held, f.Level)
	}
	if f.Sound != "Recording what the computer plays" {
		t.Errorf("sound %q", f.Sound)
	}
	if !strings.HasPrefix(f.Hint, "Press Ctrl + Shift + F9 ") {
		t.Errorf("hint %q", f.Hint)
	}
	if f.Flip != "Stop recording" || !f.CanSave {
		t.Errorf("buttons: %q, can save %v", f.Flip, f.CanSave)
	}
	if f.Icon != iconRecording || f.Tip != "LazaReplay: keeping the last 5 minutes of the screen" {
		t.Errorf("by the clock: icon %d, %q", f.Icon, f.Tip)
	}

	r.Screen = `\\.\DISPLAY5`
	if f := look(s, monitors, true, true, false, r, ""); !strings.HasPrefix(f.Status, "Recording screen 2 ") {
		t.Errorf("status %q", f.Status)
	}
	if f := look(s, monitors, true, true, true, r, ""); f.CanSave {
		t.Error("a second save could be started while one is being written")
	}
	r.Hardware, r.Encoder = false, "H264 Encoder MFT"
	if f := look(s, monitors, true, true, false, r, ""); !strings.HasPrefix(f.Encoder, "Windows software encoder") {
		t.Errorf("encoder %q", f.Encoder)
	}
	r.Sound, r.Silent = false, "No playback device was found, so clips will be silent."
	if f := look(s, monitors, true, true, false, r, ""); f.Sound != r.Silent {
		t.Errorf("sound %q", f.Sound)
	}
	s.Key = 0
	if f := look(s, monitors, true, true, false, r, ""); !strings.HasPrefix(f.Hint, "No shortcut is set") {
		t.Errorf("hint %q", f.Hint)
	}
	if f := look(s, monitors, true, true, false, r, "Taken."); f.Hint != "Taken." {
		t.Errorf("hint %q", f.Hint)
	}
}

func TestLookWhenHeldUp(t *testing.T) {
	r := recorder.Status{State: recorder.Waiting, Problem: "Windows is showing a protected screen. Recording will resume by itself."}
	f := look(settings.Default(), monitors, true, true, false, r, "")
	if f.Status != r.Problem || f.Light != winui.Waiting {
		t.Errorf("%q, light %d", f.Status, f.Light)
	}
	if f.Icon != iconRecording || f.Flip != "Stop recording" {
		t.Errorf("the program is still switched on: icon %d, %q", f.Icon, f.Flip)
	}
}

func TestMenu(t *testing.T) {
	a := &app{settings: settings.Default()}
	entries := a.menu()
	if entries[0].Text != "Start recording" || entries[1].Text != "Save the last 5 minutes" || !entries[1].Greyed {
		t.Errorf("stopped: %q, %q greyed %v", entries[0].Text, entries[1].Text, entries[1].Greyed)
	}
	a.recording = true
	entries = a.menu()
	if entries[0].Text != "Stop recording" || entries[1].Greyed {
		t.Errorf("recording: %q, save greyed %v", entries[0].Text, entries[1].Greyed)
	}
	a.saving = true
	if a.menu()[1].Greyed != true {
		t.Error("save should be greyed while a clip is being written")
	}
	last := entries[len(entries)-1]
	if last.Text != "Exit" || last.Action == nil {
		t.Errorf("the last entry is %q", last.Text)
	}
	for _, entry := range entries {
		if entry.Text != "" && entry.Action == nil {
			t.Errorf("%q does nothing", entry.Text)
		}
	}
}

func TestWords(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0: "0 s", 59 * time.Second: "59 s", time.Minute: "1 min", 252 * time.Second: "4 min 12 s", 10 * time.Minute: "10 min",
	} {
		if got := span(d); got != want {
			t.Errorf("span(%v) = %q, want %q", d, got, want)
		}
	}
	for bytes, want := range map[int64]string{0: "0.0 MB", 5 << 20: "5.0 MB", 212 << 20: "212 MB", 1434 << 20: "1.4 GB"} {
		if got := megabytes(bytes); got != want {
			t.Errorf("megabytes(%d) = %q, want %q", bytes, got, want)
		}
	}
	if got := sentence(errors.New("the disk is full")); got != "The disk is full." {
		t.Errorf("got %q", got)
	}
	if got := sentence(errors.New("Already a sentence.")); got != "Already a sentence." {
		t.Errorf("got %q", got)
	}
	if got := screenName(monitors, `\\.\DISPLAY5`, true); got != "Screen 2" {
		t.Errorf("got %q", got)
	}
	if got := screenName(monitors, "", false); got != "the main screen" {
		t.Errorf("got %q", got)
	}
}
