//go:build windows

// Package winui provides a small native Win32 interface without third-party dependencies.
package winui

import (
	"fmt"
	"math"
	"runtime"
	"sync"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	comctl32 = syscall.NewLazyDLL("comctl32.dll")
	ole32    = syscall.NewLazyDLL("ole32.dll")

	createWindow    = user32.NewProc("CreateWindowExW")
	sendMessage     = user32.NewProc("SendMessageW")
	postMessage     = user32.NewProc("PostMessageW")
	showWindow      = user32.NewProc("ShowWindow")
	enableWindow    = user32.NewProc("EnableWindow")
	setText         = user32.NewProc("SetWindowTextW")
	getText         = user32.NewProc("GetWindowTextW")
	getTextLength   = user32.NewProc("GetWindowTextLengthW")
	loadImage       = user32.NewProc("LoadImageW")
	invalidateRect  = user32.NewProc("InvalidateRect")
	fillRect        = user32.NewProc("FillRect")
	frameRect       = user32.NewProc("FrameRect")
	isWindowVisible = user32.NewProc("IsWindowVisible")
	isIconic        = user32.NewProc("IsIconic")
	setForeground   = user32.NewProc("SetForegroundWindow")
	setBkColor      = gdi32.NewProc("SetBkColor")
	setTextColor    = gdi32.NewProc("SetTextColor")
	createBrush     = gdi32.NewProc("CreateSolidBrush")
)

// Window messages and notifications.
const (
	wmDestroy     = 0x0002
	wmClose       = 0x0010
	wmDrawItem    = 0x002B
	wmSetFont     = 0x0030
	wmCommand     = 0x0111
	wmTimer       = 0x0113
	wmColorEdit   = 0x0133
	wmColorButton = 0x0135
	wmColorStatic = 0x0138
	wmHotkey      = 0x0312
	wmJobs        = 0x8001 // WM_APP + 1: run the functions queued by Post
	wmTray        = 0x8002 // WM_APP + 2: the notification area icon was used
	wmSignal      = 0x8003 // WM_APP + 3: another copy of the program asks for something

	clicked  = 0     // a button was pressed
	selected = 1     // another entry of a list was chosen
	retyped  = 0x300 // the contents of a box changed
)

// Colours, as 0x00BBGGRR.
const (
	colorWindow = 0x00FAFAFA
	colorField  = 0x00FFFFFF
	colorText   = 0x00202020
	colorMuted  = 0x00707070
	colorEdge   = 0x00C4C4C4
	colorFill   = 0x00303030
)

type point struct{ X, Y int32 }
type rect struct{ Left, Top, Right, Bottom int32 }
type message struct {
	HWND           uintptr
	ID             uint32
	WParam, LParam uintptr
	Time           uint32
	Point          point
	Private        uint32
}
type windowClass struct {
	Size, Style                        uint32
	Procedure                          uintptr
	ClassExtra, WindowExtra            int32
	Instance, Icon, Cursor, Background uintptr
	MenuName, ClassName                *uint16
	SmallIcon                          uintptr
}

type Window struct {
	Handle                       uintptr
	instance                     uintptr
	scale                        float64
	font, titleFont, headingFont uintptr
	brush, fieldBrush            uintptr
	edgeBrush, fillBrush         uintptr
	pages                        map[int][]uintptr
	sections                     map[int]uintptr // the button that shows each page
	page                         int
	nextID                       uintptr
	actions                      map[uintptr]func() // by control ID and notification
	fields                       map[uintptr]bool   // edit boxes, which are drawn white
	muted                        map[uintptr]bool   // labels drawn in grey
	texts                        map[uintptr]string // what each control shows, to skip needless redraws
	bars                         map[uintptr]*Bar
	shown                        func(page int)
	tick                         func()
	interval                     int
	signal                       func(command int)
	hotkey                       func(id int)
	closing                      func() bool
	tray                         *tray
	taskbarCreated               uint32
	jobs                         []func()
	lock                         sync.Mutex
	callback                     uintptr
}

func wide(s string) []uint16     { return append(utf16.Encode([]rune(s)), 0) }
func ptr(s string) *uint16       { return &wide(s)[0] }
func (w *Window) px(n int) int32 { return int32(math.Round(float64(n) * w.scale)) }

// Signal sends a number to the window of a copy of the program that is
// already running, which receives it through OnSignal. It reports whether
// there was such a copy.
func Signal(class string, command int) bool {
	h, _, _ := user32.NewProc("FindWindowW").Call(uintptr(unsafe.Pointer(ptr(class))), 0)
	if h == 0 {
		return false
	}
	user32.NewProc("AllowSetForegroundWindow").Call(^uintptr(0)) // so it may come to the front
	postMessage.Call(h, wmSignal, uintptr(command), 0)
	return true
}

// OnSignal sets the function that receives numbers sent with Signal.
func (w *Window) OnSignal(signal func(command int)) { w.signal = signal }

// New creates the main window with a fixed layout of the given size.
func New(title, class string, width, height int) (*Window, error) {
	runtime.LockOSThread()
	user32.NewProc("SetProcessDPIAware").Call()
	controls := struct{ Size, Classes uint32 }{8, 0x1 | 0x4 | 0x40} // lists, bars and the shortcut box
	comctl32.NewProc("InitCommonControlsEx").Call(uintptr(unsafe.Pointer(&controls)))
	dpi := uintptr(96)
	if p := user32.NewProc("GetDpiForSystem"); p.Find() == nil {
		dpi, _, _ = p.Call()
	}
	if dpi == 0 {
		dpi = 96
	}
	w := &Window{
		scale: float64(dpi) / 96, pages: map[int][]uintptr{}, sections: map[int]uintptr{}, actions: map[uintptr]func(){},
		fields: map[uintptr]bool{}, muted: map[uintptr]bool{}, texts: map[uintptr]string{}, bars: map[uintptr]*Bar{}, nextID: 100, page: -1,
	}
	screenWidth, _, _ := user32.NewProc("GetSystemMetrics").Call(0)
	screenHeight, _, _ := user32.NewProc("GetSystemMetrics").Call(1)
	// Keep the complete fixed layout visible even with a small screen or high scaling.
	w.scale = math.Min(w.scale, math.Min(float64(screenWidth-60)/float64(width), float64(screenHeight-100)/float64(height)))
	if w.scale < 0.7 {
		w.scale = 0.7
	}
	w.instance, _, _ = kernel32.NewProc("GetModuleHandleW").Call(0)
	w.brush, _, _ = createBrush.Call(colorWindow)
	w.fieldBrush, _, _ = createBrush.Call(colorField)
	w.edgeBrush, _, _ = createBrush.Call(colorEdge)
	w.fillBrush, _, _ = createBrush.Call(colorFill)
	cursor, _, _ := user32.NewProc("LoadCursorW").Call(0, 32512)
	icon, _, _ := loadImage.Call(w.instance, 1, 1, 32, 32, 0)
	small, _, _ := loadImage.Call(w.instance, 1, 1, 16, 16, 0)
	w.callback = syscall.NewCallback(w.handle)
	className := ptr(class)
	wc := windowClass{Style: 3, Procedure: w.callback, Instance: w.instance, Icon: icon, SmallIcon: small, Cursor: cursor, Background: w.brush, ClassName: className}
	wc.Size = uint32(unsafe.Sizeof(wc))
	ok, _, e := user32.NewProc("RegisterClassExW").Call(uintptr(unsafe.Pointer(&wc)))
	if ok == 0 {
		return nil, fmt.Errorf("could not register window: %w", e)
	}
	// A fixed-size window with a title bar; child controls are not painted over.
	style := uintptr(0x00ca0000 | 0x02000000)
	r := rect{Right: w.px(width), Bottom: w.px(height)}
	user32.NewProc("AdjustWindowRectEx").Call(uintptr(unsafe.Pointer(&r)), style, 0, 0)
	winWidth, winHeight := r.Right-r.Left, r.Bottom-r.Top
	x, y := (int32(screenWidth)-winWidth)/2, (int32(screenHeight)-winHeight)/2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	h, _, e := createWindow.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(ptr(title))), style, uintptr(x), uintptr(y), uintptr(winWidth), uintptr(winHeight), 0, 0, w.instance, 0)
	if h == 0 {
		return nil, fmt.Errorf("could not create window: %w", e)
	}
	w.Handle = h
	w.font = w.makeFont(16, 400)
	w.titleFont = w.makeFont(30, 700)
	w.headingFont = w.makeFont(20, 600)
	// Explorer announces itself with this message when it restarts, and the
	// notification area icon has to be put back.
	created, _, _ := user32.NewProc("RegisterWindowMessageW").Call(uintptr(unsafe.Pointer(ptr("TaskbarCreated"))))
	w.taskbarCreated = uint32(created)
	return w, nil
}

// handle is the window procedure.
func (w *Window) handle(hwnd uintptr, msg uint32, wp uintptr, lp unsafe.Pointer) uintptr {
	switch msg {
	case wmCommand:
		if action := w.actions[wp&0xffffffff]; action != nil {
			action()
			return 0
		}
	case wmDrawItem:
		item := (*drawItem)(lp)
		if bar := w.bars[item.Window]; bar != nil {
			bar.draw(w, item)
			return 1
		}
	case wmColorStatic, wmColorEdit:
		if w.fields[uintptr(lp)] {
			setBkColor.Call(wp, colorField)
			return w.fieldBrush
		}
		setBkColor.Call(wp, colorWindow)
		if w.muted[uintptr(lp)] {
			setTextColor.Call(wp, colorMuted)
		} else {
			setTextColor.Call(wp, colorText)
		}
		return w.brush
	case wmColorButton:
		return w.brush
	case wmTimer:
		if w.tick != nil {
			w.tick()
		}
		return 0
	case wmHotkey:
		if w.hotkey != nil {
			w.hotkey(int(wp))
		}
		return 0
	case wmTray:
		w.trayEvent(uint32(uintptr(lp)))
		return 0
	case wmSignal:
		if w.signal != nil {
			w.signal(int(wp))
		}
		return 0
	case wmJobs:
		w.lock.Lock()
		jobs := w.jobs
		w.jobs = nil
		w.lock.Unlock()
		for _, job := range jobs {
			job()
		}
		return 0
	case wmClose:
		if w.closing != nil && !w.closing() {
			return 0
		}
		user32.NewProc("DestroyWindow").Call(hwnd)
		return 0
	case wmDestroy:
		w.RemoveTray()
		user32.NewProc("PostQuitMessage").Call(0)
		return 0
	}
	if msg == w.taskbarCreated && w.tray != nil {
		w.tray.added = false
		w.tray.show(w)
	}
	result, _, _ := user32.NewProc("DefWindowProcW").Call(hwnd, uintptr(msg), wp, uintptr(lp))
	return result
}

func (w *Window) makeFont(size, weight int) uintptr {
	height := w.px(-size)
	font, _, _ := gdi32.NewProc("CreateFontW").Call(uintptr(height), 0, 0, 0, uintptr(weight), 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(ptr("Segoe UI"))))
	return font
}

// control creates a child control. action runs when the control sends the
// given notification.
func (w *Window) control(class, text string, style, extended uintptr, x, y, width, height int, notification uintptr, action func()) uintptr {
	id := w.nextID
	w.nextID++
	h, _, _ := createWindow.Call(extended, uintptr(unsafe.Pointer(ptr(class))), uintptr(unsafe.Pointer(ptr(text))), 0x50000000|style, uintptr(w.px(x)), uintptr(w.px(y)), uintptr(w.px(width)), uintptr(w.px(height)), w.Handle, id, 0, 0)
	sendMessage.Call(h, wmSetFont, w.font, 1)
	if w.page >= 0 {
		w.pages[w.page] = append(w.pages[w.page], h)
	}
	if action != nil {
		w.actions[notification<<16|id] = action
	}
	w.texts[h] = text
	return h
}

// Page makes the controls created next belong to one page.
func (w *Window) Page(page int) { w.page = page }

// Section adds a button that shows a page. The button of the page on show stays pressed.
func (w *Window) Section(text string, page, x, y, width, height int) uintptr {
	h := w.control("BUTTON", text, 0x11004, 0, x, y, width, height, clicked, func() { w.ShowPage(page) })
	w.sections[page] = h
	return h
}

func (w *Window) ShowPage(page int) {
	for n, h := range w.sections {
		pressed := uintptr(0)
		if n == page {
			pressed = 1
		}
		sendMessage.Call(h, 0x00f1, pressed, 0)
	}
	for n, handles := range w.pages {
		for _, h := range handles {
			show := uintptr(0)
			if n == page {
				show = 5
			}
			showWindow.Call(h, show)
		}
	}
	if w.shown != nil {
		w.shown(page)
	}
}

// OnPage sets a function that is called whenever a page is shown.
func (w *Window) OnPage(shown func(page int)) { w.shown = shown }

func (w *Window) Label(text string, x, y, width, height int) uintptr {
	return w.control("STATIC", text, 0x80, 0, x, y, width, height, 0, nil)
}

// Line is a one-line label. Text that is too long for it is cut short with
// an ellipsis at the end.
func (w *Window) Line(text string, x, y, width, height int) uintptr {
	return w.control("STATIC", text, 0x4080, 0, x, y, width, height, 0, nil)
}

// Quiet is a Line in a softer colour, for notes and the bar at the foot of the window.
func (w *Window) Quiet(text string, x, y, width, height int) uintptr {
	h := w.Line(text, x, y, width, height)
	w.muted[h] = true
	return h
}

func (w *Window) Title(text string, x, y, width, height int) uintptr {
	h := w.Label(text, x, y, width, height)
	sendMessage.Call(h, wmSetFont, w.titleFont, 1)
	return h
}

func (w *Window) Heading(text string, x, y, width, height int) uintptr {
	h := w.Label(text, x, y, width, height)
	sendMessage.Call(h, wmSetFont, w.headingFont, 1)
	return h
}

// Edit is a single-line read-only box that shows a path.
func (w *Window) Edit(text string, x, y, width, height int) uintptr {
	h := w.control("EDIT", text, 0x10880, 0x200, x, y, width, height, 0, nil)
	w.fields[h] = true
	return h
}

func (w *Window) Button(text string, x, y, width, height int, action func()) uintptr {
	return w.control("BUTTON", text, 0x10000, 0, x, y, width, height, clicked, action)
}

func (w *Window) Checkbox(text string, x, y, width, height int, action func()) uintptr {
	return w.control("BUTTON", text, 0x10003, 0, x, y, width, height, clicked, action)
}

func (w *Window) Checked(h uintptr) bool { r, _, _ := sendMessage.Call(h, 0x00f0, 0, 0); return r == 1 }

func (w *Window) Check(h uintptr, on bool) {
	state := uintptr(0)
	if on {
		state = 1
	}
	sendMessage.Call(h, 0x00f1, state, 0)
}

// Choice is a drop-down list. action runs when another entry is chosen.
func (w *Window) Choice(entries []string, x, y, width int, action func()) uintptr {
	h := w.control("COMBOBOX", "", 0x10000|0x00200000|0x3, 0, x, y, width, 320, selected, action)
	w.SetEntries(h, entries)
	return h
}

// SetEntries replaces the entries of a drop-down list.
func (w *Window) SetEntries(h uintptr, entries []string) {
	sendMessage.Call(h, 0x014B, 0, 0)
	for _, entry := range entries {
		sendMessage.Call(h, 0x0143, 0, uintptr(unsafe.Pointer(ptr(entry))))
	}
}

// Chosen returns the number of the chosen entry of a drop-down list.
func (w *Window) Chosen(h uintptr) int {
	r, _, _ := sendMessage.Call(h, 0x0147, 0, 0)
	return int(int32(r))
}

// Choose selects an entry of a drop-down list.
func (w *Window) Choose(h uintptr, entry int) { sendMessage.Call(h, 0x014E, uintptr(entry), 0) }

// Shortcut modifier keys, as Windows numbers them for global shortcuts.
const (
	Alt   = 1
	Ctrl  = 2
	Shift = 4
)

// Shortcut is a box that shows a key combination and takes a new one when
// keys are pressed in it. action runs when the combination changes.
func (w *Window) Shortcut(x, y, width, height int, action func()) uintptr {
	h := w.control("msctls_hotkey32", "", 0x10000, 0x200, x, y, width, height, retyped, action)
	// A key on its own, or with Shift alone, would be taken away from every
	// other program. Windows adds Ctrl and Alt to such a choice.
	sendMessage.Call(h, 0x403, 0x1|0x2, 0x2|0x4)
	return h
}

// ShortcutKeys returns the virtual key code and modifiers shown in a
// shortcut box. key is zero when the box is empty.
func (w *Window) ShortcutKeys(h uintptr) (key, modifiers uint32) {
	r, _, _ := sendMessage.Call(h, 0x402, 0, 0)
	key = uint32(r & 0xff)
	box := uint32(r>>8) & 0xff
	if box&1 != 0 {
		modifiers |= Shift
	}
	if box&2 != 0 {
		modifiers |= Ctrl
	}
	if box&4 != 0 {
		modifiers |= Alt
	}
	return key, modifiers
}

// SetShortcutKeys shows a key combination in a shortcut box.
func (w *Window) SetShortcutKeys(h uintptr, key, modifiers uint32) {
	box := uintptr(0)
	if modifiers&Shift != 0 {
		box |= 1
	}
	if modifiers&Ctrl != 0 {
		box |= 2
	}
	if modifiers&Alt != 0 {
		box |= 4
	}
	if key >= 0x21 && key <= 0x2E { // the arrow, paging and editing keys are "extended"
		box |= 8
	}
	sendMessage.Call(h, 0x401, uintptr(key)|box<<8, 0)
}

func (w *Window) Text(h uintptr) string {
	n, _, _ := getTextLength.Call(h)
	buffer := make([]uint16, n+1)
	getText.Call(h, uintptr(unsafe.Pointer(&buffer[0])), n+1)
	return syscall.UTF16ToString(buffer)
}

// SetText changes what a control shows. A control that already shows the
// text is left alone, so that a status refreshed several times a second
// does not flicker.
func (w *Window) SetText(h uintptr, text string) {
	if w.texts[h] == text {
		return
	}
	w.texts[h] = text
	setText.Call(h, uintptr(unsafe.Pointer(ptr(text))))
}

// SetPath shows a path in an edit box, scrolled to its end so that the file name is visible.
func (w *Window) SetPath(h uintptr, path string) {
	w.SetText(h, path)
	end := uintptr(len(wide(path)))
	sendMessage.Call(h, 0x00B1, end, end)
	sendMessage.Call(h, 0x00B7, 0, 0)
}

func (w *Window) Enable(h uintptr, enabled bool) {
	b := uintptr(0)
	if enabled {
		b = 1
	}
	enableWindow.Call(h, b)
}

// drawItem mirrors DRAWITEMSTRUCT.
type drawItem struct {
	Kind, ID, Item, Action, State uint32
	Window                        uintptr
	Context                       uintptr
	Area                          rect
	Data                          uintptr
}

// Bar is a plain level bar: a thin outlined track filled from the left.
type Bar struct {
	handle uintptr
	level  float64
	filled int32
}

// Bar adds a level bar.
func (w *Window) Bar(x, y, width, height int) *Bar {
	b := &Bar{filled: -1}
	b.handle = w.control("STATIC", "", 0x0D, 0, x, y, width, height, 0, nil)
	w.bars[b.handle] = b
	return b
}

// Set moves the bar to a level between 0 and 1. The bar is only redrawn
// when its filled part changes by a whole pixel.
func (b *Bar) Set(level float64) {
	b.level = math.Max(0, math.Min(1, level))
	var area rect
	user32.NewProc("GetClientRect").Call(b.handle, uintptr(unsafe.Pointer(&area)))
	if filled := b.span(area); filled != b.filled {
		invalidateRect.Call(b.handle, 0, 0)
	}
}

func (b *Bar) span(area rect) int32 {
	return int32(math.Round(float64(area.Right-area.Left-2) * b.level))
}

func (b *Bar) draw(w *Window, item *drawItem) {
	area := item.Area
	b.filled = b.span(area)
	frameRect.Call(item.Context, uintptr(unsafe.Pointer(&area)), w.edgeBrush)
	inner := rect{area.Left + 1, area.Top + 1, area.Right - 1, area.Bottom - 1}
	done, rest := inner, inner
	done.Right = inner.Left + b.filled
	rest.Left = done.Right
	fillRect.Call(item.Context, uintptr(unsafe.Pointer(&done)), w.fillBrush)
	fillRect.Call(item.Context, uintptr(unsafe.Pointer(&rest)), w.fieldBrush)
}

// Every calls tick on the window's thread at the given interval in
// milliseconds for as long as the window is on show. A hidden window gets
// no calls at all, so it costs nothing; tick runs once more the moment the
// window is shown again.
func (w *Window) Every(milliseconds int, tick func()) {
	w.tick, w.interval = tick, milliseconds
	if visible, _, _ := isWindowVisible.Call(w.Handle); visible != 0 {
		user32.NewProc("SetTimer").Call(w.Handle, 1, uintptr(milliseconds), 0)
	}
}

// OnClose sets a function that is asked when the window is about to close.
// When it returns false the window stays.
func (w *Window) OnClose(closing func() bool) { w.closing = closing }

// SetHotkey registers a key combination that works whichever program is in
// front. id tells combinations apart when one is pressed.
func (w *Window) SetHotkey(id int, key, modifiers uint32) error {
	user32.NewProc("UnregisterHotKey").Call(w.Handle, uintptr(id))
	if key == 0 {
		return nil
	}
	const noRepeat = 0x4000
	ok, _, _ := user32.NewProc("RegisterHotKey").Call(w.Handle, uintptr(id), uintptr(modifiers|noRepeat), uintptr(key))
	if ok == 0 {
		return fmt.Errorf("another program is already using that shortcut")
	}
	return nil
}

// OnHotkey sets the function called when a registered combination is pressed.
func (w *Window) OnHotkey(pressed func(id int)) { w.hotkey = pressed }

// Show brings the window to the front, restoring it if it was hidden or minimised.
func (w *Window) Show() {
	if minimised, _, _ := isIconic.Call(w.Handle); minimised != 0 {
		showWindow.Call(w.Handle, 9)
	} else {
		showWindow.Call(w.Handle, 5)
	}
	setForeground.Call(w.Handle)
	if w.tick != nil {
		w.tick()
		user32.NewProc("SetTimer").Call(w.Handle, 1, uintptr(w.interval), 0)
	}
}

// Hide takes the window off the screen and the taskbar.
func (w *Window) Hide() {
	user32.NewProc("KillTimer").Call(w.Handle, 1)
	showWindow.Call(w.Handle, 0)
}

// Visible reports whether the window can be seen.
func (w *Window) Visible() bool {
	visible, _, _ := isWindowVisible.Call(w.Handle)
	minimised, _, _ := isIconic.Call(w.Handle)
	return visible != 0 && minimised == 0
}

// Close ends the program, whatever OnClose would say.
func (w *Window) Close() {
	w.closing = nil
	user32.NewProc("DestroyWindow").Call(w.Handle)
}

// Chime plays the plain notification sound of Windows.
func (w *Window) Chime() { user32.NewProc("MessageBeep").Call(0x40) }

func (w *Window) Alert(title, text string) {
	user32.NewProc("MessageBoxW").Call(w.Handle, uintptr(unsafe.Pointer(ptr(text))), uintptr(unsafe.Pointer(ptr(title))), 0x10)
}

// Post runs a function on the window's thread. Background work uses it to update the interface.
func (w *Window) Post(job func()) {
	w.lock.Lock()
	w.jobs = append(w.jobs, job)
	w.lock.Unlock()
	postMessage.Call(w.Handle, wmJobs, 0, 0)
}

// Run shows the window, unless hidden is set, and handles its messages
// until it closes.
func (w *Window) Run(hidden bool) {
	if !hidden {
		showWindow.Call(w.Handle, 5)
		user32.NewProc("UpdateWindow").Call(w.Handle)
		if w.tick != nil {
			user32.NewProc("SetTimer").Call(w.Handle, 1, uintptr(w.interval), 0)
		}
	}
	var msg message
	for {
		r, _, _ := user32.NewProc("GetMessageW").Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if r == 0 || int32(r) == -1 {
			break
		}
		isDialog, _, _ := user32.NewProc("IsDialogMessageW").Call(w.Handle, uintptr(unsafe.Pointer(&msg)))
		if isDialog != 0 {
			continue
		}
		user32.NewProc("TranslateMessage").Call(uintptr(unsafe.Pointer(&msg)))
		user32.NewProc("DispatchMessageW").Call(uintptr(unsafe.Pointer(&msg)))
	}
	for _, object := range []uintptr{w.font, w.titleFont, w.headingFont, w.brush, w.fieldBrush, w.edgeBrush, w.fillBrush} {
		gdi32.NewProc("DeleteObject").Call(object)
	}
}

// OpenFolder shows a folder in Explorer.
func (w *Window) OpenFolder(path string) {
	r, _, _ := shell32.NewProc("ShellExecuteW").Call(w.Handle, uintptr(unsafe.Pointer(ptr("open"))), uintptr(unsafe.Pointer(ptr(path))), 0, 0, 1)
	if r <= 32 {
		w.Alert("Could not open folder", fmt.Sprintf("Windows error %d", r))
	}
}

// Reveal opens the folder of a file in Explorer with the file selected.
func (w *Window) Reveal(path string) {
	arguments := `/select,"` + path + `"`
	shell32.NewProc("ShellExecuteW").Call(w.Handle, uintptr(unsafe.Pointer(ptr("open"))), uintptr(unsafe.Pointer(ptr("explorer.exe"))), uintptr(unsafe.Pointer(ptr(arguments))), 0, 1)
}

// VideosFolder returns the user's Videos folder, or "" when Windows cannot say.
func VideosFolder() string {
	videos := guid{0x18989B1D, 0x99B5, 0x455B, [8]byte{0x84, 0x1C, 0xAB, 0x7C, 0x74, 0xE4, 0xDD, 0xFC}}
	var path *uint16
	r, _, _ := shell32.NewProc("SHGetKnownFolderPath").Call(uintptr(unsafe.Pointer(&videos)), 0, 0, uintptr(unsafe.Pointer(&path)))
	if r != 0 || path == nil {
		return ""
	}
	defer ole32.NewProc("CoTaskMemFree").Call(uintptr(unsafe.Pointer(path)))
	return syscall.UTF16ToString((*[32768]uint16)(unsafe.Pointer(path))[:])
}

// KeyName returns the name Windows gives a key, such as F9 or Page Up.
func KeyName(key uint32) string {
	switch {
	case key >= 0x70 && key <= 0x87:
		return fmt.Sprintf("F%d", key-0x6F)
	case key >= '0' && key <= '9', key >= 'A' && key <= 'Z':
		return string(rune(key))
	}
	scan, _, _ := user32.NewProc("MapVirtualKeyW").Call(uintptr(key), 0)
	if key >= 0x21 && key <= 0x2E {
		scan |= 0x100 // the arrow, paging and editing keys share codes with the number pad
	}
	buffer := make([]uint16, 64)
	if n, _, _ := user32.NewProc("GetKeyNameTextW").Call(scan<<16, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer))); n > 0 {
		return syscall.UTF16ToString(buffer)
	}
	return fmt.Sprintf("key %d", key)
}

// Fatal reports an error that stops the app from starting.
func Fatal(title string, err error) {
	user32.NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(ptr(err.Error()))), uintptr(unsafe.Pointer(ptr(title))), 0x10)
}
