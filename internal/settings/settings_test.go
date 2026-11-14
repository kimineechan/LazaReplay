package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "settings.json")
	want := Settings{Seconds: 120, Quality: 2, Height: 0, Rate: 30, Monitor: `\\.\DISPLAY2`, Sound: false, Folder: `D:\Clips`, Key: 0x7A, Modifiers: 1 | 2, Chime: false, Record: true, Mic: true, MicDevice: `{0.0.1.00000000}.{a-microphone}`}
	if err := want.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	if got := LoadFrom(path); got != want {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
	if _, err := os.Stat(path + ".part"); err == nil {
		t.Fatal("the temporary file was left behind")
	}
}

func TestMissingFileGivesDefaults(t *testing.T) {
	if got := LoadFrom(filepath.Join(t.TempDir(), "none.json")); got != Default() {
		t.Fatalf("got %+v", got)
	}
}

func TestDamagedFileGivesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	for _, content := range []string{"", "{", "not json", `{"seconds": "five"}`, "[1,2,3]"} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if got := LoadFrom(path); got != Default() {
			t.Errorf("%q gave %+v", content, got)
		}
	}
}

func TestOutOfRangeValuesAreReset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	content := `{"seconds": 7, "quality": 9, "height": 333, "rate": 144, "key": 4096, "modifiers": 255, "sound": false, "folder": "E:\\Keep"}`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	got, d := LoadFrom(path), Default()
	if got.Seconds != d.Seconds || got.Quality != d.Quality || got.Height != d.Height || got.Rate != d.Rate {
		t.Fatalf("out-of-range values kept: %+v", got)
	}
	if got.Key != d.Key || got.Modifiers != d.Modifiers {
		t.Fatalf("impossible shortcut kept: key %d modifiers %d", got.Key, got.Modifiers)
	}
	if got.Sound || got.Folder != `E:\Keep` {
		t.Fatalf("valid values were lost: %+v", got)
	}
}

func TestMissingFieldsKeepTheirDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"seconds": 60}`), 0600); err != nil {
		t.Fatal(err)
	}
	want := Default()
	want.Seconds = 60
	if got := LoadFrom(path); got != want {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

func TestRecordingDoesNotStartByItself(t *testing.T) {
	if Default().Record {
		t.Fatal("a first run must not record until the user starts it")
	}
	if Default().Mic {
		t.Fatal("no microphone may be recorded until the user chooses one")
	}
	// A settings file written by version 1.0.0, which always recorded, has
	// no such choice in it. It must not turn recording on.
	path := filepath.Join(t.TempDir(), "settings.json")
	old := `{"seconds": 300, "quality": 1, "height": 1080, "rate": 60, "monitor": "", "sound": true, "folder": "", "key": 120, "modifiers": 6, "chime": true}`
	if err := os.WriteFile(path, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	if got := LoadFrom(path); got.Record || got.Mic {
		t.Fatalf("an older settings file turned recording or the microphone on: %+v", got)
	}
	// Only the user asking for it does.
	if err := os.WriteFile(path, []byte(`{"record": true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if !LoadFrom(path).Record {
		t.Fatal("the choice to record on opening was not kept")
	}
}
