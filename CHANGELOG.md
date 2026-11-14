# Changelog

## 1.1.0 - 2026-11-14

Recording now starts when you start it, and the window is new.

- Opening LazaReplay records nothing. Recording starts with **Start recording**, in the window or on the menu of the icon by the clock, and **Stop recording** stops it and wipes the loop. Start and Stop replace Pause and Resume.
- While nothing is being recorded the Save button and the Save entry of the menu are greyed, and the shortcut and `LazaReplay.exe --save` save nothing and say why: on the line at the foot of the window, by the clock when the window is closed, and with the Windows warning sound when the sound is switched on.
- `LazaReplay.exe --tray` and **Start with Windows** open LazaReplay by the clock without recording.
- New setting, off by default: **Start recording when LazaReplay opens**, for those who want it to record without being asked.
- New switches: `--start` starts recording, opening LazaReplay first if it is not running, and `--stop` stops it.
- The icon by the clock is a plain ring while nothing is recorded and gains a red dot while recording.
- A new window: black and see-through, with what is behind it blurred, rounded buttons, lists and switches, a light that shows the state, and a matching dark menu on the icon by the clock. The button for the thing to do next is the light one.
- Controls show when the pointer is over them, when they are pressed, when they cannot be used, and, with a white ring, where the keyboard is. Lists open with Enter, Space or an arrow key and change nothing until an entry is chosen.
- The window is drawn by LazaReplay itself, only where something changed, and not at all while it is hidden or minimised. No web view, no third-party modules, CGO still off.
- A settings file from 1.0.0 is read as it is. It leaves recording off until you start it.

## 1.0.0 - 2026-11-12

First public release.

- Keep the last 30 seconds to 10 minutes of a screen in memory and save them as an MP4 on request.
- Save with a shortcut that works in any program, with a button, from the icon by the clock, or with `LazaReplay.exe --save`.
- Capture with desktop duplication, convert on the graphics chip, and compress with the hardware H.264 encoder of the chip that drives the screen: Intel Quick Sync and the AMD encoder through Media Foundation, NVIDIA NVENC directly.
- Fall back to the next encoder when one fails, ending with the software encoder built into Windows at 1280 by 720 and 30 pictures a second.
- Record what the computer plays as AAC sound, kept in step with the picture and filled with silence while nothing plays.
- Draw the mouse pointer into the recording, including pointers that invert the screen.
- Ride out the lock screen, administrator prompts, resolution changes and driver restarts without losing the loop.
- Choose the length, quality, picture size and rate, screen, shortcut and folder, with the memory cost shown beside each choice.
- Start with Windows on request, run from the notification area, and wipe the loop on pause and on exit.
- Include `lazareplay-check.exe`, which records a generated picture through the same pipeline, plays the clip back and checks it by numbers.
- Include Windows build and release automation, and tests.
