# Changelog

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
