# Validation

This file records what was checked before release, how, and where the checking stops. It is meant to be read with some scepticism: a recorder touches graphics drivers, and one computer is not every computer.

## The method

A screen recording cannot be checked by looking at it, not reliably, and not by anyone who should not see what was on the screen. So LazaReplay is checked against a picture in which every pixel is known.

`internal/testcard` draws a card with eight flat colour bars, its own frame number written as sixteen black or white squares, a square that moves every frame, and a lamp that is lit for the first half of every second. A generated source feeds one frame of the card to the recorder at every tick, in place of the screen. The recording therefore passes through the real conversion, the real encoder, the real loop and the real MP4 writer. The saved file is then played back through the decoders built into Windows and read as numbers:

- **Frames.** Every frame number must appear once, in order. A dropped, repeated or reordered picture shows at once.
- **Colours.** Each bar is compared with the value the BT.709 standard gives for it.
- **Pointer.** The source reports a mouse pointer of each kind Windows has, over different bars. The colour under each part of each pointer is known: solid, half transparent, replaced, inverted.
- **Timing.** The time between pictures must be one tick.
- **Sound.** A generated sound source makes a click on every whole second, counted from the same zero as the lamp. In the clip, each click must fall where the lamp comes on.

This is what `lazareplay-check.exe` runs. The same check was repeated with variations during development.

## Results on the development computer

Intel Core 9 270H, Intel graphics driving the built-in 1920 by 1080 screen at 144 Hz, GeForce RTX 5070 driving a second screen, Windows 10 build 19045.

| Check | Intel Quick Sync | NVIDIA NVENC | Software encoder |
| --- | --- | --- | --- |
| Frames in a five-second clip | 302, none missing, repeated or out of order | 300, the same | 150 at 30 a second, the same |
| Colour bars, largest error out of 255 | 0.4 | 1.3 | 2.0 |
| Mouse pointer, three kinds | 0.4 | 0.4 | not checked at the reduced size |
| Time between pictures | 16.67 ms | 16.67 ms | 33.33 ms |
| Sound against picture | within 5 ms | within 5 ms | not checked |

The sound figure comes from a stricter version of the check, in which the card shows the time each picture was taken in milliseconds, so the position a click should have in the file is known without the uncertainty of one tick. Over three recordings on each encoder the clicks were 3.3 to 5.1 ms early.

Variations that were run and passed, with every frame present and the colours within the same limits:

- A loop of 4 seconds recorded for 9: the clip held the last 4.7 seconds and started on a keyframe.
- A screen that stops changing: one picture a second, a keyframe every two.
- Screens of 2560 by 1440 and 3840 by 2160 recorded at 1080p, and 3840 by 2160 recorded at full size at 60 a second, on both hardware encoders.
- A screen of 5120 by 1440, scaled to 4096 by 1152.
- 30 pictures a second.
- Conversion by drawing and by the chip's video processor, on both chips.

## A second decoder

A file that plays in one decoder can still be wrong. The clips were therefore also opened in a Chromium-based browser, whose MP4 reader and H.264 and AAC decoders share no code with Windows Media Foundation.

- The browser read the picture as 1920 by 1080 and the length to the millisecond.
- Seeking to six points gave the frame numbers expected at those times.
- The colour bars, as the browser showed them in RGB, were 255,255,255, then 254,255,0, 0,254,255, 0,255,1, 255,0,254, 255,1,0, 1,0,255 and 0,0,0: correct to within one level. This is the check that the file describes its colours in a way players act on.
- The clicks in the decoded sound were at the same positions as Windows gave, to a tenth of a millisecond.

The browser check used a temporary local file server that is not part of the release.

## The real screen and the real speakers

- The card was shown in a window on the real screen, redrawn 144 times a second, and the screen was recorded at 60 a second: 400 pictures in 6.7 seconds, none repeated or out of order, the colours of the card within 0.4. The frame numbers advanced by 2.4 a picture, which is 144 over 60.
- A two-second 1000 Hz tone was played at about one two-hundredth of full volume and recorded from the default playback device. All of the sound energy in the clip was at 1000 Hz.
- The second screen, on the NVIDIA chip, was recorded through NVENC.

## Cost

Measured with Windows' own counters on the development computer, recording 1920 by 1080 at 60 a second with sound, the screen redrawing 144 times a second, the window hidden.

| Resource | Use |
| --- | --- |
| Processor | 6.8 to 6.9% of one core over ten seconds, twice |
| Intel chip | 3.6% of the 3D engine, 11 to 12% of the video engine |
| NVIDIA chip | none |
| Capture thread | 1.0 ms of processor time per picture, of which 0.5 ms is handing the picture to the Intel encoder |

Conversion by drawing took 0.009 ms per picture against 0.37 to 0.42 ms for the video processor. With the video processor the whole process used 12.5% of one core, with drawing 8.5%, in back-to-back runs. When another program loaded the machine during a test, the video processor path fell behind, with 155 pictures late out of 640, while drawing had 44.

## A longer run

The finished app was left running for seven minutes with the window hidden and the default five-minute loop, recording the real screen at 1920 by 1080 and 60 a second with sound, while the computer was in ordinary use.

| Time | Processor, share of one core | Memory |
| --- | --- | --- |
| 30 s | 6.0% | 170 MB |
| 2 min | 6.2% | 252 MB |
| 4 min | 6.6% | 360 MB |
| 5 min | 6.8% | 420 MB |
| 6 min | 7.1% | 448 MB |
| 7 min | 6.4% | 457 MB |

Memory rose as the loop filled and levelled off once it was full. The number of threads and handles did not grow. The clip saved at the end was 274 MB and 5 minutes 1.9 seconds long, with 17,837 pictures, the longest gap between two of them 67 ms, and a sound track 41 ms shorter than the pictures.

To separate a leak from a loop that is merely filling, the generated picture, which costs the same every second, was recorded for 150 seconds with a loop of 20 seconds. From 75 seconds to the end the memory of the process stayed between 176.0 and 176.1 MB, and the number of handles did not move. All 8,988 pictures were recorded without one waiting for the encoder, and the clip saved at the end held the last 20.7 seconds, every frame present and in order.

Two failures were provoked on purpose, since real hardware seldom provides them:

- The generated source was made to fail every 90 pictures in the way a monitor does when Windows takes it away for the lock screen. Recording carried on without a gap longer than two ticks.
- The hardware encoder was made to report a failure after 40 pictures. After the second failure the recorder passed it over, started the software encoder and carried on; the clip from that point played back with every frame in order.

## Starting by hand, and the new window

Version 1.1.0 changed when recording starts and replaced the window. The built `LazaReplay.exe` was run on the development computer with a settings file of its own, and worked by sending its window the messages a mouse and a keyboard send. What it showed was read from copies of the window's picture and of its icon by the clock, its cost from Windows' own counters, and its clips with `lazareplay-check verify`.

| Step | What was seen |
| --- | --- |
| Open the program | The window says Not recording and the icon is a plain ring. 31 ms of processor time in the first six seconds, 19 MB of memory |
| Shortcut, then `--save`, while not recording | No file. The line at the foot says there is nothing to save |
| Start recording | Recording within three seconds, a red light in the window and a red dot on the icon |
| Shortcut while recording | One clip: 8.7 seconds, 87 pictures of a still screen, a sound track of the same length, decoded by Windows' decoders |
| Stop recording | No processor time in four seconds. The shortcut saves nothing |
| Start again, save four seconds later | A clip of 3.7 seconds: only footage from after the restart, so stopping had wiped the loop |
| `--tray` | No window, plain ring, no processor time in three seconds |
| `--start`, `--save`, `--stop`, `--exit` to the running copy | Recording with the red dot; one clip; plain ring and no further clip; closed |
| `--save`, `--stop`, `--exit` with no copy running | Nothing opens |
| `--tray --start` | No window, recording |
| Start with Windows, on then off | The entry appears in the start-up list with `--tray`, then is gone |
| Start recording when LazaReplay opens, on, then `--tray` | Recording without being asked. Off by default |

The window itself:

- **Readable.** It was shown over a white screen, a colourful picture and a picture of sharp black text, stripes and saturated colour. The text of the window stayed readable and nothing behind it could be read. A test measures the same thing by numbers for every kind of text, over white and over black, and requires the contrast the accessibility guidelines for the web ask of body text.
- **Blur.** Windows blurred what was behind the window with its **Transparency effects** setting turned off, which is how the development computer is set.
- **Scale.** Drawn at 100%, 125% and 150%. Asked for 200% on a 1920 by 1080 screen it drew itself at 153%, the largest that fits.
- **Keyboard and pointer.** Lists, switches, buttons, the shortcut box and the ring that shows where the keyboard is were worked in the running program, and are covered by tests that need no screen.
- **Dragging.** Holding the top of the window and moving the pointer moved the window by exactly the distance moved; holding a button or the lower part did not move it.
- **Cost.** Recording a busy screen, the program used 703 ms of processor time in ten seconds with the window open and 875 ms with it closed, so the window adds nothing that can be told from the recorder's own variation. A test confirms that a hidden window is not drawn.

## Automated tests

73 tests run on every push: all of them on Windows, and those that do not need Windows on Linux as well, with the race detector.

| Package | What is tested |
| --- | --- |
| `internal/ring` | The window is kept and trimmed at keyframes; the byte ceiling; a snapshot is not disturbed by later pictures; new parameter sets start a fresh clip; reset |
| `internal/mp4` | A file is written and read back through its own tables, with 32-bit and 64-bit offsets; inconsistent input is refused before a byte is written |
| `internal/h264` | Streams are split into units; parameter sets are parsed, with escape bytes; damaged ones are rejected |
| `internal/audio` | The sound timeline: steady sound, gaps filled with silence, a slow clock followed without inventing samples, overlaps dropped |
| `internal/dx` | The three kinds of pointer are turned into the right layers; damaged shapes are refused; pictures are fitted without changing shape |
| `internal/settings` | Round trip; damaged and out-of-range files fall back to the defaults; a first run and a file from 1.0.0 leave recording off |
| `internal/winui` | The drawing code: smooth rounded shapes, clipping, pixels that stay valid however colours are laid over each other. The colours: every kind of text readable over white and over black. The keys a shortcut box takes. On Windows: the order Tab visits controls, lists, switches and buttons with keyboard and pointer, redrawing only what changed, and nothing drawn while hidden |
| `cmd/lazareplay` | On Windows: the command line; what the window, the buttons and the icon by the clock show while stopped, starting, recording, saving and held up; the menu of the icon |
| `internal/autostart` | Writing, reading and removing an entry, in a key of its own |

The tests do not need a graphics chip. Everything that does is covered by the self-check, which cannot run on GitHub's build machines and is run by hand.

## Where the checking stops

- **One computer.** Everything above was measured on one laptop. Intel Quick Sync and NVIDIA NVENC were exercised on it. No AMD chip was available, so the AMD route is written to the same interface as the Intel one and has never been run.
- **NVIDIA desktop cards.** NVENC was tested on a laptop RTX 5070 with a current driver. The interface version used, 11.1, needs driver 471.41 or newer; older drivers fall back to NVIDIA's Media Foundation encoder, which could not be tested here because it does not start on this laptop.
- **Windows 11.** Developed and tested on Windows 10 build 19045. The interfaces used are the same on Windows 11. The new window asks Windows 11 for rounded corners and keeps the stronger blur while it is dragged; neither has been seen there.
- **The window by eye.** How smoothly the window follows the pointer was checked by position, not watched. High contrast was not turned on to see the solid window. Versions of Windows 10 older than April 2018, which get the weaker blur, were not tried.
- **Screen readers.** The window draws its own controls and a screen reader cannot read them. The menu of the icon by the clock carries the text screen readers look for; that was not tried with one.
- **Games.** The cost was not measured while a game was running. On a computer with one graphics chip, recording shares that chip with the game; how the two share it under full load is untested.
- **Turned and HDR screens.** Neither was available. A turned screen is recorded sideways without the pointer by design; an HDR screen is expected to record in standard range.
- **Changing playback device.** The code that moves sound capture to a new default device was not exercised.
- **Long runs.** The longest run is the one described above.
- **Protected video.** Not tested; Windows documents that it is withheld.

A report from `lazareplay-check.exe` on hardware not listed here is the most useful contribution the project can receive.
