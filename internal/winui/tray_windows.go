//go:build windows

package winui

import (
	"unsafe"
)

var (
	notifyIcon      = shell32.NewProc("Shell_NotifyIconW")
	createPopupMenu = user32.NewProc("CreatePopupMenu")
	appendMenu      = user32.NewProc("AppendMenuW")
	trackPopupMenu  = user32.NewProc("TrackPopupMenu")
	destroyMenu     = user32.NewProc("DestroyMenu")
	getCursorPos    = user32.NewProc("GetCursorPos")
	systemMetrics   = user32.NewProc("GetSystemMetrics")
)

// notifyIconData mirrors NOTIFYICONDATAW.
type notifyIconData struct {
	Size            uint32
	Window          uintptr
	ID              uint32
	Flags           uint32
	CallbackMessage uint32
	Icon            uintptr
	Tip             [128]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	Version         uint32
	InfoTitle       [64]uint16
	InfoFlags       uint32
	GUID            [16]byte
	BalloonIcon     uintptr
}

// MenuEntry is one line of the menu of the notification area icon. An
// entry with no text is a dividing line.
type MenuEntry struct {
	Text   string
	Action func()
	Greyed bool
}

type tray struct {
	data  notifyIconData
	added bool
	open  func()
	menu  func() []MenuEntry
}

func fill(target []uint16, text string) {
	units := wide(text)
	if len(units) > len(target) {
		units = append(units[:len(target)-1], 0)
	}
	copy(target, units)
}

// Tray puts an icon in the notification area, next to the clock. icon is
// the number of an icon in the program's resources and tip the text shown
// when the mouse rests on it. open runs when the icon is clicked; menu
// supplies the entries shown on a right click.
func (w *Window) Tray(icon int, tip string, open func(), menu func() []MenuEntry) {
	w.tray = &tray{open: open, menu: menu}
	d := &w.tray.data
	d.Size = uint32(unsafe.Sizeof(*d))
	d.Window, d.ID, d.CallbackMessage = w.Handle, 1, wmTray
	w.SetTray(icon, tip)
}

// SetTray changes the icon and the tip.
func (w *Window) SetTray(icon int, tip string) {
	if w.tray == nil {
		return
	}
	width, _, _ := systemMetrics.Call(49)
	height, _, _ := systemMetrics.Call(50)
	d := &w.tray.data
	d.Icon, _, _ = loadImage.Call(w.instance, uintptr(icon), 1, width, height, 0)
	fill(d.Tip[:], tip)
	d.Flags = 0x1 | 0x2 | 0x4 // message, icon, tip
	w.tray.show(w)
}

func (t *tray) show(w *Window) {
	action := uintptr(1) // modify
	if !t.added {
		action = 0 // add
	}
	if ok, _, _ := notifyIcon.Call(action, uintptr(unsafe.Pointer(&t.data))); ok != 0 {
		t.added = true
	}
}

// Notify shows a short message by the notification area icon. Windows
// holds it back while the user has notifications silenced.
func (w *Window) Notify(title, text string) {
	if w.tray == nil || !w.tray.added {
		return
	}
	d := w.tray.data
	d.Flags |= 0x10 // with a message
	fill(d.InfoTitle[:], title)
	fill(d.Info[:], text)
	d.InfoFlags = 0x1 | 0x80 // information; respect quiet time
	notifyIcon.Call(1, uintptr(unsafe.Pointer(&d)))
}

// RemoveTray takes the icon away.
func (w *Window) RemoveTray() {
	if w.tray != nil && w.tray.added {
		notifyIcon.Call(2, uintptr(unsafe.Pointer(&w.tray.data)))
		w.tray.added = false
	}
}

// trayEvent handles a click on the icon.
func (w *Window) trayEvent(event uint32) {
	if w.tray == nil {
		return
	}
	switch event {
	case 0x0202, 0x0203, 0x0405: // left click, double click, click on a message
		if w.tray.open != nil {
			w.tray.open()
		}
	case 0x0205: // right click
		if w.tray.menu != nil {
			w.popup(w.tray.menu())
		}
	}
}

// popup shows a menu at the mouse pointer and runs the entry chosen.
func (w *Window) popup(entries []MenuEntry) {
	menu, _, _ := createPopupMenu.Call()
	if menu == 0 {
		return
	}
	for i, entry := range entries {
		if entry.Text == "" {
			appendMenu.Call(menu, 0x800, 0, 0)
			continue
		}
		flags := uintptr(0)
		if entry.Greyed {
			flags = 0x1
		}
		appendMenu.Call(menu, flags, uintptr(i+1), uintptr(unsafe.Pointer(ptr(entry.Text))))
	}
	var at point
	getCursorPos.Call(uintptr(unsafe.Pointer(&at)))
	// Without this the menu would stay open when the user clicks elsewhere.
	setForeground.Call(w.Handle)
	const returnChoice, quiet, rightButton = 0x100, 0x80, 0x2
	chosen, _, _ := trackPopupMenu.Call(menu, returnChoice|quiet|rightButton, uintptr(at.X), uintptr(at.Y), 0, w.Handle, 0)
	postMessage.Call(w.Handle, 0, 0, 0)
	destroyMenu.Call(menu)
	if chosen > 0 && int(chosen) <= len(entries) {
		if action := entries[chosen-1].Action; action != nil {
			action()
		}
	}
}
