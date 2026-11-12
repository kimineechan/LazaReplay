package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "settings.json")
	want := Settings{Seconds: 120, Quality: 2, Height: 0, Rate: 30, Monitor: `\\.\DISPLAY2`, Sound: false, Folder: `D:\Clips`, Key: 0x7A, Modifiers: 1 | 2, Chime: false}
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
