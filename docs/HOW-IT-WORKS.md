# How LazaReplay works

LazaReplay is a pipeline with five stages. The first three happen on the graphics chip; only the compressed result reaches main memory.

```text
screen ──► capture ──► convert ──► encode ──► loop in memory ──► MP4 file
          (Windows)   (chip)      (chip)     (compressed)       (on request)
                                       ▲
sound ──► capture ──► AAC encode ──────┘
```

The aim throughout is to do as little as possible, as rarely as possible, and never on the chip a game is using if another is available.

## 1. Capture

Windows composes the desktop on the graphics chip. An interface called desktop duplication hands a program that picture as a texture, a block of pixels that stays on the chip. LazaReplay asks for it on the chip that drives the screen, since only that chip has it.

Two details decide the cost.

**Holding the picture.** While a program holds a duplicated frame, Windows only notes which parts of the desktop change. It copies the newest picture once, when the next frame is asked for. A program that lets go early has Windows copy every update, which on a 144 Hz screen is 144 copies a second. LazaReplay holds each frame until the moment it asks for the next, so the desktop is copied 60 times a second however fast the screen redraws.

**Waiting, not polling.** After a frame is let go, the next one is not ready at once: it arrives with the screen's next redraw. Asking with no wait finds nothing every second time and halves the frame rate. LazaReplay asks with a wait of most of one tick, and the thread sleeps inside that request. A still screen therefore costs nothing: there is no redraw to wake it.

The loop ticks on a high-resolution timer, 60 or 30 times a second. Pictures are stamped with the time of their tick, so a clip of a busy screen has evenly spaced pictures, and a clip of a quiet one simply has fewer.

The duplicated picture has no mouse pointer in it; Windows draws the pointer separately. When the pointer is showing, LazaReplay copies the frame and draws the pointer on the copy, in two passes: one painted through its transparency, one that inverts the screen where the pointer's mask says so, which is how the text cursor stays visible on any background. When the pointer is hidden, as in most games, the frame is used as it is and nothing is copied.

Capture can be lost for a while: the lock screen, an administrator prompt, a change of resolution, a game switching to exclusive full screen. LazaReplay keeps repeating the last picture once a second until the screen is back, so the sound and the timeline carry on.

## 2. Convert

Encoders take a format called NV12: a full-size plane of brightness values and a half-size plane of colour values. The desktop is in BGRA, four bytes a pixel.

LazaReplay converts by drawing. Two small programs run by the chip for every pixel write the two planes, using the numbers of the BT.709 standard at studio range, which is what players assume for HD video. Four samples are averaged for each output pixel; for the colour plane that covers the four screen pixels behind it, and when the picture is being scaled down it smooths what a single sample would skip.

Windows also offers a video processor that does the same in one call. On the Intel chip used for development that call cost 0.37 milliseconds of processor time per picture against 0.009 for drawing, so drawing is preferred and the video processor is the fallback for a chip that cannot draw into the planes of an NV12 texture.

## 3. Encode

The converted texture goes to a hardware H.264 encoder on the same chip, so there is still no copy.

| Chip | Route |
| --- | --- |
| Intel | Quick Sync Video, as a Media Foundation transform |
| NVIDIA | NVENC, through `nvEncodeAPI64.dll`, which the driver installs |
| AMD | The AMD encoder, as a Media Foundation transform |
| Any | The software encoder built into Windows, as a last resort |

NVIDIA's encoder is driven directly because the Media Foundation transform NVIDIA supplies fails to start on some machines, laptops with two graphics chips among them. The direct route does not depend on it.

The settings are the same on every route: variable bitrate with a ceiling, so a still screen spends almost no bits and a busy one is capped; no pictures that depend on later ones, so pictures come out in the order they went in; and a keyframe requested by time, every two seconds.

Hardware encoders work in the background. LazaReplay never waits for one: at each tick it collects what is finished and hands over the next picture only if the encoder is ready for it. If it is not, the picture stays held and is offered again on the next tick, so nothing is lost, it is only late.

If an encoder breaks down soon after starting, twice, it is passed over in favour of the next in the list. That is how the recorder copes with hardware it has never met. The software encoder is kept to 1280 by 720 at 30 pictures a second, because compressing on the processor takes real work away from whatever else is running.

## 4. The loop

Compressed video can only start playing from a keyframe. The loop is therefore a list of groups, each one keyframe and the pictures that depend on it, about two seconds long. A group is dropped once the group after it starts before the edge of the window, so the edge is always covered and a full loop gives a clip of at least the length asked for.

A group is never changed after it is closed. Saving copies the list of groups, which takes microseconds, and then writes the file from that copy while recording carries on; nothing is locked and no footage is duplicated.

The size of the loop is bounded twice: by time, and by a ceiling in bytes worked out from the encoder's peak bitrate.

## 5. Sound

Windows offers a copy of whatever is on its way to the speakers, called loopback capture. No microphone is involved in that. Two things make it awkward, and both are handled in `internal/audio/timeline.go`.

- Windows delivers nothing at all while nothing is playing. The gaps are filled with silence, or the sound track would be shorter than the pictures and drift out of step.
- The sound card's clock runs very slightly fast or slow against the computer's. The timeline follows the times Windows reports, a little at a time, instead of trusting a count of samples for ever.

A microphone, if the user chose one, is captured separately and asked of Windows in the same form, 48,000 samples a second in stereo, whatever the microphone itself delivers. Its packets arrive on their own schedule, so each block of the computer's sound waits, for up to a quarter of a second, until the microphone's sound of the same moment has come. The two are then added sample by sample; a sum too loud for a sample is held at the loudest value. If the microphone fails, the computer's sound goes on alone and the microphone is tried again every two seconds.

Sound is compressed to AAC by the encoder built into Windows and stored in the loop alongside the pictures of the same moment.

## 6. The file

The MP4 writer is written for this project. It lays the file out for instant playback: the index of every picture comes first and the pictures after it, so a player can start, or jump to any point, without reading to the end. That layout needs every size to be known before the first byte is written, which is the case here because a clip is complete before it is saved.

The file is written under a temporary name and renamed when complete, so a clip either exists whole or not at all. The writing thread runs at background priority.

## 7. The window

The window is black, see-through and has rounded controls. Windows has no controls like that, and its own text drawing knows nothing of opacity, so LazaReplay draws the window itself. It stays a native Windows program: there is no web view and no interface toolkit, only calls to Windows.

**One picture.** The whole window is a picture in memory in which every pixel has its own opacity. Rounded shapes are drawn by working out, for each pixel near an edge, how much of the pixel the shape covers, which is what makes a curve look smooth instead of stepped. The picture is handed to Windows as a layered window, a kind of window whose opacity is set pixel by pixel. Windows keeps its own copy, so moving the window or uncovering it costs LazaReplay nothing.

**Text.** Windows draws the letters, white on black, on a second picture. That picture is then used as a stencil: where it is white the text colour is laid on the window, where it is grey, partly. Drawing the letters straight on the window would punch see-through holes in it.

**The blur.** A window that only let the desktop show through would be hard to read over a page of text. LazaReplay asks Windows to blur what is behind the window, the way Windows blurs its own Start menu, and lays a black sheet that is four fifths solid over the result. The request is one Microsoft does not document, although it has kept its form since 2015; where it is missing or refused the sheet is made more solid instead, and with high contrast turned on in Windows it is fully solid. The colours are checked by a test that lays them over a white screen and a black one and measures the contrast of every kind of text.

**Only what changed.** A control that changes marks its own rectangle as out of date. When the message that caused the change has been dealt with, that rectangle is redrawn and copied to Windows, once. There is no animation and no timer that repaints. While the window is hidden or minimised nothing is drawn at all, and the half-second timer that refreshes the figures is stopped; opening the window draws it once, complete.

**Keyboard and pointer.** With no Windows controls there is nothing to do this for free, so the window keeps track itself of which control the pointer is over, which is pressed and which has the keyboard, and of the list that is open.

## Layout of the source

| Path | Contents |
| --- | --- |
| `internal/dx` | Direct3D: adapters and screens, desktop duplication, conversion, pointer drawing |
| `internal/mf` | Media Foundation: the H.264 and AAC encoders, and a reader used only to check clips |
| `internal/nvenc` | NVIDIA's encoder, driven directly |
| `internal/audio` | Loopback and microphone capture, and the sound timeline |
| `internal/ring` | The loop |
| `internal/h264` | Reads an H.264 stream far enough to store it |
| `internal/mp4` | The MP4 writer |
| `internal/recorder` | Ties the stages together; chooses the encoder; saves clips |
| `internal/clock` | One time base and the frame timer |
| `internal/com` | The few helpers needed to call Windows COM interfaces |
| `internal/settings`, `internal/autostart` | Remembered choices, and starting with Windows |
| `internal/testcard` | A picture in which every pixel is known, for the self-check |
| `internal/winui` | The window: the drawing code, the controls, the icon by the clock and its menu |
| `cmd/lazareplay` | The app: what each state shows, starting and stopping, saving, the command line |
| `cmd/lazareplay-check` | The self-check |
| `tools/mkres` | Draws the icon and builds the Windows resource object |

There are no third-party modules. Windows interfaces are called through their method tables with `syscall`, and CGO is off.
