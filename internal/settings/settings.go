// Package settings keeps the user's choices between runs, in a small JSON
// file in the user's own application data folder.
package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// The values each choice may take. The first lists are in the order the
// window shows them.
var (
	Lengths = []int{30, 60, 120, 300, 600} // seconds
	Heights = []int{720, 1080, 1440, 0}    // tallest picture; 0 keeps the screen's own size
	Rates   = []int{30, 60}                // pictures per second
)

// Settings are the user's choices.
type Settings struct {
	Seconds   int    `json:"seconds"`   // how much to keep
	Quality   int    `json:"quality"`   // 0 low, 1 medium, 2 high
	Height    int    `json:"height"`    // tallest picture to record
	Rate      int    `json:"rate"`      // pictures per second
	Monitor   string `json:"monitor"`   // Windows name of the screen; empty for the main one
	Sound     bool   `json:"sound"`     // record what the computer plays
	Folder    string `json:"folder"`    // where clips are saved; empty for the Videos folder
	Key       uint32 `json:"key"`       // virtual key code of the shortcut; 0 for none
	Modifiers uint32 `json:"modifiers"` // 1 Alt, 2 Ctrl, 4 Shift
	Chime     bool   `json:"chime"`     // play a sound when a clip is saved
	Record    bool   `json:"record"`    // start recording when the program opens, without being asked
	Mic       bool   `json:"mic"`       // record a microphone as well
	MicDevice string `json:"micDevice"` // Windows name of the microphone; empty for the default one
}

// Default returns the choices a first run starts with: five minutes at
// medium quality, up to 1080p at 60 a second, with sound, saved with
// Ctrl + Shift + F9. Recording does not start by itself: Record is off,
// and a settings file written before that choice existed leaves it off.
// No microphone is recorded unless the user chooses one.
func Default() Settings {
	return Settings{Seconds: 300, Quality: 1, Height: 1080, Rate: 60, Sound: true, Key: 0x78, Modifiers: 2 | 4, Chime: true}
}

func oneOf(value int, allowed []int, fallback int) int {
	for _, v := range allowed {
		if v == value {
			return value
		}
	}
	return fallback
}

// Checked returns the settings with any value that is out of range put
// back to its default, so a damaged or hand-edited file cannot give the
// recorder something it does not expect.
func (s Settings) Checked() Settings {
	d := Default()
	s.Seconds = oneOf(s.Seconds, Lengths, d.Seconds)
	s.Height = oneOf(s.Height, Heights, d.Height)
	s.Rate = oneOf(s.Rate, Rates, d.Rate)
	if s.Quality < 0 || s.Quality > 2 {
		s.Quality = d.Quality
	}
	if s.Key > 0xFE {
		s.Key, s.Modifiers = d.Key, d.Modifiers
	}
	s.Modifiers &= 1 | 2 | 4
	return s
}

// Path returns where the settings file lives. The environment variable
// LAZAREPLAY_SETTINGS names another file, which is how the tests keep away
// from the user's own settings.
func Path() (string, error) {
	if path := os.Getenv("LAZAREPLAY_SETTINGS"); path != "" {
		return path, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "LazaReplay", "settings.json"), nil
}

// Load reads the settings. A missing or unreadable file gives the defaults.
func Load() Settings {
	path, err := Path()
	if err != nil {
		return Default()
	}
	return LoadFrom(path)
}

// LoadFrom reads the settings from a given file.
func LoadFrom(path string) Settings {
	s := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	if json.Unmarshal(data, &s) != nil {
		return Default()
	}
	return s.Checked()
}

// Save writes the settings.
func (s Settings) Save() error {
	path, err := Path()
	if err != nil {
		return err
	}
	return s.SaveTo(path)
}

// SaveTo writes the settings to a given file. The file is replaced in one
// step, so a crash part-way cannot leave half a file behind.
func (s Settings) SaveTo(path string) error {
	data, err := json.MarshalIndent(s.Checked(), "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	part := path + ".part"
	if err := os.WriteFile(part, append(data, '\n'), 0600); err != nil {
		return err
	}
	if err := os.Rename(part, path); err != nil {
		os.Remove(part)
		return err
	}
	return nil
}
